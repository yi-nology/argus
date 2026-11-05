// 租约存储（多副本选主）：实现 oagentkit/queue/worker.LeaseStore，供 worker.LeaderElector
// 收敛"只能跑一份"的控制面组件（出站轮询、定时清理）。SQL 条件更新保证原子性——
// `WHERE lease_key=? AND (holder=? OR expires_at<?)` 单语句完成"判-占-续"。
// 时间统一 UTC：跨实例时钟偏移（同 LAN 毫秒级）远小于 ttl 量级，可容忍。
package storage

import (
	"context"
	"time"

	agentkitworker "github.com/oagentkit/queue/worker"
)

// LeaderLease leader_leases 表：一行一个租约 key。列名 lease_key（避开 MySQL
// 保留字 key）。
type LeaderLease struct {
	LeaseKey  string    `gorm:"column:lease_key;primaryKey;size:128"`
	Holder    string    `gorm:"column:holder;size:128"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (LeaderLease) TableName() string { return "leader_leases" }

var _ agentkitworker.LeaseStore = (*Store)(nil)

// TryAcquire 原子获取或续约租约（oagentkit/queue/worker.LeaseStore 实现）。
// 两条路径：① 他人持有未过期 → 条件 UPDATE 仅在 holder=我 或已过期时生效；
// ② 无行 → 首次 INSERT（唯一键冲突 = 他实例抢先，回退再试一次条件更新收敛）。
func (s *Store) TryAcquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	res := s.db.WithContext(ctx).Exec("UPDATE leader_leases SET holder = ?, expires_at = ?, updated_at = ? WHERE lease_key = ? AND (holder = ? OR expires_at < ?)", holder, exp, now, key, holder, now)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		return true, nil
	}
	// INSERT 走 quietDB：行被他实例持有时 INSERT 恒撞唯一键——gorm 默认 logger
	// 会把这条预期内的冲突打红 ERROR（多副本下每次租约操作刷一条，噪声淹没真
	// 故障）。冲突语义由下方回退条件 UPDATE 收敛，v5.4.9 实弹降噪。
	err := s.quietDB().WithContext(ctx).Exec("INSERT INTO leader_leases (lease_key, holder, expires_at, updated_at) VALUES (?, ?, ?, ?)", key, holder, exp, now).Error
	if err == nil {
		return true, nil
	}
	res = s.db.WithContext(ctx).Exec("UPDATE leader_leases SET holder = ?, expires_at = ?, updated_at = ? WHERE lease_key = ? AND (holder = ? OR expires_at < ?)", holder, exp, now, key, holder, now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Release 释放租约（仅持有者本人生效；oagentkit/queue/worker.LeaseStore 实现）。
func (s *Store) Release(ctx context.Context, key, holder string) error {
	return s.db.WithContext(ctx).Exec("DELETE FROM leader_leases WHERE lease_key = ? AND holder = ?", key, holder).Error
}
