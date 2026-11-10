// comment_delivery.go —— 评论事件的持久化投递幂等（v3.8.0）。
//
// 此前 HandleComment 的 delivery 幂等是纯内存 map：多副本共库时各副本独立
// 判重（平台 at-least-once 重试命中另一副本即重复烧 LLM + 重复回帖）；单实例
// 重启后 1 小时窗口内的重投递也会重复回答。DB 主键幂等多副本天然一致。

package storage

import (
	"context"
	"time"
)

// CommentDelivery 已处理的评论投递（delivery ID 主键幂等）。
type CommentDelivery struct {
	DeliveryID string    `gorm:"column:delivery_id;primaryKey;size:128"`
	SeenAt     time.Time `gorm:"column:seen_at;index"`
}

func (CommentDelivery) TableName() string { return "comment_deliveries" }

// MarkCommentDelivery 记录投递已处理。返回 fresh=false 表示该投递此前已处理过
// （幂等命中，调用方直接跳过）。deliveryID 为空时跳过落库（header 缺失兜底），
// 恒返回 fresh=true（此时只剩进程内 30s 节流兜底）。
func (s *Store) MarkCommentDelivery(ctx context.Context, deliveryID string, at time.Time) (fresh bool, err error) {
	if deliveryID == "" {
		return true, nil
	}
	err = s.withWrite(func() error {
		derr := s.db.WithContext(ctx).Create(&CommentDelivery{DeliveryID: deliveryID, SeenAt: at}).Error
		if derr != nil {
			if isDuplicateKeyErr(derr) {
				return nil
			}
			return derr
		}
		fresh = true
		return nil
	})
	return fresh, err
}

// PruneCommentDeliveries 清理过期的投递记录（7 天前的重试已无幂等意义）。
// 分批删除（每批 500，与 PruneTasks 同理：单条大 DELETE 长事务占住 SQLite
// 单写连接，v3.9.1 补齐）。
func (s *Store) PruneCommentDeliveries(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&CommentDelivery{}).
				Where("seen_at < ?", cutoff).Limit(500).Pluck("delivery_id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("delivery_id IN ?", batch).Delete(&CommentDelivery{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}
