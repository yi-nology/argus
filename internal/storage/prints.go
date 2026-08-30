package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

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
		p.Severity = plugin.HigherSeverity(p.Severity, f.Severity)
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

// ListFindingPrints 观测辅助：某 PR 的全部指纹记录（GET 任务结果时可用）。
func (s *Store) ListFindingPrints(ctx context.Context, platform, owner, repo, number string) ([]FindingPrint, error) {
	var out []FindingPrint
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ?",
		platform, owner, repo, number).Order("last_seen_at DESC").Find(&out).Error
	return out, err
}
