package poller

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestStartLeaderGateSkipsNonLeader 多副本门控：非 leader 的 tick 直接跳过。
// SDK/Store 为 nil——若门控失效会 nil 指针 panic，存活即证明门控生效。
func TestStartLeaderGateSkipsNonLeader(t *testing.T) {
	p := &Poller{
		IsLeader: func() bool { return false },
		Interval: 10 * time.Millisecond,
		Log:      logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
}

// TestStartLeaderGateLeaderPolls leader 语义不受影响（IsLeader=nil = 单实例恒真）。
func TestStartLeaderGateLeaderPolls(t *testing.T) {
	p := &Poller{
		IsLeader: func() bool { return true },
		Interval: 10 * time.Millisecond,
		Log:      logx.NewSlogLogger("test"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
}
