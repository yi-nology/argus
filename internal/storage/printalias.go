// printalias.go —— 指纹别名 + 近重复匹配层（v3.7.0，深挖"措辞漂移漏杀"收口）。
//
// 背景：指纹 v2 = file|line|规范化comment 的哈希，是精确身份——LLM 每轮措辞
// 微调会产生新指纹，跨轮复现计数/抑制/regret 学习环因此对 LLM 来源意见失效。
// 本层语义：精确身份不变；未命中时在同 PR 同文件内做**近重复匹配**（行号 ±2
// 且评语二元组重叠率 ≥60%），命中即并入既有条目（计数延续、抑制状态尊重），
// 并把新措辞记为别名，后续轮次精确命中零扫描。
package storage

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	akseverity "git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/agentkit/textutil"

	"argus/internal/plugin"
)

// PrintAlias 别名映射：alias_fp（某轮出现的新措辞指纹）→ print_fp（既有条目的
// 规范指纹）。一行多别名是常态（同一问题跨多轮的不同措辞）。
type PrintAlias struct {
	AliasFP   string    `gorm:"column:alias_fp;primaryKey;size:64"`
	PrintFP   string    `gorm:"column:print_fp;size:64;index"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (PrintAlias) TableName() string { return "print_aliases" }

// nearSimilarityThreshold 近重复判定阈值：归一化评语的 rune 二元组重叠率
// （按较短侧）≥ 0.6 视为同一问题的不同措辞。0.6 下 SQL 注入 vs err 未检查
// （同位置不同问题）远达不到，同一问题的措辞微调稳过。
const nearSimilarityThreshold = 0.6

// commentNear 归一化后互含或二元组重叠率达标。
// 集合构造复用 agentkit v0.10.0 沉淀的 textutil.BigramSet；度量刻意不用
// textutil.Jaccard（并集分母，更严）——本层阈值 0.6 按短侧重叠率校准，
// 换 Jaccard 会漏掉合法的措辞漂移匹配（测试钉死该行为）。
func commentNear(a, b string) bool {
	na, nb := akseverity.NormalizeComment(a), akseverity.NormalizeComment(b)
	if na == "" || nb == "" {
		return false
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return true
	}
	sa, sb := textutil.BigramSet(na), textutil.BigramSet(nb)
	if len(sa) == 0 || len(sb) == 0 {
		return false
	}
	small, large := sa, sb
	if len(sb) < len(sa) {
		small, large = sb, sa
	}
	hit := 0
	for g := range small {
		if large[g] {
			hit++
		}
	}
	return float64(hit) >= float64(len(small))*nearSimilarityThreshold
}

// resolveAliasFP 别名一跳解析（alias → 规范指纹；无映射原样返回）。
func resolveAliasFP(tx *gorm.DB, fingerprint string) string {
	var a PrintAlias
	if err := tx.Where("alias_fp = ?", fingerprint).First(&a).Error; err != nil {
		return fingerprint
	}
	return a.PrintFP
}

// findNearPrint 在同 PR 同文件的既有条目里找近重复（行号相容 + 评语近重复）。
// suppressed 行同样是候选——措辞漂移的复现应当延续抑制，而不是绕开抑制复活。
// 两级匹配（v3.8.0）：
//  1. exact：规范化评语**完全相等**——行号不限距。同 PR 推新提交使行号整体位移
//     >±2 时，±2 窗口救不回（抑制/复现/regret 全部失忆、被抑制意见复活重发）；
//     同文件同句评语视为行号漂移而非新问题。这也让 SuppressPrintNear 预插的
//     line=0 行可以被真实意见回收。优先取行号相容者（真两处同模板意见时轮内
//     byNC ±2 闸门已各自成行，此处行号相容优先可减少跨位置误并）。
//  2. near：评语近重复（二元组重叠 ≥0.6）且行号 ±2——v3.7.0 原语义不动。
//
// 返回 nil 表示无近重复；exact=true 表示走的是完全同评语通道（调用方可据此
// 让行号跟随新值，行内评论位置不漂移）。
func findNearPrint(tx *gorm.DB, platform, owner, repo, number string, f plugin.Finding) (*FindingPrint, bool, error) {
	// 不在 SQL 层限行距：exact 通道必须看到行号超窗的同评语行（行号漂移场景）；
	// 同 PR 同文件条目规模小（试点 <50），Limit(200) 足够，分级在内存做。
	var rows []FindingPrint
	if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND file = ?",
		platform, owner, repo, number, f.File).
		Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	nc := akseverity.NormalizeComment(f.Comment)
	var exact, near *FindingPrint
	for i := range rows {
		if nc != "" && akseverity.NormalizeComment(rows[i].Comment) == nc {
			// 完全同评语：行号相容者优先，否则取最新（rows 已按 last_seen_at DESC）
			if exact == nil || (linesClose(rows[i].Line, f.Line) && !linesClose(exact.Line, f.Line)) {
				exact = &rows[i]
			}
			continue
		}
		if near == nil && f.Line > 0 && linesClose(rows[i].Line, f.Line) && commentNear(rows[i].Comment, f.Comment) {
			near = &rows[i]
		}
	}
	if exact != nil {
		return exact, true, nil
	}
	if near != nil {
		return near, false, nil
	}
	return nil, false, nil
}

func linesClose(a, b int) bool { return absDiff(a, b) <= 2 }

// SuppressPrintNear 抑制通道的 file+原文 形态（API 请求无行号，无法直算 v2 指纹）：
// 先在同 PR 同文件内近重复检索既有条目并抑制其规范指纹（matched=true）；
// 未命中则以 line=0 的 v2 指纹预插 suppressed 行（matched=false）——真实意见
// 下轮入库时会经近重复层并回该行，抑制随之生效。
func (s *Store) SuppressPrintNear(ctx context.Context, platform, owner, repo, number, file, comment string, at time.Time) (fp string, matched bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []FindingPrint
		q := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND file = ?", platform, owner, repo, number, file)
		var best *FindingPrint
		if err := q.Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if commentNear(rows[i].Comment, comment) && (best == nil || rows[i].TimesSeen > best.TimesSeen) {
				best = &rows[i]
			}
		}
		if best != nil {
			best.Status = PrintSuppressed
			best.LastSeenAt = at
			matched = true
			fp = best.Print
			return tx.Save(best).Error
		}
		fp = plugin.Fingerprint(file, comment, 0)
		p := FindingPrint{
			Platform: platform, Owner: owner, Repo: repo, Number: number,
			Print: fp,
			File:  file, Comment: comment, Status: PrintSuppressed,
			FirstTaskID: "manual", LastTaskID: "manual", TimesSeen: 0,
			FirstSeenAt: at, LastSeenAt: at,
		}
		return tx.Create(&p).Error
	})
	return fp, matched, err
}

// migratePrintFingerprintsV2 存量迁移（v3.7.0；v3.8.0 起由 SchemaMigration 哨兵
// 门控，只跑一次不再每次启动全表扫）：v1 指纹 = sha256(file|规范化comment)
// （无行号、无版本域），v2 加入行号与版本域。
// 同 PR 内重算后指纹相同的行（v1 时代 regret 幽灵行/重复手工行的遗留）先按组合并：
// TimesSeen/RegretCount 取 max、抑制状态 OR、时间取早/晚、非空内容保留——否则
// 第二行 UPDATE 必撞 idx_print 唯一索引，迁移每次启动在同一行失败、永不完成
// （v3.8.0 修复）。合并后逐行回写；抑制状态、复现/regret 计数原地保留。
// 迁移后旧短码不再可解析（CHANGELOG 有告知）；单行失败不阻断启动，留待重试。
func (s *Store) migratePrintFingerprintsV2(ctx context.Context) (migrated int, err error) {
	var rows []FindingPrint
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return 0, err
	}
	type groupKey struct{ pr, nv string }
	groups := map[groupKey][]FindingPrint{}
	order := []groupKey{}
	for _, r := range rows {
		nv := plugin.Fingerprint(r.File, r.Comment, r.Line)
		k := groupKey{r.Platform + "/" + r.Owner + "/" + r.Repo + "/" + r.Number, nv}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			continue // 无碰撞，走普通逐行迁移
		}
		keep := g[0] // ID 最小（最老）者为主行
		for _, r := range g[1:] {
			if r.ID < keep.ID {
				keep = r
			}
		}
		for _, r := range g {
			if r.ID == keep.ID {
				continue
			}
			if r.TimesSeen > keep.TimesSeen {
				keep.TimesSeen = r.TimesSeen
			}
			if r.RegretCount > keep.RegretCount {
				keep.RegretCount = r.RegretCount
			}
			if r.Status == PrintSuppressed {
				keep.Status = PrintSuppressed
			}
			if r.FirstSeenAt.Before(keep.FirstSeenAt) {
				keep.FirstSeenAt = r.FirstSeenAt
			}
			if r.LastSeenAt.After(keep.LastSeenAt) {
				keep.LastSeenAt = r.LastSeenAt
			}
			if keep.File == "" {
				keep.File = r.File
			}
			if keep.Comment == "" {
				keep.Comment = r.Comment
			}
			if keep.Plugin == "" {
				keep.Plugin = r.Plugin
			}
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			keep.Print = k.nv
			keep.UpdatedAt = time.Now()
			if err := tx.Save(&keep).Error; err != nil {
				return err
			}
			ids := make([]uint, 0, len(g)-1)
			for _, r := range g {
				if r.ID != keep.ID {
					ids = append(ids, r.ID)
				}
			}
			return tx.Where("id IN ?", ids).Delete(&FindingPrint{}).Error
		}); err != nil {
			return migrated, err
		}
		migrated += len(g) - 1
	}
	for _, r := range rows {
		nv := plugin.Fingerprint(r.File, r.Comment, r.Line)
		if nv == r.Print {
			continue
		}
		res := s.db.WithContext(ctx).Model(&FindingPrint{}).Where("id = ?", r.ID).
			Update("print", nv)
		if res.Error != nil {
			return migrated, res.Error
		}
		migrated += int(res.RowsAffected)
	}
	return migrated, nil
}
