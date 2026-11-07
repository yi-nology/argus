package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type flakyModel struct {
	errs  []error // 第 i 次 Generate/Stream 返回 errs[i]；耗尽后成功
	calls int
}

func (f *flakyModel) next() error {
	f.calls++
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	return nil
}

func (f *flakyModel) Generate(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (f *flakyModel) Stream(_ context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.Message](1)
	w.Send(schema.AssistantMessage("ok", nil), nil)
	w.Close()
	return r, nil
}

func (f *flakyModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return f, nil
}

func err429() error {
	return errors.New("failed to create chat completion: error, status code: 429, status: 429 Too Many Requests, message: per-model RPM 超限")
}

func newTestRetry(inner einomodel.BaseChatModel, attempts int) einomodel.BaseChatModel {
	return newRetryChatModel(inner, attempts, time.Millisecond, 4*time.Millisecond, "test", nil)
}

func TestRetryGenerateSurvivesRateLimit(t *testing.T) {
	f := &flakyModel{errs: []error{err429(), err429()}}
	out, err := newTestRetry(f, 4).Generate(context.Background(), nil)
	if err != nil || out.Content != "ok" {
		t.Fatalf("限流 2 连败后应第 3 次成功: %v %v", out, err)
	}
	if f.calls != 3 {
		t.Fatalf("应恰好 3 次物理调用, 实得 %d", f.calls)
	}
}

func TestRetryGiveUpAfterAttempts(t *testing.T) {
	f := &flakyModel{errs: []error{err429(), err429(), err429()}}
	_, err := newTestRetry(f, 3).Generate(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("3 次打满应透传原 429: %v", err)
	}
	if f.calls != 3 {
		t.Fatalf("attempts=3 应调用 3 次, 实得 %d", f.calls)
	}
}

func TestRetryNonRetryableShortCircuits(t *testing.T) {
	f := &flakyModel{errs: []error{errors.New("status code: 400, bad request")}}
	_, err := newTestRetry(f, 4).Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("400 应直接失败")
	}
	if f.calls != 1 {
		t.Fatalf("确定性失败不该重试, 实调 %d 次", f.calls)
	}
}

func TestRetryStreamSameLoop(t *testing.T) {
	f := &flakyModel{errs: []error{err429()}}
	st, err := newTestRetry(f, 4).Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream 重试应成功: %v", err)
	}
	msg, err := st.Recv()
	if err != nil || msg.Content != "ok" {
		t.Fatalf("流内容异常: %v %v", msg, err)
	}
	if f.calls != 2 {
		t.Fatalf("应 2 次调用, 实得 %d", f.calls)
	}
}

func TestRetryCtxCancelStopsWait(t *testing.T) {
	f := &flakyModel{errs: []error{err429(), err429()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 首个 429 后退避等待立即被取消
	_, err := newTestRetry(f, 4).Generate(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应即停: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("取消后不应再发, 实调 %d", f.calls)
	}
}

func TestRetryWithToolsPassthrough(t *testing.T) {
	f := &flakyModel{}
	m := newTestRetry(f, 3)
	bound, err := m.(einomodel.ToolCallingChatModel).WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools 应透传: %v", err)
	}
	if _, ok := bound.(*retryChatModel); !ok {
		t.Fatalf("绑定产物应仍为重试包装, 实得 %T", bound)
	}
}

func TestRetryPassthroughWhenAttemptsLE1(t *testing.T) {
	f := &flakyModel{}
	if got := newRetryChatModel(f, 1, time.Millisecond, time.Millisecond, "s", nil); got != einomodel.BaseChatModel(f) {
		t.Fatal("attempts<=1 应直通不包")
	}
}
