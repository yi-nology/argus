// usage_event.go —— 逐调用成本账本（#32）：每次 LLM 调用一行的不可重复计费
// 记录（invocation_id 唯一索引兜底幂等）与真实消费账（实销）的聚合口径。
// 预留/核销（执行前预留 + 调用后核销）见 budget_reserve.go。

package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// UsageEvent usage_events 表：一次 LLM 调用的实销记录（#32 验收①）。
// platform/owner/repo 三列冗余自 tasks（不 JOIN）：实销按仓聚合是预算闸与
// /usage/monthly 的热路径，JOIN 会在任务清理后丢历史消费（孤儿事件仍要计账）。
type UsageEvent struct {
	ID uint `gorm:"column:id;primaryKey"`

	TaskID    string `gorm:"column:task_id;size:40;not null;index:idx_ue_task"`
	AttemptID string `gorm:"column:attempt_id;size:32"`
	// InvocationID 幂等键（#32 验收②）：同一物理调用重复落账（回调重放/写库
	// 重试）撞唯一索引收敛为 no-op——「不可重复计费」的单源保证。
	InvocationID string `gorm:"column:invocation_id;size:80;not null;uniqueIndex:idx_ue_inv"`

	Platform string `gorm:"column:platform;size:16;not null;index:idx_ue_budget,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_ue_budget,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_ue_budget,priority:3"`

	Provider string `gorm:"column:provider;size:64"`
	Model    string `gorm:"column:model;size:128"`

	InputTokens   int     `gorm:"column:input_tokens"`
	OutputTokens  int     `gorm:"column:output_tokens"`
	CachedTokens  int     `gorm:"column:cached_tokens"`
	EstimatedCost float64 `gorm:"column:estimated_cost"`

	// Status 记录状态：settled=已核销实销（缺省，调用完成即入账）。
	Status     string    `gorm:"column:status;size:16;not null;default:settled;index:idx_ue_budget,priority:4"`
	OccurredAt time.Time `gorm:"column:occurred_at;not null;index:idx_ue_budget,priority:5"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (UsageEvent) TableName() string { return "usage_events" }

// usage_event 状态词表。
const (
	// UsageSettled 已核销实销（调用完成，token 已花）。
	UsageSettled = "settled"
)

// RecordUsageEvent 落一笔逐调用实销记录（幂等）：invocation_id 撞唯一索引
// = 同一物理调用的重复投递，收敛为成功 no-op（不重复计费、不上抛错误）。
// 同步落账（#32 缺口「崩溃前未落库调用丢失」的最小窗口）：调用点在 LLM
// 返回后立即写库，写失败只由调用方告警——账本绝不阻断审查主链路。
func (s *Store) RecordUsageEvent(ctx context.Context, ev *UsageEvent) error {
	if ev == nil {
		return errors.New("storage: usage_event 为 nil")
	}
	if ev.TaskID == "" || ev.InvocationID == "" {
		return errors.New("storage: usage_event 缺少 task_id/invocation_id")
	}
	if ev.Status == "" {
		ev.Status = UsageSettled
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	// quietDB：同键重放是预期幂等路径，gorm 默认 logger 会把唯一键冲突打红
	// ERROR（重试风暴下刷屏），与 MarkCommentDelivery 同款降噪。
	err := s.quietDB().WithContext(ctx).Create(ev).Error
	if isDuplicateKeyErr(err) {
		return nil
	}
	return err
}

// TaskUsageEvents 取任务的逐调用实销记录（观测/排障面：任务维度账单明细，
// 按发生时间升序）。runner 埋点测试与 /tasks/:id 调试面共用。
func (s *Store) TaskUsageEvents(ctx context.Context, taskID string) ([]UsageEvent, error) {
	var out []UsageEvent
	err := s.quietDB().WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("occurred_at ASC, id ASC").Find(&out).Error
	return out, err
}

// ReconcileUsageEvents 历史实销回填（#32 启动期对账，无哨兵 marker）：升级前
// 已终态、无逐调用事件的任务按 tokens_used 补 settled 事件——真实消费账口径
// 不因升级断档（滚动发布期间旧二进制终态的任务下次启动自愈）。NOT EXISTS
// 快速排除已有事件的任务（新口径在账，含终态差额兜底已补齐的）；补账事件
// invocation_id=backfill:<task_id> 唯一索引幂等，重跑零重复。OccurredAt 取
// 任务终态/创建时间——历史消费落在其原始月份，不灌进当前月预算窗口。
// 仅覆盖「完全无事件」的任务：有部分事件的任务其差额由终态差额兜底
// （backfillTaskUsageDelta，attempt 级）负责，任务级求差会把重试的前次消耗
// 误判为缺口（tokens_used 只覆盖末次执行）。
func (s *Store) ReconcileUsageEvents(ctx context.Context) (int, error) {
	var tasks []Task
	if err := s.quietDB().WithContext(ctx).
		Select("id", "platform", "owner", "repo", "attempt", "tokens_used", "created_at", "finished_at").
		Where("status IN ? AND tokens_used > 0 "+
			"AND NOT EXISTS (SELECT 1 FROM usage_events WHERE usage_events.task_id = tasks.id)",
			[]string{StatusDone, StatusFailed, StatusStale}).
		Find(&tasks).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, tk := range tasks {
		occurred := tk.CreatedAt
		if tk.FinishedAt != nil {
			occurred = *tk.FinishedAt
		}
		if err := s.RecordUsageEvent(ctx, &UsageEvent{
			TaskID: tk.ID, AttemptID: strconv.Itoa(tk.Attempt),
			InvocationID: "backfill:" + tk.ID,
			Platform:     tk.Platform, Owner: tk.Owner, Repo: tk.Repo,
			Provider: "backfill", InputTokens: tk.TokensUsed,
			Status: UsageSettled, OccurredAt: occurred,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// backfillTaskUsageDelta 终态实销差额兜底（#32）：埋点是 best-effort（单笔
// 写失败仅告警不阻断审查），终态时按「tokens_used − 本次执行（attempt 级）已
// 落事件」补 settled 事件——丢笔不丢账。attempt 级求差是关键：重试后
// tokens_used 只覆盖本次执行，跨 attempt 求差会把前次执行的已入账消耗误判为
// 缺口。补账事件 invocation_id=backfill:<task>:<attempt> 唯一索引幂等。
// 调用方在既有写门内执行（本方法不取 withWrite）；失败只由调用方告警。
func (s *Store) backfillTaskUsageDelta(ctx context.Context, taskID, platform, owner, repo string,
	attempt, tokensUsed int, now time.Time) error {
	var recorded int64
	if err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("task_id = ? AND attempt_id = ? AND status = ?",
			taskID, strconv.Itoa(attempt), UsageSettled).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&recorded).Error; err != nil {
		return err
	}
	delta := int64(tokensUsed) - recorded
	if delta <= 0 {
		return nil
	}
	return s.RecordUsageEvent(ctx, &UsageEvent{
		TaskID: taskID, AttemptID: strconv.Itoa(attempt),
		InvocationID: fmt.Sprintf("backfill:%s:%d", taskID, attempt),
		Platform:     platform, Owner: owner, Repo: repo,
		Provider: "backfill", InputTokens: int(delta),
		Status: UsageSettled, OccurredAt: now,
	})
}
