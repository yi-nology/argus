// event_outbox.go —— 事件 Outbox（v6.9.0 P1-05，#10）：进度事件的跨实例可靠
// 分发事实源。SSE 事件总线此前纯进程内（Bus 广播），多副本下连接 Pod A 的
// 客户端收不到 Pod B 上的任务事件。Outbox 模式：发布点在进程内广播的同时落
// outbox 表（best-effort 追加写，见下注），每实例一个 fanout 环轮询新行回灌
// 本地 Bus——SSE handler 不变（Bus 订阅 + AuthorizeEvent 按仓过滤），跨实例
// 事件经 outbox → fanout → 本地 Bus 送达。
//
// 语义边界：outbox 追加写是发布点事务外的独立小写（progress 事件是观测面
// 数据，at-least-once + 极小丢失窗可接受；任务一致性不依赖事件）。不引入
// 消息中间件（审计明确不建议）——PostgreSQL 轮询已满足跨副本送达；LISTEN/
// NOTIFY 是延迟优化预留（500ms 轮询已低于 SSE keepalive 粒度）。

package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yi-nology/argus/internal/progress"
)

// OutboxEvent 事件 outbox 行：单调 ID 分页游标 + 事件载荷。
type OutboxEvent struct {
	ID   int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name string `gorm:"column:event_id;size:64;index"` // progress.Event.ID（去重键）
	Body string `gorm:"column:body;type:text"`         // Event JSON
	// Platform/Owner/Repo 冗余列：按仓过滤的行级查询入口（审计对账/运维排查
	// 不需解 JSON）；Type 事件类型。
	Platform  string    `gorm:"column:platform;size:16"`
	Owner     string    `gorm:"column:owner;size:128"`
	Repo      string    `gorm:"column:repo;size:128"`
	Type      string    `gorm:"column:type;size:32"`
	CreatedAt time.Time `gorm:"column:created_at;index"`
}

func (OutboxEvent) TableName() string { return "event_outbox" }

// AppendOutboxEvent 落一条事件（best-effort：失败由调用方告警，不阻断主流程）。
func (s *Store) AppendOutboxEvent(ctx context.Context, ev progress.Event) error {
	row := OutboxEvent{
		Name: ev.ID, Platform: ev.Platform, Owner: ev.Owner, Repo: ev.Repo,
		Type: ev.Type, CreatedAt: time.Now(),
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	row.Body = string(b)
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Create(&row).Error
	})
}

// OutboxRow fanout 轮询行：游标 ID + 事件载荷。
type OutboxRow struct {
	ID    int64
	Event progress.Event
}

// ListOutboxEvents 游标分页拉取（id > afterID，按 id 升序，limit 收口）。
// 返回行数 < limit 表示本批拉完。
func (s *Store) ListOutboxEvents(ctx context.Context, afterID int64, limit int) ([]OutboxRow, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []OutboxEvent
	err := s.quietDB().WithContext(ctx).
		Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]OutboxRow, 0, len(rows))
	for _, r := range rows {
		var ev progress.Event
		if err := jsonUnmarshal([]byte(r.Body), &ev); err != nil {
			continue // 坏行跳过（fanout 不断流）
		}
		out = append(out, OutboxRow{ID: r.ID, Event: ev})
	}
	return out, nil
}

// MaxOutboxID 当前最大游标（fanout 启动基线——从"现在"开始，不重播历史）。
func (s *Store) MaxOutboxID(ctx context.Context) (int64, error) {
	var maxID *int64
	err := s.quietDB().WithContext(ctx).Model(&OutboxEvent{}).Select("MAX(id)").Scan(&maxID).Error
	if err != nil || maxID == nil {
		return 0, err
	}
	return *maxID, nil
}

// PruneOutboxEvents 保留期清理（观测面数据，24h 足够跨实例 fanout 消化）。
func (s *Store) PruneOutboxEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&OutboxEvent{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}
