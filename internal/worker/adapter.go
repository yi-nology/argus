// Package worker 提供 agentkit/worker.TaskQueue 的 storage.Store 适配器。
package worker

import (
	"context"
	"time"

	"argus/internal/storage"
)

// StoreQueue 适配 *storage.Store 实现 agentkit/worker.TaskQueue 接口。
// ClaimNextPending 返回值从 *Task 适配为 taskID string。
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
