// Package progress L1 扩展：任务进度事件总线 + SSE 数据源（v3.1）。
// 发布方：service（task.created）、runner（task.running / task.finished）；
// 消费方：GET /events SSE 流。发布永不阻塞（订阅者慢就丢弃事件）。
package progress

import (
	"context"
	"sync"
	"time"
)

// Event 词表：Type ∈ task.created | task.running | task.finished。
type Event struct {
	Type     string `json:"type"`
	TaskID   string `json:"task_id"`
	Platform string `json:"platform,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Number   string `json:"number,omitempty"`
	Status   string `json:"status,omitempty"` // task.finished: done/failed/stale
	Verdict  string `json:"verdict,omitempty"`
	At       string `json:"at"`
}

// Bus 多订阅者广播。每个订阅者 64 缓冲，满了就丢（观测数据允许有损）。
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: map[chan Event]struct{}{}}
}

// Publish 非阻塞广播。
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	e.At = time.Now().Format(time.RFC3339)
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // 订阅者积压：丢弃，不拖慢审查主链路
		}
	}
}

// Subscribe 注册订阅者；返回取消函数（ctx 结束也必须调用，防泄漏）。
func (b *Bus) Subscribe(ctx context.Context) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, cancel
}
