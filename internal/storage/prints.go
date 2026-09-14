package storage

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"git.example.com/enjoydream/agentkit/severity"

	"argus/internal/plugin"
)

// GetPrintState 实现 plugin.PrintStore（别名一跳解析：新措辞指纹查到既有条目）。
func (s *Store) GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*plugin.PrintState, error) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, resolveAliasFP(s.db.WithContext(ctx), fingerprint)).First(&p).Error
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
	}, nil
}

// UpsertPrint 实现 plugin.PrintStore：同一指纹跨轮次 = 同一问题 → 更新状态而非新增。
// 行号 ±2 容差内采纳新行号（匹配辅助，不入指纹）。
func (s *Store) UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f plugin.Finding, taskID string, at time.Time) (suppressed bool, err error) {

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return suppressed, s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fingerprint = resolveAliasFP(tx, fingerprint)
		var p FindingPrint
		err := tx.Where(
			"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
			platform, owner, repo, number, fingerprint).First(&p).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			// 近重复匹配层（v3.7.0）：精确未命中时，同 PR 同文件内行号相容且
			// 评语近重复的既有条目视为同一问题——并入它（计数/抑制状态延续），
			// 新措辞记为别名。措辞漂移不再产生"永远新问题"。
			near, nerr := findNearPrint(tx, platform, owner, repo, number, f)
			if nerr != nil {
				return nerr
			}
			if near != nil {
				p = *near
				if fingerprint != p.Print {
					if aerr := tx.Create(&PrintAlias{AliasFP: fingerprint, PrintFP: p.Print, UpdatedAt: at}).Error; aerr != nil {
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
		p.Severity = severity.Higher(p.Severity, f.Severity)
		if p.Plugin == "" {
			p.Plugin = firstBy(f.By)
		}
		if absDiff(p.Line, f.Line) <= 2 && f.Line > 0 {
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
		fingerprint = resolveAliasFP(tx, fingerprint)
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
	fp, pluginName, ok := s.ResolvePrintWithPlugin(ctx, platform, owner, repo, number, prefix)
	_ = pluginName
	return fp, ok
}

// ResolvePrintWithPlugin ResolvePrintFingerprint 的超集：附带插件名供学习环归属。
func (s *Store) ResolvePrintWithPlugin(ctx context.Context, platform, owner, repo, number, prefix string) (fp, pluginName string, ok bool) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
		platform, owner, repo, number, prefix+"%").
		Order("last_seen_at DESC").First(&p).Error
	if err == nil {
		return p.Print, p.Plugin, true
	}
	// 别名前缀兜底：报告意见行展示的可能是新措辞指纹（别名），短码解析需映射回规范指纹
	var a PrintAlias
	if err := s.db.WithContext(ctx).Where("alias_fp LIKE ?", prefix+"%").Order("updated_at DESC").First(&a).Error; err != nil {
		return "", "", false
	}
	return a.PrintFP, "", true
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
// 行不存在时创建（status=open, TimesSeen=0）。
func (s *Store) RecordPrintRegret(ctx context.Context, platform, owner, repo, number, fingerprint string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
				Print:       fingerprint,
				Comment:     "",
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
func (s *Store) RestorePrint(ctx context.Context, platform, owner, repo string, number int, prefix string) (bool, error) {
	if len(prefix) < 6 {
		return false, nil
	}
	matched := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
			platform, owner, repo, number, prefix+"%").Find(&rows).Error; err != nil {
			return err
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
