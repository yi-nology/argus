// service_eventfanout.go —— 事件 Outbox fanout 环（v6.9.0 P1-05，#10）：
// 每实例一个轮询协程，把 outbox 新行回灌本地 Bus——连接本实例的 SSE 客户端
// 即可收到他实例发布的事件（事件源实例已本地广播，回灌同 ID 事件去重跳过）。
// SSE handler 语义不变（Bus 订阅 + AuthorizeEvent 按仓过滤）。

package service

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/progress"
)

// outboxFanoutInterval 轮询间隔：跨实例事件延迟上界 ≈ interval + Bus 广播即时。
// 500ms 已低于 SSE keepalive（15s）粒度；LISTEN/NOTIFY 是延迟优化预留（不改语义）。
const outboxFanoutInterval = 500 * time.Millisecond

// outboxSeenCap 去重环容量：回灌协程读到的「本实例刚发过」事件 ID 集合上界。
// 双写窗口内的重复形态只有"本实例 500ms 内发过"——容量按事件峰值 100/s 量级
// 放 4096 足够；FIFO 淘汰。
const outboxSeenCap = 4096

// outboxRetention outbox 行保留期（观测面数据）：fanout 消化后仅审计价值。
const outboxRetention = 24 * time.Hour

// StartOutboxFanout 启动事件回灌环（bootstrap 装配；ctx 取消即退出，停机排水
// 无需显式等待——轮询协程无在途外部副作用）。nil Bus/Store 安全（测试栈）。
func (s *Service) StartOutboxFanout(ctx context.Context) {
	if s.Bus == nil || s.Store == nil {
		return
	}
	// 基线游标：从"现在"开始，不重播历史（重启不回放旧事件——SSE 是实时面）。
	lastID, err := s.Store.MaxOutboxID(ctx)
	if err != nil {
		s.log().Warn("argus.events.fanout_baseline_failed", "error", err.Error())
		lastID = 0
	}
	// v6.12 P1：去重环从局部提升为实例级共享组件（Service.EventSeen，bootstrap
	// 装配与 Runner 共享同一实例）——本地发布侧 publishProgress/publishOutbox
	// 登记 ID，回灌命中环即跳过，同实例 at-most-once。nil 时退回局部环（测试栈
	// 独立构造 Service 的场景，语义不变：fanout 单协程独占仍去重，只是本地发布
	// 侧不登记）。
	seen := s.EventSeen
	if seen == nil {
		seen = progress.NewDedup(outboxSeenCap)
	}
	go func() {
		prune := time.NewTicker(time.Hour)
		defer prune.Stop()
		t := time.NewTicker(outboxFanoutInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				if n, err := s.Store.PruneOutboxEvents(ctx, time.Now().Add(-outboxRetention)); err != nil {
					s.log().Warn("argus.events.outbox_prune_failed", "error", err.Error())
				} else if n > 0 {
					s.log().Info("argus.events.outbox_pruned", "removed", n)
				}
			case <-t.C:
				rows, err := s.Store.ListOutboxEvents(ctx, lastID, 200)
				if err != nil {
					s.log().Warn("argus.events.fanout_poll_failed", "error", err.Error())
					continue
				}
				for _, r := range rows {
					if r.ID > lastID {
						lastID = r.ID
					}
					if r.Event.ID == "" || !seen.Add(r.Event.ID) {
						continue // 本实例已广播过（v6.12：本地发布侧登记）或行缺 ID（坏行）
					}
					s.Bus.Publish(r.Event)
				}
			}
		}
	}()
	s.log().Info("argus.events.fanout_started", "baseline_id", lastID, "interval", outboxFanoutInterval.String())
}
