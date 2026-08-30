package worker

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/storage"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)  {}
func (nopLogger) Info(string, ...any)   {}
func (nopLogger) Warn(string, ...any)   {}
func (nopLogger) Error(string, ...any)  {}
func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}

func (nopLogger) CtxDebug(context.Context, string, ...any)  {}
func (nopLogger) CtxInfo(context.Context, string, ...any)   {}
func (nopLogger) CtxWarn(context.Context, string, ...any)   {}
func (nopLogger) CtxError(context.Context, string, ...any)  {}
func (nopLogger) CtxDebugf(context.Context, string, ...any) {}
func (nopLogger) CtxInfof(context.Context, string, ...any)  {}
func (nopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (nopLogger) CtxErrorf(context.Context, string, ...any) {}

func (nopLogger) WithCtx(context.Context) logx.Logger { return nopLogger{} }
func (nopLogger) WithService(string) logx.Logger      { return nopLogger{} }
func (nopLogger) WithAttrs(...logx.Attr) logx.Logger  { return nopLogger{} }
func (nopLogger) WithGroup(string) logx.Logger        { return nopLogger{} }
func (nopLogger) SetLevel(logx.Level)                 {}

var _ logx.Logger = nopLogger{}

func testStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 两阶段停机：在途任务在 grace 内自然完成，不被硬取消。
func TestStopGracefulCompletion(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	_ = store.CreateTask(ctx, &storage.Task{ID: "t1", Kind: "code_review", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: storage.StatusPending, CreatedAt: time.Now()})

	var executed atomic.Bool
	p := &Pool{Store: store, N: 1, Log: nopLogger{},
		Run: func(ctx context.Context, taskID string) error {
			executed.Store(true)
			return nil
		}}
	p.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for !executed.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	p.Stop(5 * time.Second) // 应立即返回（任务已完成）
	if !executed.Load() {
		t.Fatal("任务未执行")
	}
}

// grace 超时：在途任务被硬取消，且 Stop 会返回（不卡死）。
func TestStopGraceExceeded(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	_ = store.CreateTask(ctx, &storage.Task{ID: "t1", Kind: "code_review", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: storage.StatusPending, CreatedAt: time.Now()})

	started := make(chan struct{})
	runCtxCancelled := make(chan struct{})
	p := &Pool{Store: store, N: 1, Log: nopLogger{},
		Run: func(ctx context.Context, taskID string) error {
			close(started) // 通知：在途任务已开始
			<-ctx.Done()   // 挂起直到被硬取消
			close(runCtxCancelled)
			return ctx.Err()
		}}
	p.Start(ctx)
	<-started // 等真正领取并进入执行，再触发停机（否则 worker 可能尚未 claim）
	start := time.Now()
	p.Stop(300 * time.Millisecond)
	elapsed := time.Since(start)

	select {
	case <-runCtxCancelled:
	default:
		t.Fatal("grace 超时后应硬取消执行上下文")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Stop 应在 grace 后返回，实际 %s", elapsed)
	}
	// 任务保持 running：下次启动重置回 pending（既有语义）
	tk, _ := store.GetTask(context.Background(), "t1")
	if tk.Status != storage.StatusRunning {
		t.Fatalf("被硬取消的任务应保持 running，got %s", tk.Status)
	}
}
