package storage

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// LatestDoneTask 某 PR 最近一次成功审查的任务（ask_argus 数据源）。
// 不存在返回 nil, nil。
func (s *Store) LatestDoneTask(ctx context.Context, platform, owner, repo, number string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ?",
			platform, owner, repo, number, StatusDone).
		Order("finished_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}
