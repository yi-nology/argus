package storage

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"git.example.com/enjoydream/agentkit/reportutil"

	"argus/internal/plugin"
)

// GetPrintState 实现 plugin.PrintStore（别名一跳解析：新措辞指纹查到既有条目）。
func (s *Store) GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*plugin.PrintState, error) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, resolveAliasFP(s.db.WithContext(ctx), platform, owner, repo, number, fingerprint)).First(&p).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &plugin.PrintState{
		Status:      p.Status,
		Line:        p.Line,
		Severity:    p.Severity,
		TimesSeen:   p.TimesSeen,
		RegretCount: p.RegretCount,
		Comment:     p.Comment, // 入库原文（标注前）——pattern regret 记账键的权威来源
	}, nil
}

// UpsertPrint 实现 plugin.PrintStore：同一指纹跨轮次 = 同一问题 → 更新状态而非新增。
// 行号 ±2 容差内采纳新行号（匹配辅助，不入指纹）。
func (s *Store) UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f plugin.Finding, taskID string, at time.Time) (suppressed bool, err error) {

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return suppressed, s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
		var p FindingPrint
		exact := false // 本轮是否经"完全同评语"通道并入（行号跟随用）
		pQ := tx.Where(
			"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
			platform, owner, repo, number, fingerprint)
		// MySQL 下 FOR UPDATE：多副本共库时防止并发轮次的"读-改-全列回写"
		// 互相覆盖（抑制状态/TimesSeen 丢失）；SQLite 单写串行无需锁（v3.8.0）。
		if !s.sqlite {
			pQ = pQ.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := pQ.First(&p).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			// 近重复匹配层（v3.7.0）：精确未命中时，同 PR 同文件内行号相容且
			// 评语近重复的既有条目视为同一问题——并入它（计数/抑制状态延续），
			// 新措辞记为别名。措辞漂移不再产生"永远新问题"。
			// v3.8.0：完全同评语免行距（exact 通道）——行号整体位移 >±2 时
			// 计数/抑制不失忆；并入后行号跟随新值，行内评论位置不漂移。
			near, xexact, nerr := findNearPrint(tx, platform, owner, repo, number, f)
			if nerr != nil {
				return nerr
			}
			if near != nil {
				p = *near
				exact = xexact
				if fingerprint != p.Print {
					// 幂等写入：别名 PR 域化（v3.9.2）后同 (PR, alias_fp) 重复创建
					// 仍可能发生（同 PR 内措辞漂移回摆），冲突保留既有映射。
					if aerr := tx.Clauses(clause.OnConflict{DoNothing: true}).
						Create(&PrintAlias{Platform: platform, Owner: owner, Repo: repo, Number: number,
							AliasFP: fingerprint, PrintFP: p.Print, UpdatedAt: at}).Error; aerr != nil {
						return aerr
					}
				}
			} else {
				// 全新指纹
				p = FindingPrint{
					Platform: platform, Owner: owner, Repo: repo, Number: number, Print: fingerprint,
					File: f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment, Plugin: firstBy(f.By),
					Status:      PrintOpen,
					FirstTaskID: taskID, LastTaskID: taskID, TimesSeen: 1,
					FirstSeenAt: at, LastSeenAt: at,
				}
				return tx.Create(&p).Error
			}
		}
		// 既有指纹：更新可见性元数据；保留 suppressed 状态（误报反馈不被新轮次冲掉）
		p.LastTaskID = taskID
		p.LastSeenAt = at
		p.TimesSeen++
		p.Severity = reportutil.Higher(p.Severity, f.Severity)
		if p.Plugin == "" {
			p.Plugin = firstBy(f.By)
		}
		// 行号跟随：exact（完全同评语，含行号漂移）无条件跟随新值；
		// near（近重复）保持 ±2 相容才跟随的保守语义。
		if f.Line > 0 && (exact || absDiff(p.Line, f.Line) <= 2) {
			p.Line = f.Line
		}
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		suppressed = p.Status == PrintSuppressed
		return nil
	})
}

func absDiff(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d
}

var _ plugin.PrintStore = (*Store)(nil)

// SuppressPrint 误报反馈（§3.3 反馈通道最小入口，v3.0.1）：
// 把指纹置为 suppressed（既有 → 更新并返回 matched=true；
// 不存在 → 以 suppressed 预插（意见可能尚未在库），matched=false）。
// 之后每轮 Merger 自动剔除该意见。
func (s *Store) SuppressPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f plugin.Finding, at time.Time) (matched bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
		var p FindingPrint
		err := tx.Where(
			"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
			platform, owner, repo, number, fingerprint).First(&p).Error
		if err == nil {
			matched = true
			p.Status = PrintSuppressed
			p.LastSeenAt = at
			if f.File != "" {
				p.File, p.Line, p.Severity, p.Comment = f.File, f.Line, f.Severity, f.Comment
			}
			return tx.Save(&p).Error
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		p = FindingPrint{
			Platform: platform, Owner: owner, Repo: repo, Number: number, Print: fingerprint,
			File: f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment,
			Status:      PrintSuppressed,
			FirstTaskID: "manual", LastTaskID: "manual", TimesSeen: 0,
			FirstSeenAt: at, LastSeenAt: at,
		}
		return tx.Create(&p).Error
	})
	return matched, err
}

// ResolvePrintFingerprint 按短码前缀解析完整指纹（取最近更新的一条）。
// found=false 表示该前缀在此 PR 下无匹配。同时返回产出插件名（可为空）。
func (s *Store) ResolvePrintFingerprint(ctx context.Context, platform, owner, repo, number, prefix string) (string, bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", false
	}
	return p.Print, true
}

// ResolvePrintWithPlugin ResolvePrintFingerprint 的超集：附带插件名供学习环归属。
func (s *Store) ResolvePrintWithPlugin(ctx context.Context, platform, owner, repo, number, prefix string) (fp, pluginName string, ok bool) {
	p, ok := s.ResolvePrintRecord(ctx, platform, owner, repo, number, prefix)
	if !ok {
		return "", "", false
	}
	return p.Print, p.Plugin, true
}

// validFPPrefix 指纹短码前缀校验：4~64 位十六进制（指纹为 sha256 hex 串）。
// 前缀会直接拼进 LIKE——含 %/_ 的输入会变成通配模式越权匹配（v3.9.1 收口）。
func validFPPrefix(p string) bool {
	if len(p) < 4 || len(p) > 64 {
		return false
	}
	for _, c := range p {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// ResolvePrintRecord 按短码前缀解析完整 prints 行（v3.8.0）：
// 人工反馈（suppress/restore）派生 pattern 键与回写必须用行内原始
// file/line/severity/comment——此前调用方拿"请求里的评语"（评论路径下是
// 整条评论原文）记账，PatternKey 剥离 hex/指令词后全部塌缩到垃圾键，学习环
// 账本与真实模式永远对不上。别名前缀命中时二跳回规范行。
// v3.9.1：①前缀 hex 白名单（防 LIKE 通配注入）；②本 PR 内多行命中同一前缀
// 时拒绝解析（与 restore 同款"不猜"——错误抑制比不抑制难回滚）；③别名兜底
// 限定到本 PR 的规范行（别名表全局共享，此前取全局最近更新可能落到别的 PR）。
func (s *Store) ResolvePrintRecord(ctx context.Context, platform, owner, repo, number, prefix string) (FindingPrint, bool) {
	if !validFPPrefix(prefix) {
		return FindingPrint{}, false
	}
	var rows []FindingPrint
	if err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
			platform, owner, repo, number, prefix+"%").
		Order("last_seen_at DESC").Find(&rows).Error; err != nil {
		return FindingPrint{}, false
	}
	if len(rows) == 1 {
		return rows[0], true
	}
	if len(rows) > 1 {
		return FindingPrint{}, false // 前缀歧义：不猜
	}
	// 别名前缀兜底：报告意见行展示的可能是新措辞指纹（别名），短码解析需映射
	// 回规范行。别名表已 PR 域化（v3.9.2），直接按本 PR 过滤。
	var a PrintAlias
	if err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp LIKE ?",
		platform, owner, repo, number, prefix+"%").
		Order("updated_at DESC").First(&a).Error; err != nil {
		return FindingPrint{}, false
	}
	var p FindingPrint
	if err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, a.PrintFP).First(&p).Error; err != nil {
		return FindingPrint{}, false
	}
	return p, true
}

func firstBy(by string) string {
	if i := strings.IndexByte(by, ','); i > 0 {
		return by[:i]
	}
	return by
}

// ListFindingPrints 观测辅助：某 PR 的全部指纹记录（GET 任务结果时可用）。
func (s *Store) ListFindingPrints(ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	var out []FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ?",
		platform, owner, repo, number).Order("last_seen_at DESC").Find(&out).Error
	return out, err
}

// RecordPrintRegret R3.5 反思剔除某指纹时累加 regret 计数：该指纹历史上被
// 终审 rubric 判为误报的次数，跨轮由 Merger 读取并标注到报告（只标注，不自动
// suppress——持久抑制仍必须来自人工反馈通道，防自动化误杀）。
// 行不存在时以 f 的原始内容创建（v3.8.0：此前创建 File=""/Comment="" 的幽灵行，
// 近重复层永不回收、v2 迁移还会因同 PR 多条空行重算出同一常量指纹而撞唯一索引）。
// 指纹先经别名解析：剔除的可能以别名身份在库。
func (s *Store) RecordPrintRegret(ctx context.Context, platform, owner, repo, number, fingerprint string, f plugin.Finding) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
		var p FindingPrint
		err := tx.Where(
			"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
			platform, owner, repo, number, fingerprint).First(&p).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			now := time.Now()
			return tx.Create(&FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number,
				Print: fingerprint,
				File:  f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment,
				Plugin:      firstBy(f.By),
				Status:      PrintOpen,
				RegretCount: 1,
				FirstTaskID: "reflect", LastTaskID: "reflect", TimesSeen: 0,
				FirstSeenAt: now, LastSeenAt: now,
			}).Error
		}
		return tx.Model(&p).Updates(map[string]any{
			"regret_count": gorm.Expr("regret_count + 1"),
			"updated_at":   time.Now(),
		}).Error
	})
}

// RestorePrint 恢复被误抑制的意见（suppress 反向操作）：fp 短码前缀解析后置回 open，
// 下一轮审查起重新可见。只作用于 suppressed 行——已 open 的同前缀行视为已恢复
// （matched=true 不改状态）；无匹配或前缀歧义 matched=false，由调用方回帖提示。
// restore 不回流知识：恢复只是撤销剔除，不是新的误报/正确结论。
// 别名回退（v3.8.0）：suppress 可经别名表进来（措辞漂移后报告展示的是别名短码），
// restore 同样必须走别名兜底——否则抑制是"进得来、出不去"的单向门。
func (s *Store) RestorePrint(ctx context.Context, platform, owner, repo string, number int, prefix string) (bool, error) {
	if len(prefix) < 6 || !validFPPrefix(prefix) {
		return false, nil
	}
	matched := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
			platform, owner, repo, number, prefix+"%").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			// 别名前缀兜底：alias_fp 为全指纹，前缀命中歧义时同样不猜。
			// 别名表已 PR 域化（v3.9.2），直接按本 PR 过滤。
			var aliases []PrintAlias
			if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp LIKE ?",
				platform, owner, repo, number, prefix+"%").Find(&aliases).Error; err != nil {
				return err
			}
			if len(aliases) != 1 {
				return nil // 无匹配或歧义：不猜
			}
			if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
				platform, owner, repo, number, aliases[0].PrintFP).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 1 {
				return nil
			}
		}
		if len(rows) != 1 {
			return nil // 无匹配或前缀歧义：不猜
		}
		matched = true
		if rows[0].Status == PrintSuppressed {
			rows[0].Status = PrintOpen
			return tx.Save(&rows[0]).Error
		}
		return nil
	})
	return matched, err
}

// ListAllPrints 全量意见指纹（G1c 训练数据导出用；只读，无 PR 过滤）。
func (s *Store) ListAllPrints(ctx context.Context) ([]FindingPrint, error) {
	var rows []FindingPrint
	err := s.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}
