// Package worker worker pool（design §10.4）：DB 即队列。
// webhook/TaskAPI 同步路径只落库（pending）即返回，不直接 spawn 审查 goroutine；
// N 个 worker 从 tasks 表拉 pending（原子抢占）执行。启动时把遗留 running 重置回 pending
// （在 main 装配时调用 store.ResetRunningToPending）。
//
// 两阶段优雅停机（v3.0.1 运维加固）：
//
//	Stop(grace) 先只停止"领取新任务"，给在途任务 grace 时间自然完成；
//	超时才取消执行上下文（任务保持 running，下次启动重置回 pending）——
//	避免部署重启把跑到 90% 的审查硬杀后重跑、双倍消耗 LLM。
package worker

import (
	"context"
	"sync"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/storage"
)

// 心跳节奏：running 任务每 20s 续一次心跳；心跳死亡超过 90s（≥3 个间隔的安全余量）
// 的任务视为实例已崩溃，可被任意存活实例重置回 pending（v3.0.2 多副本/崩溃恢复语义）。
const (
	HeartbeatInterval = 20 * time.Second
	StaleRunningAfter = 90 * time.Second
)

// Pool worker 池。
type Pool struct {
	Store *storage.Store
	// Run 单任务执行体（runner.Execute）。
	Run func(ctx context.Context, taskID string) error
	N   int
	Log logx.Logger

	loopCtx    context.Context // 领取循环生命周期：Stop 时立即取消
	runCtx     context.Context // 执行生命周期：grace 超时后才取消
	cancelLoop context.CancelFunc
	cancelRun  context.CancelFunc
	done       chan struct{}
	once       sync.Once
}

// Start 启动 N 个常驻 worker。ctx 只是进程退出的硬终止兜底；优雅停机请调用 Stop。
func (p *Pool) Start(ctx context.Context) {
	if p.N <= 0 {
		p.N = 1
	}
	p.loopCtx, p.cancelLoop = context.WithCancel(ctx)
	p.runCtx, p.cancelRun = context.WithCancel(ctx)
	p.done = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < p.N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p.loop(p.loopCtx, p.runCtx, id)
		}(i)
	}
	go func() {
		wg.Wait()
		close(p.done)
	}()
	// 心跳 + stale 重拾协程：独立于 loopCtx 的超时控制，Stop 阶段一即退出
	go p.heartbeatLoop(p.loopCtx)
	p.Log.Info("argus.worker.started", "workers", p.N)
}

// heartbeatLoop 周期续本实例 running 任务心跳，并重置心跳已死的 running 任务
// （本机上次硬取消残留 / 其他实例崩溃）。启动场景之外也能自愈，无需等下次重启。
func (p *Pool) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := p.Store.TouchRunningHeartbeats(hbCtx); err != nil {
				p.Log.Warn("argus.worker.heartbeat_failed", "error", err.Error())
			}
			if n, err := p.Store.ResetRunningToPending(hbCtx, StaleRunningAfter); err != nil {
				p.Log.Warn("argus.worker.stale_reset_failed", "error", err.Error())
			} else if n > 0 {
				p.Log.Warn("argus.worker.stale_running_reset", "count", n,
					"stale_after", StaleRunningAfter.String(),
					"hint", "心跳过期的 running 任务已重置回 pending")
			}
			cancel()
		}
	}
}

// Stop 两阶段停机：取消领取循环 → 等 grace → 硬取消执行。幂等。
func (p *Pool) Stop(grace time.Duration) {
	p.once.Do(func() {
		if p.cancelLoop == nil {
			return
		}
		p.cancelLoop() // 阶段一：不再领取新任务
		select {
		case <-p.done: // 在途任务已全部自然完成
		case <-time.After(grace):
			p.Log.Warn("argus.worker.stop_grace_exceeded",
				"grace", grace.String(), "action", "硬取消在途任务（保持 running，下次启动重置回 pending）")
			p.cancelRun() // 阶段二：硬取消
			select {
			case <-p.done:
			case <-time.After(10 * time.Second):
			}
		}
		p.Log.Info("argus.worker.stopped")
	})
}

func (p *Pool) loop(loopCtx, runCtx context.Context, id int) {
	for {
		select {
		case <-loopCtx.Done():
			return
		default:
		}
		task, ok, err := p.Store.ClaimNextPending(loopCtx)
		if err != nil {
			if loopCtx.Err() != nil {
				return
			}
			p.Log.Error("argus.worker.claim_failed", "worker", id, "error", err.Error())
			sleepCtx(loopCtx, time.Second)
			continue
		}
		if !ok {
			sleepCtx(loopCtx, 500*time.Millisecond) // 空转轮询
			continue
		}
		p.Log.Info("argus.worker.claimed", "worker", id, "task", task.ID,
			"kind", task.Kind, "pr", task.Owner+"/"+task.Repo+"#"+task.Number)
		if err := p.Run(runCtx, task.ID); err != nil {
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
