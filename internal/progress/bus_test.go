package progress

import (
	"context"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"
)

// TestBusFanOut 多订阅者各收到一份事件。
func TestBusFanOut(t *testing.T) {
	b := oagentprogress.NewBus[Event]()
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
	b := oagentprogress.NewBus[Event]()
	ch, cancel := b.Subscribe(context.Background())
	defer cancel()
	_ = ch
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Publish(Event{Type: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish 被慢订阅者阻塞")
	}
}
