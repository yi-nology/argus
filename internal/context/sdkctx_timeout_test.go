package context

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"
)

// v3.5.5 回归：弱网/VPN 下 SDK 连接停滞且不报错，poster 的总结评论更新曾无限挂起。
// bound() 必须给无 deadline 的调用兜底限时，超时错误向上传播。
func TestSDKCtxBoundTimeout(t *testing.T) {
	hang := &hangingProvider{block: make(chan struct{})}
	s := NewSDKCtxStubbed(logx.NewSlogLogger("sdkctx-test"),
		map[string]provider.Provider{"gitea": hang}).WithSDKCallTimeout(50 * time.Millisecond)

	start := time.Now()
	if _, err := s.GetCR(context.Background(), "gitea", "o", "r", "1"); err == nil {
		t.Fatal("挂死的 SDK 调用应返回错误")
	} else if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("应是 ctx 超时错误，got: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("兜底超时应≈50ms 内返回，实际 %v", el)
	}

	// 入参 ctx 已带 deadline：尊重调用方语义（外部 30ms 先于兜底 50ms 生效）
	hang.reset()
	ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	if _, err := s.GetCR(ctx2, "gitea", "o", "r", "1"); err == nil {
		t.Fatal("外部 deadline 到期应返回错误")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("应遵循外部 deadline≈30ms，实际 %v", el)
	}

	// 负值 = 显式关闭兜底（调用只随外部 ctx 语义）
	s.WithSDKCallTimeout(-1)
	hang.reset()
	ctx3, cancel3 := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel3()
	if _, err := s.GetCR(ctx3, "gitea", "o", "r", "1"); err == nil {
		t.Fatal("关闭兜底后仍应遵循外部 ctx 取消")
	}
}

// hangingProvider 只实现 GetCR：阻塞直至 ctx 结束（模拟停滞的 TCP 连接，
// 未显式实现的方法借嵌入接口 panic 兜底——本测试不应触达）。
type hangingProvider struct {
	provider.Provider
	provider.IssueManager
	provider.ReviewManager
	block chan struct{}
}

func (h *hangingProvider) reset() { h.block = make(chan struct{}) }

func (h *hangingProvider) GetCR(ctx context.Context, _, _, _ string) (*provider.ChangeRequest, error) {
	// 模拟真实 SDK：请求随 ctx 中止（bound 取消 / 外部 deadline 均能打断）
	select {
	case <-h.block:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
