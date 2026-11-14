// runner_budget_test.go —— 执行前预算闸（#32）：预留判定、在途预留挡后启任务、
// 双账本拒绝路径与失败播报。

package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// budgetTestRunner 造带真 Store 的 Runner（sqlite 临时库）。
func budgetTestRunner(t *testing.T) (*Runner, *storage.Store, context.Context) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{RepoMonthlyTokenBudget: 0, TaskTokenBudget: 400_000}
	r := &Runner{Cfg: cfg, Log: logx.Nop(), Store: store}
	return r, store, context.Background()
}

// budgetTask 落一个任务行供闸归属。
func budgetTask(t *testing.T, store *storage.Store, id string) *storage.Task {
	t.Helper()
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "1")
	task.ID = id
	task.HeadSHA = id
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

// TestBudgetGateReservesBeforeRun 通过闸 = 落预留行，且在途预留挡住后续启动：
// 配额 500、任务预算 400 → t1 拿 400、t2 按剩余钳到 100、t3 拒绝
// （#32 验收③：新任务超限拒绝在启动前，多副本并发超支窗口闭合）。
func TestBudgetGateReservesBeforeRun(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	knobs := taskKnobs{monthlyBudget: 500, taskTokenBudget: 400}

	t1 := budgetTask(t, store, "t1")
	if intercepted, granted := r.budgetGate(ctx, t1, knobs, 0); intercepted {
		t.Fatal("t1 额度内不应拦截")
	} else if granted != 400 {
		t.Fatalf("t1 授予值应 400（任务预算未被钳制），got %d", granted)
	}
	if held, _ := store.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 400 {
		t.Fatalf("t1 放行应落 400 预留，got %d", held)
	}

	t2 := budgetTask(t, store, "t2")
	if intercepted, granted := r.budgetGate(ctx, t2, knobs, 0); intercepted {
		t.Fatal("t2 剩余 100 内不应拦截（按剩余钳制放行）")
	} else if granted != 100 {
		t.Fatalf("t2 授予值应钳到剩余 100（评审脱节场景），got %d", granted)
	}
	if held, _ := store.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 500 {
		t.Fatalf("t2 预留应钳到 100（合计 500），got %d", held)
	}

	t3 := budgetTask(t, store, "t3")
	if intercepted, _ := r.budgetGate(ctx, t3, knobs, 0); !intercepted {
		t.Fatal("t3 已承诺 500/500 应拦截")
	}
	row, err := store.GetTask(ctx, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusFailed || !strings.Contains(row.FailReason, "预算已耗尽") {
		t.Fatalf("拦截终态失真: status=%s fail=%s", row.Status, row.FailReason)
	}
	// 拒绝不落预留行。
	if held, _ := store.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 500 {
		t.Fatalf("拒绝不应改变预留合计，got %d", held)
	}
}

// TestBudgetGateRealLedgerRejects 实销账拒绝路径：运行中任务的逐调用实销
// 占满真实预算 → 新任务启动前拒绝并走真实账失败播报（#32 验收③）。
func TestBudgetGateRealLedgerRejects(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	knobs := taskKnobs{monthlyBudget: 0, taskTokenBudget: 300} // 配额账关

	// 运行中任务已实销 100（不在任何任务终态账里——旧口径看不见）。
	run := budgetTask(t, store, "t-run")
	run.Status = storage.StatusRunning
	if err := store.RecordUsageEvent(ctx, &storage.UsageEvent{
		TaskID: run.ID, AttemptID: "0", InvocationID: "inv-run",
		Platform: "gitea", Owner: "acme", Repo: "app",
		Model: "m", InputTokens: 100, Status: storage.UsageSettled,
		OccurredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	target := budgetTask(t, store, "t-target")
	if intercepted, _ := r.budgetGate(ctx, target, knobs, 100); !intercepted {
		t.Fatal("实销 100/100 应拦截")
	}
	row, err := store.GetTask(ctx, "t-target")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusFailed || !strings.Contains(row.FailReason, "真实") {
		t.Fatalf("真实账拦截终态失真: status=%s fail=%s", row.Status, row.FailReason)
	}
	if held, _ := store.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 0 {
		t.Fatalf("拒绝不应落预留，got %d", held)
	}
}

// TestBudgetGateBothLedgersOff 双账本全关：零开销直通、不落预留（沿用旧闸
// budget<=0 关闭语义）。
func TestBudgetGateBothLedgersOff(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	task := budgetTask(t, store, "t-off")
	if intercepted, _ := r.budgetGate(ctx, task, taskKnobs{monthlyBudget: 0, taskTokenBudget: 400_000}, 0); intercepted {
		t.Fatal("双账本全关不应拦截")
	}
	if held, _ := store.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 0 {
		t.Fatalf("全关不应落预留，got %d", held)
	}
}

// TestBudgetGateFailOpenOnStorageError 判定不可得（存储故障）→ fail-open 放行
// （沿用旧两闸「查询失败放行」语义，闸自身故障不阻断审查）。
func TestBudgetGateFailOpenOnStorageError(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	task := budgetTask(t, store, "t-broken")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if intercepted, _ := r.budgetGate(ctx, task, taskKnobs{monthlyBudget: 100, taskTokenBudget: 400_000}, 100); intercepted {
		t.Fatal("存储故障应 fail-open 放行")
	}
}

// TestBudgetGateHardRejectsOnStorageError hard 模式（Phase1①）：判定不可得
// （存储故障）不再 fail-open，闸直接拦截——成本敏感环境「账本不可用就不开
// 新计费调用」。库已关故只断言拦截行为（终态写入是 best-effort，与既有
// FailOpen 用例同 harness 形态）；soft 侧由 TestBudgetGateFailOpenOnStorageError
// 保持回归。
func TestBudgetGateHardRejectsOnStorageError(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	r.Cfg.BudgetEnforcement = "hard"
	knobs := taskKnobs{monthlyBudget: 100, taskTokenBudget: 400_000}
	task := budgetTask(t, store, "t-hard")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if intercepted, _ := r.budgetGate(ctx, task, knobs, 0); !intercepted {
		t.Fatal("hard 模式存储故障应拦截（fail-closed）")
	}
}
