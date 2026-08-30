// Package worker worker pool（design §10.4）：DB 即队列。
// webhook/TaskAPI 同步路径只落库（pending）即返回，不直接 spawn 审查 goroutine；
// N 个 worker 从 tasks 表拉 pending（原子抢占）执行。启动时把遗留 running 重置回 pending
// （在 main 装配时调用 store.ResetRunningToPending）。
package worker

import (
	"context"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/storage"
)

// Pool worker 池。
type Pool struct {
	Store *storage.Store
	// Run 单任务执行体（runner.Execute）。
	Run func(ctx context.Context, taskID string) error
	N   int
	Log logx.Logger
}

// Start 启动 N 个常驻 worker；ctx 取消时全部退出。
func (p *Pool) Start(ctx context.Context) {
	if p.N <= 0 {
		p.N = 1
	}
	for i := 0; i < p.N; i++ {
		go p.loop(ctx, i)
	}
	p.Log.Info("argus.worker.started", "workers", p.N)
}

func (p *Pool) loop(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		task, ok, err := p.Store.ClaimNextPending(ctx)
		if err != nil {
			p.Log.Error("argus.worker.claim_failed", "worker", id, "error", err.Error())
			sleepCtx(ctx, time.Second)
			continue
		}
		if !ok {
			sleepCtx(ctx, 500*time.Millisecond) // 空转轮询
			continue
		}
		p.Log.Info("argus.worker.claimed", "worker", id, "task", task.ID,
			"kind", task.Kind, "pr", task.Owner+"/"+task.Repo+"#"+task.Number)
		if err := p.Run(ctx, task.ID); err != nil {
			// runner 正常情况下自行终结任务；此处兜底日志（防御性）
			p.Log.Error("argus.worker.run_error", "worker", id, "task", task.ID, "error", err.Error())
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
