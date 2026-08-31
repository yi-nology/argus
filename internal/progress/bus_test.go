package progress

import (
	"context"
	"testing"
	"time"
)

// TestBusFanOut 多订阅者各收到一份事件。
func TestBusFanOut(t *testing.T) {
	b := NewBus()
	ctx := context.Background()
	ch1, c1 := b.Subscribe(ctx)
	ch2, c2 := b.Subscribe(ctx)
	defer c1()
	defer c2()
	b.Publish(Event{Type: "task.created", TaskID: "t1"})
	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != "task.created" || ev.TaskID != "t1" {
				t.Fatalf("event 不符: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatal("订阅者未收到事件")
		}
	}
}

// TestBusDropOnSlow 订阅者积压时发布不阻塞、事件有损丢弃。
func TestBusDropOnSlow(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe(context.Background())
	defer cancel()
	_ = ch // 有损丢弃语义：只断言发布端不阻塞
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Type: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
		// 发布端全程非阻塞即通过；ch 里收到几条不重要
	case <-time.After(2 * time.Second):
		t.Fatal("Publish 被慢订阅者阻塞")
	}
}

// TestSubscribeCtxCleanup ctx 取消后自动注销。
func TestSubscribeCtxCleanup(t *testing.T) {
	b := NewBus()
	ctx, c := context.WithCancel(context.Background())
	_, _ = b.Subscribe(ctx)
	c()
	time.Sleep(10 * time.Millisecond)
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("ctx 取消后应注销，剩余 %d", n)
	}
}
