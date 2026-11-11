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
// v6.9.0 P2-03（#14）扩为副作用账本：处理事实（delivery 行存在）、回帖终态
// （ReplyStatus）、外部副作用（平台评论 ID）三列分离——「回帖成功但处理失败/
// 反之」的不一致态可对账（CountPostedButFailedTasks 互补：审查任务侧的
// 回帖事实 = tasks.summary_comment_id）。
type CommentDelivery struct {
	DeliveryID string    `gorm:"column:delivery_id;primaryKey;size:128"`
	SeenAt     time.Time `gorm:"column:seen_at;index"`
	// ReplyStatus 回帖终态：""（处理中/未回帖）/ ok / error / duplicate。
	ReplyStatus string `gorm:"column:reply_status;size:16"`
	// CommentID 平台侧回帖评论 ID（外部副作用；0=未回帖）。URL 由平台按 ID 构造，
	// 不冗余存列。
	CommentID int64  `gorm:"column:comment_id"`
	Ref       string `gorm:"column:ref;size:160"` // prRef（owner/repo#number，对账定位）
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

// MarkCommentDeliveryOutcome 回填投递终态与外部副作用（v6.9.0 P2-03，#14）：
// 回帖出口（postCommentReply/postQAError）成功/失败后调用；deliveryID 为空
// 跳过。best-effort：对账账本写失败不阻断回帖主流程（告警面由调用方日志承接）。
func (s *Store) MarkCommentDeliveryOutcome(ctx context.Context, deliveryID, status string, commentID int64, ref string) error {
	if deliveryID == "" {
		return nil
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&CommentDelivery{}).
			Where("delivery_id = ?", deliveryID).
			Updates(map[string]any{"reply_status": status, "comment_id": commentID, "ref": ref}).Error
	})
}

// OutOfSyncDeliveries 副作用对账（v6.9.0 P2-03，#14）：①回帖成功但任务终态
// failed（任务 failed 且 summary_comment_id>0，窗口内）——审查链路「回帖成功
// 但任务落库失败」形态；②回帖失败但任务 done——任务成功而确认回帖缺失。
// 计数进指标（bootstrap 运维 ticker 消费），明细留给人工按窗口查询。
func (s *Store) OutOfSyncDeliveries(ctx context.Context, since time.Time) (postedButFailed, doneWithoutReply int64, err error) {
	if e := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ? AND summary_comment_id > 0 AND finished_at >= ?", StatusFailed, since).
		Count(&postedButFailed).Error; e != nil {
		return 0, 0, e
	}
	if e := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ? AND (summary_comment_id IS NULL OR summary_comment_id = 0) AND finished_at >= ? AND kind IN ?",
			StatusDone, since, []string{"requirement_compliance", "code_review", "visual_check"}).
		Count(&doneWithoutReply).Error; e != nil {
		return 0, 0, e
	}
	return postedButFailed, doneWithoutReply, nil
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
