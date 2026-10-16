package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/logx"

	"argus/internal/poller"
)

// v3.0.1 试点改造回归：轮询兜底任务源——发现 PR、同 SHA 幂等、执行后 last_head_sha 拦截。
func TestScenario12_PollerDedup(t *testing.T) {
	s := newStack(t, stackOpts{workers: 1})
	pl := &poller.Poller{
		SDK: s.sdk, Store: s.store,
		Targets:  []poller.Target{{Platform: "gitea", Owner: "acme", Repo: "app"}},
		Interval: 80 * time.Millisecond, Log: testNopLogger{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pl.Start(ctx)

	// 等第一个任务被轮询发现并执行完
	deadline := time.Now().Add(20 * time.Second)
	for {
		if s.anyTaskDoneByHead("sha-1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("轮询任务未完成")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // 再过几个轮询周期

	// 幂等：同一 head 只应有 1 个任务（in-flight 拦截 + last_head_sha 拦截）
	if n := s.countTasksByHead("sha-1"); n != 1 {
		t.Fatalf("同 head 应只建 1 个任务，实际 %d", n)
	}
}

// testNopLogger logx.Logger 全接口空实现（与 plugin 包测试同款）。
type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any)  {}
func (testNopLogger) Info(string, ...any)   {}
func (testNopLogger) Warn(string, ...any)   {}
func (testNopLogger) Error(string, ...any)  {}
func (testNopLogger) Debugf(string, ...any) {}
func (testNopLogger) Infof(string, ...any)  {}
func (testNopLogger) Warnf(string, ...any)  {}
func (testNopLogger) Errorf(string, ...any) {}

func (testNopLogger) CtxDebug(context.Context, string, ...any)  {}
func (testNopLogger) CtxInfo(context.Context, string, ...any)   {}
func (testNopLogger) CtxWarn(context.Context, string, ...any)   {}
func (testNopLogger) CtxError(context.Context, string, ...any)  {}
func (testNopLogger) CtxDebugf(context.Context, string, ...any) {}
func (testNopLogger) CtxInfof(context.Context, string, ...any)  {}
func (testNopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (testNopLogger) CtxErrorf(context.Context, string, ...any) {}

func (testNopLogger) WithCtx(context.Context) logx.Logger { return testNopLogger{} }
func (testNopLogger) WithService(string) logx.Logger      { return testNopLogger{} }
func (testNopLogger) WithAttrs(...logx.Attr) logx.Logger  { return testNopLogger{} }
func (testNopLogger) WithGroup(string) logx.Logger        { return testNopLogger{} }
func (testNopLogger) SetLevel(logx.Level)                 {}

var _ logx.Logger = testNopLogger{}
