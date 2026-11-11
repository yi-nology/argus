// Package worker 提供 oagentkit/queue/worker.TaskQueue 的 storage.Store 适配器。
package worker

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

// StoreQueue 适配 *storage.Store 实现 oagentkit/queue/worker.TaskQueue 接口。
// ClaimNextPending 返回值从 *Task 适配为 taskID string。
// 同时实现可选能力 worker.ScopedHeartbeater（类型断言探测，心跳按在飞任务集
// 续期而非盲续全部 running）。
type StoreQueue struct {
	Store *storage.Store
}

// ClaimNextPending 原子抢占下一个 pending 任务，返回 taskID。
func (q *StoreQueue) ClaimNextPending(ctx context.Context) (string, bool, error) {
	task, ok, err := q.Store.ClaimNextPending(ctx)
	if !ok || err != nil {
		return "", ok, err
	}
	return task.ID, true, nil
}

// TouchRunningHeartbeats 续期本实例 running 任务的心跳。
func (q *StoreQueue) TouchRunningHeartbeats(ctx context.Context) error {
	return q.Store.TouchRunningHeartbeats(ctx)
}

// ResetRunningToPending 重置心跳已死的 running 任务回 pending。
func (q *StoreQueue) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error) {
	return q.Store.ResetRunningToPending(ctx, staleAfter)
}

// TouchHeartbeatsFor 只续期 taskIDs 中属于本实例且在跑的任务心跳
// （oagentkit/queue worker.ScopedHeartbeater 实现）。Pool 按本进程在飞任务集
// 调用本方法替代盲续 TouchRunningHeartbeats——runTask 退出（成功/报错/panic）
// 的任务移出续期集后心跳冻结，staleAfter 后由 ResetRunningToPending 复位
// （oagentkit/queue#42：盲续会把这些行一直续活，复位永不命中）。
func (q *StoreQueue) TouchHeartbeatsFor(ctx context.Context, taskIDs []string) error {
	return q.Store.TouchHeartbeatsFor(ctx, taskIDs)
}
