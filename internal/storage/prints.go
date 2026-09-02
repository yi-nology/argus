package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"git.example.com/enjoydream/agentkit/severity"

	"argus/internal/plugin"
)

// GetPrintState 实现 plugin.PrintStore。
func (s *Store) GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*plugin.PrintState, error) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, fingerprint).First(&p).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &plugin.PrintState{
		Status:    p.Status,
		Line:      p.Line,
		Severity:  p.Severity,
		TimesSeen: p.TimesSeen,
	}, nil
}

// UpsertPrint 实现 plugin.PrintStore：同一指纹跨轮次 = 同一问题 → 更新状态而非新增。
// 行号 ±2 容差内采纳新行号（匹配辅助，不入指纹）。
func (s *Store) UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f plugin.Finding, taskID string, at time.Time) (suppressed bool, err error) {

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return suppressed, s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p FindingPrint
		err := tx.Where(
			"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
			platform, owner, repo, number, fingerprint).First(&p).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			// 新指纹
			p = FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number, Print: fingerprint,
				File: f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment,
				Status:      PrintOpen,
				FirstTaskID: taskID, LastTaskID: taskID, TimesSeen: 1,
				FirstSeenAt: at, LastSeenAt: at,
			}
			return tx.Create(&p).Error
		}
		// 既有指纹：更新可见性元数据；保留 suppressed 状态（误报反馈不被新轮次冲掉）
		p.LastTaskID = taskID
		p.LastSeenAt = at
		p.TimesSeen++
		p.Severity = severity.Higher(p.Severity, f.Severity)
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
// found=false 表示该前缀在此 PR 下无匹配。
func (s *Store) ResolvePrintFingerprint(ctx context.Context, platform, owner, repo, number, prefix string) (string, bool) {
	var p FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
		platform, owner, repo, number, prefix+"%").
		Order("last_seen_at DESC").First(&p).Error
	if err != nil {
		return "", false
	}
	return p.Print, true
}

// ListFindingPrints 观测辅助：某 PR 的全部指纹记录（GET 任务结果时可用）。
func (s *Store) ListFindingPrints(ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	var out []FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ?",
		platform, owner, repo, number).Order("last_seen_at DESC").Find(&out).Error
	return out, err
}
