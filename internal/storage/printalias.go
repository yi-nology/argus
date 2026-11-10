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

	akreportutil "github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/domain"
)

// PrintAlias 别名映射：alias_fp（某轮出现的新措辞指纹）→ print_fp（既有条目的
// 规范指纹）。一行多别名是常态（同一问题跨多轮的不同措辞）。
// v3.9.2 起 PR 四列并入主键：指纹按 PR 归属（idx_print 唯一索引含 number），
// 旧形态 alias_fp 全局唯一主键下，两个 PR 出现同款措辞漂移（同一新指纹）时
// 第二个 PR 建别名撞主键、UpsertPrint 整体回滚。存量由 print_alias_pr_scope
// 迁移按 print_fp 反查 PR 回填（命中多 PR 逐 PR 复制，孤儿丢弃）。
type PrintAlias struct {
	Platform  string    `gorm:"column:platform;primaryKey;size:16;not null"`
	Owner     string    `gorm:"column:owner;primaryKey;size:128;not null"`
	Repo      string    `gorm:"column:repo;primaryKey;size:128;not null"`
	Number    string    `gorm:"column:number;primaryKey;size:16;not null"`
	AliasFP   string    `gorm:"column:alias_fp;primaryKey;size:64;not null"`
	PrintFP   string    `gorm:"column:print_fp;size:64;index"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (PrintAlias) TableName() string { return "print_aliases" }

// nearSimilarityThreshold 近重复判定阈值：归一化评语的 rune 二元组重叠率
// （按较短侧）≥ 0.6 视为同一问题的不同措辞。0.6 下 SQL 注入 vs err 未检查
// （同位置不同问题）远达不到，同一问题的措辞微调稳过。
const nearSimilarityThreshold = 0.6

// vagueNearSuppressedThreshold 模糊意见（line=0 的多文件聚合/摘要形态）专属
// 吸收阈值（v3.133.0）：实测同一问题的彻底重述与"同函数不同问题"的重叠率区间
// 互锁（0.237~0.319 vs 0.364），全局阈值无解——但模糊意见本身无行号锚点、
// 主张精度低，对人工已判误报的行按 ≥0.2 吸收是合理不对称：精确意见（line>0）
// 仍走 0.6/0.4 两级不动。0.2 下界与真实跨问题对（0.02~0.06）有充分间距。
const vagueNearSuppressedThreshold = 0.2

// looseNearSuppressedThreshold 抑制行专属宽松阈值（v3.130.0）：人工已判误报
// （suppressed）的行按 ≥0.4 吸收**重措辞**复现——2026-10-09 实测：同一处硬编码
// 密钥问题被模型改写为「新增了硬编码的生产密钥…」后与原措辞仅 ~0.43 相似，
// 0.6 阈值失守、抑制被绕过复活。只对 suppressed 行放宽：普通行仍按 0.6 做
// 合并/别名，宽松合并普通行会吞掉真正的新问题（同文件近邻的不同问题）。
const looseNearSuppressedThreshold = 0.4

// commentNear 归一化后互含或二元组重叠率 ≥0.6（v3.7.0 一般近重复语义）。
func commentNear(a, b string) bool { return commentMatch(a, b, nearSimilarityThreshold) }

// commentNearLoose 抑制行宽松近重复（≥0.4）：同 commentNear 度量、阈值放宽。
func commentNearLoose(a, b string) bool { return commentMatch(a, b, looseNearSuppressedThreshold) }

// commentNearVague 模糊意见吸收（≥0.2，仅 suppressed 行 + line=0 + 文件组件
// 相交三重约束下使用）。
func commentNearVague(a, b string) bool { return commentMatch(a, b, vagueNearSuppressedThreshold) }

// fileSetIntersect 逗号分隔多文件形态的组件交集（"db.go,util.go" vs "db.go"
// = 命中；同值恒命中）。
func fileSetIntersect(a, b string) bool {
	as := strings.Split(a, ",")
	for _, x := range strings.Split(b, ",") {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		for _, y := range as {
			if strings.TrimSpace(y) == x {
				return true
			}
		}
	}
	return false
}

// commentMatch 归一化后互含（无阈值）或二元组重叠率达标。
// 集合构造复用 agentkit v0.10.0 沉淀的 textutil.BigramSet；度量刻意不用
// textutil.Jaccard（并集分母，更严）——阈值按短侧重叠率校准，换 Jaccard 会
// 漏掉合法的措辞漂移匹配（测试钉死该行为）。
func commentMatch(a, b string, threshold float64) bool {
	na, nb := akreportutil.NormalizeComment(a), akreportutil.NormalizeComment(b)
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
	return float64(hit) >= float64(len(small))*threshold
}

// resolveAliasFP 别名一跳解析（alias → 规范指纹；无映射原样返回）。
// PR 域限定（v3.9.2）：别名按 PR 归属，同 alias_fp 在不同 PR 可指向各自规范行。
func resolveAliasFP(tx *gorm.DB, platform, owner, repo, number, fingerprint string) string {
	var a PrintAlias
	if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp = ?",
		platform, owner, repo, number, fingerprint).First(&a).Error; err != nil {
		return fingerprint
	}
	return a.PrintFP
}

// findNearPrint 在同 PR 同文件的既有条目里找近重复（行号相容 + 评语近重复）。
// suppressed 行同样是候选——措辞漂移的复现应当延续抑制，而不是绕开抑制复活。
// 三级匹配（v3.8.0 两级 + v3.130.0 抑制行宽松级）：
//  1. exact：规范化评语**完全相等**——行号不限距。同 PR 推新提交使行号整体位移
//     >±2 时，±2 窗口救不回（抑制/复现/regret 全部失忆、被抑制意见复活重发）；
//     同文件同句评语视为行号漂移而非新问题。这也让 SuppressPrintNear 预插的
//     line=0 行可以被真实意见回收。优先取行号相容者（真两处同模板意见时轮内
//     byNC ±2 闸门已各自成行，此处行号相容优先可减少跨位置误并）。
//  2. near：评语近重复（二元组重叠 ≥0.6）且行号 ±2——v3.7.0 原语义不动。
//  3. loose：suppressed 行的宽松近重复（≥0.4）且行号 ±2（v3.130.0）——重措辞
//     复现不再绕过人工抑制；只放宽已判误报的行，普通行不动。
//
// 返回 nil 表示无近重复；exact=true 表示走的是完全同评语通道（调用方可据此
// 让行号跟随新值，行内评论位置不漂移）。
func findNearPrint(tx *gorm.DB, platform, owner, repo, number string, f domain.Finding) (*FindingPrint, bool, error) {
	// 不在 SQL 层限行距：exact 通道必须看到行号超窗的同评语行（行号漂移场景）；
	// 同 PR 同文件条目规模小（试点 <50），Limit(200) 足够，分级在内存做。
	// 查询面含多文件组件（"db.go,util.go" 要看见 "db.go" 的行）；内存层按
	// 匹配级别各自收口。
	candFiles := map[string]bool{f.File: true}
	for _, part := range strings.Split(f.File, ",") {
		if p := strings.TrimSpace(part); p != "" {
			candFiles[p] = true
		}
	}
	fileList := make([]string, 0, len(candFiles))
	for f := range candFiles {
		fileList = append(fileList, f)
	}
	var rows []FindingPrint
	if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND file IN ?",
		platform, owner, repo, number, fileList).
		Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	nc := akreportutil.NormalizeComment(f.Comment)
	var exact, near, loose *FindingPrint
	for i := range rows {
		if nc != "" && akreportutil.NormalizeComment(rows[i].Comment) == nc {
			// 完全同评语：行号相容者优先，否则取最新（rows 已按 last_seen_at DESC）
			if exact == nil || (linesClose(rows[i].Line, f.Line) && !linesClose(exact.Line, f.Line)) {
				exact = &rows[i]
			}
			continue
		}
		switch {
		case f.Line > 0 && linesClose(rows[i].Line, f.Line):
			switch {
			case near == nil && commentNear(rows[i].Comment, f.Comment):
				near = &rows[i]
			case loose == nil && rows[i].Status == PrintSuppressed && commentNearLoose(rows[i].Comment, f.Comment):
				loose = &rows[i]
			}
		case f.Line == 0 && loose == nil && rows[i].Status == PrintSuppressed &&
			fileSetIntersect(rows[i].File, f.File) && commentNearVague(rows[i].Comment, f.Comment):
			// 模糊意见吸收（v3.133.0）：line=0 聚合形态无行号锚点，靠文件组件
			// 相交 + 0.2 话题重叠吸收进被抑制行——彻底重述不再绕过抑制。
			loose = &rows[i]
		}
	}
	if exact != nil {
		return exact, true, nil
	}
	if near != nil {
		return near, false, nil
	}
	if loose != nil {
		return loose, false, nil
	}
	return nil, false, nil
}

// absDiff 行距（v3.76.0 用内建 max 惯用法替代手写分支）。
func absDiff(a, b int) int { return max(a-b, b-a) }

func linesClose(a, b int) bool { return absDiff(a, b) <= 2 }

// SuppressPrintNear 抑制通道的 file+原文 形态（API 请求无行号，无法直算 v2 指纹）：
// 先在同 PR 同文件内近重复检索既有条目并抑制其规范指纹（matched=true）；
// 未命中则以 line=0 的 v2 指纹预插 suppressed 行（matched=false）——真实意见
// 下轮入库时会经近重复层并回该行，抑制随之生效。
func (s *Store) SuppressPrintNear(ctx context.Context, platform, owner, repo, number, file, comment string, at time.Time) (fp string, matched bool, err error) {
	err = s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
				if err := tx.Save(best).Error; err != nil {
					return err
				}
				return suppressFamily(tx, platform, owner, repo, number, at)
			}
			fp = domain.Fingerprint(file, comment, 0)
			p := FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number,
				Print: fp,
				File:  file, Comment: comment, Status: PrintSuppressed,
				FirstTaskID: "manual", LastTaskID: "manual", TimesSeen: 0,
				FirstSeenAt: at, LastSeenAt: at,
			}
			if err := tx.Create(&p).Error; err != nil {
				return err
			}
			return suppressFamily(tx, platform, owner, repo, number, at)
		})
	})
	return fp, matched, err
}

// familyRelated 家族传播判定（**有方向**，v3.133.2）：anchor=已抑制行，
// cand=候选行。双侧带行号锚点走 0.4+行距±2（同函数不同问题的 0.364 反例被挡）；
// 锚点精确、候选 line=0 走 0.2+文件组件相交（模糊聚合形态归族）；**line=0 的
// 模糊行不得向 line>0 的精确行传播**——大杂烩行同时含多问题文本，是天然的
// 「桥」（实测 blob-vs-SQL=0.217 过 0.2），无向传播会连坐真意见（v3.133.2
// 活体对照抓的过度抑制）。模糊行之间仍按 0.2 归族。
func familyRelated(anchor, cand FindingPrint) bool {
	switch {
	case anchor.Line > 0 && cand.Line > 0:
		return linesClose(anchor.Line, cand.Line) && commentNearLoose(anchor.Comment, cand.Comment)
	case anchor.Line == 0 && cand.Line > 0:
		return false // 模糊锚不外溢到精确行（防大杂烩桥接连坐）
	default: // 候选为 line=0 模糊形态
		return fileSetIntersect(anchor.File, cand.File) && commentNearVague(anchor.Comment, cand.Comment)
	}
}

// suppressFamily 抑制家族传播（v3.133.1）：人工拒绝一条 = 拒绝同一问题的全部
// 措辞变体——从 suppressed 行出发不动点扩张，家族行并入抑制。此前须逐变体打
// 地鼠（实测同一问题四轮四种措辞，压二缺一即复活）。
func suppressFamily(tx *gorm.DB, platform, owner, repo, number string, at time.Time) error {
	for round := 0; round < 8; round++ { // 家族规模小，不动点 8 轮封顶
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		grew := false
		for i := range rows {
			if rows[i].Status != PrintSuppressed {
				continue
			}
			for j := range rows {
				if rows[j].Status == PrintSuppressed {
					continue
				}
				if familyRelated(rows[i], rows[j]) {
					rows[j].Status = PrintSuppressed
					rows[j].LastSeenAt = at
					if err := tx.Save(&rows[j]).Error; err != nil {
						return err
					}
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
	return nil
}

// restoreFamily 恢复家族传播（suppressFamily 对称）：从 open 行出发把家族内的
// suppressed 行放回 open。
func restoreFamily(tx *gorm.DB, platform, owner, repo, number string) error {
	for round := 0; round < 8; round++ {
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		grew := false
		for i := range rows {
			if rows[i].Status != PrintOpen {
				continue
			}
			for j := range rows {
				if rows[j].Status != PrintSuppressed {
					continue
				}
				if familyRelated(rows[i], rows[j]) {
					rows[j].Status = PrintOpen
					if err := tx.Save(&rows[j]).Error; err != nil {
						return err
					}
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
	return nil
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
		nv := domain.Fingerprint(r.File, r.Comment, r.Line)
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
		nv := domain.Fingerprint(r.File, r.Comment, r.Line)
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

// migratePrintAliasPRScope print_aliases 表重建（v3.9.2，SchemaMigration 哨兵
// 门控）：PR 四列并入主键。旧形态 alias_fp 全局唯一主键与"指纹按 PR 归属"矛盾
// ——两个 PR 同款措辞漂移时第二个 PR 建别名撞主键、UpsertPrint 整体回滚。
// 回填规则：旧行按 print_fp 关联 finding_prints 反查 PR——命中多个 PR 逐 PR
// 复制（"alias_fp ↔ 该 PR 的规范行"在各 PR 内均成立），零命中（规范行已被
// 清理的孤儿别名）丢弃。新库 AutoMigrate 即新形态，重建空表无害（幂等）。
// 语句全部为内联字面量，无运行期插值——迁移 DDL 不含任何用户输入。
func (s *Store) migratePrintAliasPRScope(ctx context.Context) (int, error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.isSQLite() {
			if err := tx.Exec(`CREATE TABLE "print_aliases_new" ("platform" text NOT NULL, "owner" text NOT NULL, "repo" text NOT NULL, "number" text NOT NULL, "alias_fp" text NOT NULL, "print_fp" text, "updated_at" datetime, PRIMARY KEY ("platform", "owner", "repo", "number", "alias_fp"))`).Error; err != nil {
				return err
			}
		} else if s.isMySQL() {
			// MySQL 反引号表名与引号列名在 Go 源码里无需转义
			if err := tx.Exec("CREATE TABLE `print_aliases_new` (`platform` varchar(16) NOT NULL, `owner` varchar(128) NOT NULL, `repo` varchar(128) NOT NULL, `number` varchar(16) NOT NULL, `alias_fp` varchar(64) NOT NULL, `print_fp` varchar(64) DEFAULT NULL, `updated_at` datetime(3) DEFAULT NULL, PRIMARY KEY (`platform`, `owner`, `repo`, `number`, `alias_fp`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error; err != nil {
				return err
			}
		} else {
			// PostgreSQL（v5.4.8）：双引号标识符 + text/timestamp 形态
			if err := tx.Exec(`CREATE TABLE print_aliases_new ("platform" text NOT NULL, "owner" text NOT NULL, "repo" text NOT NULL, "number" text NOT NULL, "alias_fp" text NOT NULL, "print_fp" text, "updated_at" timestamptz, PRIMARY KEY ("platform", "owner", "repo", "number", "alias_fp"))`).Error; err != nil {
				return err
			}
		}
		// 回填按 print_fp 反查 PR：多 PR 命中经 JOIN 逐 PR 复制，同 (PR, alias_fp)
		// 天然去重；updated_at 取 MAX（旧行 alias_fp 全局唯一，无并列）。
		if err := tx.Exec(`INSERT INTO print_aliases_new (platform, owner, repo, number, alias_fp, print_fp, updated_at) SELECT p.platform, p.owner, p.repo, p.number, a.alias_fp, a.print_fp, MAX(a.updated_at) FROM print_aliases a JOIN finding_prints p ON p.print = a.print_fp GROUP BY p.platform, p.owner, p.repo, p.number, a.alias_fp, a.print_fp`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DROP TABLE print_aliases`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`ALTER TABLE print_aliases_new RENAME TO print_aliases`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE INDEX idx_print_aliases_print_fp ON print_aliases (print_fp)`).Error
	}); err != nil {
		return 0, err
	}
	var n int64
	if err := s.db.WithContext(ctx).Table("print_aliases").Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}
