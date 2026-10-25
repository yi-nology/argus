// runner_panic_test.go —— v3.185.4 回归锚：任务体 panic 必落 failed 终态。
// live 实弹炸出：影响面注入 nil map panic 后任务卡死 running（上游 queue/worker
// 的 panic 隔离只记日志，心跳过期复位对确定性 panic 是「复位→再炸」死循环）；
// guardPanic 把 panic 转 error 走 handleRunFailure 的 failed-retry 正道。
package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oekit/logx"

	"argus/internal/config"
	"argus/internal/plugin"
	"argus/internal/storage"
	"argus/internal/worker"
)

// TestExecutePanicToFailedTerminal Execute 层端到端：SDK/Poster 全 nil 的裸
// Runner 上跑真实任务——任务体内确定性 panic（nil SDK 解引用）被 guardPanic
// 转换，任务必达 failed 终态且 FailReason 带 panic 线索。
func TestExecutePanicToFailedTerminal(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "panic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	r := &Runner{Cfg: &config.Config{}, Log: logx.NewSlogLogger("test"), Store: store}

	task := storage.NewTask(plugin.TaskKindCodeReview, "gitea", "o", "r", "7")
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// 走真实 claim（pending→running）：fenced 终态写入以此为归属凭据，
	// 直接 Execute 会因任务仍 pending 被正确拒写（那正是 fencing 的职责）
	q := &worker.StoreQueue{Store: store}
	if _, ok, err := q.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// SDK 未注入：prepareContext 的平台调用必炸——守卫必须兜住并落终态
	_ = r.Execute(ctx, task.ID)

	row, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusFailed {
		t.Fatalf("panic 后任务终态 = %s, want failed（卡 running 即本回归锚要防的形态）", row.Status)
	}
	if !strings.Contains(row.FailReason, "panic") {
		t.Fatalf("FailReason 缺 panic 线索: %s", row.FailReason)
	}
}

// TestGuardPanicPassthrough guardPanic 契约：无 panic 原样透传返回值，panic
// 转 error 且日志含栈。
func TestGuardPanicPassthrough(t *testing.T) {
	log := logx.NewSlogLogger("test")
	ok := guardPanic(log, "t1", func() error { return nil })
	if ok != nil {
		t.Fatalf("无 panic 应透传 nil: %v", ok)
	}
	boom := guardPanic(log, "t2", func() error { panic("boom-123") })
	if boom == nil || !strings.Contains(boom.Error(), "boom-123") {
		t.Fatalf("panic 应转 error: %v", boom)
	}
}
