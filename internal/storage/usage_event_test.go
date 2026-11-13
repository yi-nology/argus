// usage_event_test.go —— 逐调用成本账本（#32）：usage_event 不可重复计费记录、
// invocation_id 幂等、真实消费账（实销）的逐调用口径。

package storage

import (
	"context"
	"testing"
	"time"
)

// mkEvent 造一笔逐调用实销记录（缺省 gitea/acme/app 仓）。
func mkEvent(taskID, invocationID, repo string, in, out int) *UsageEvent {
	return &UsageEvent{
		TaskID: taskID, AttemptID: "0", InvocationID: invocationID,
		Platform: "gitea", Owner: "acme", Repo: repo,
		Provider: "llm.example", Model: "test-model",
		InputTokens: in, OutputTokens: out, CachedTokens: 0,
		EstimatedCost: 0.001,
		Status:        UsageSettled, OccurredAt: time.Now(),
	}
}

// TestRecordUsageEvent_Idempotent invocation_id 幂等（#32 验收②）：同键重放
// 收敛为 no-op 不报错，实销只计一次——重试/回调重放不重复计费。
func TestRecordUsageEvent_Idempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.RecordUsageEvent(ctx, mkEvent("t1", "inv-1", "app", 100, 20)); err != nil {
		t.Fatalf("首笔记账: %v", err)
	}
	// 同 invocation_id 重放（物理调用重复投递/落库重试）：幂等收敛。
	if err := s.RecordUsageEvent(ctx, mkEvent("t1", "inv-1", "app", 100, 20)); err != nil {
		t.Fatalf("同键重放应幂等收敛，got err=%v", err)
	}
	// 不同 invocation_id 是另一笔真实调用，照记。
	if err := s.RecordUsageEvent(ctx, mkEvent("t1", "inv-2", "app", 50, 10)); err != nil {
		t.Fatalf("第二笔记账: %v", err)
	}

	total, err := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if total != 180 {
		t.Fatalf("实销应 180（inv-1 只计一次 120 + inv-2 60），got %d", total)
	}
}

// TestRealLedgerFromUsageEvents 实销口径 = usage_event 逐调用聚合（#32 验收①）：
// 运行中任务的调用即时入账、失败任务的调用照记——与任务终态脱钩（旧口径
// 运行中消费不在任一账本、失败/重试的中途消费绕过硬预算）。
func TestRealLedgerFromUsageEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 三个任务分别处于 running / failed / done，各有一笔调用入账。
	for _, tc := range []struct{ id, status string }{
		{"t-running", StatusRunning},
		{"t-failed", StatusFailed},
		{"t-done", StatusDone},
	} {
		tk := mkTask(tc.id, nil, "sha-"+tc.id)
		tk.Status = tc.status
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordUsageEvent(ctx, mkEvent("t-running", "inv-r", "app", 300, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageEvent(ctx, mkEvent("t-failed", "inv-f", "app", 100, 50)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageEvent(ctx, mkEvent("t-done", "inv-d", "app", 40, 10)); err != nil {
		t.Fatal(err)
	}

	total, err := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if total != 500 {
		t.Fatalf("实销应 500（运行中+失败+完成 全计），got %d", total)
	}
	// 任务行的 tokens_used 此时为 0（未终态聚合）——实销不再依赖任务行。
	if used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60)); used != 0 {
		t.Fatalf("配额账（done 口径、任务行）应仍为 0，got %d", used)
	}
}

// TestMonthlyTokensByRepoRealGrouping 实销分仓聚合（/usage/monthly 数据源）：
// 按仓求和、tasks 列 = 触达的不同任务数。
func TestMonthlyTokensByRepoRealGrouping(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_ = s.RecordUsageEvent(ctx, mkEvent("t1", "inv-a", "app", 100, 0))
	_ = s.RecordUsageEvent(ctx, mkEvent("t1", "inv-b", "app", 50, 0)) // 同任务第二笔调用
	_ = s.RecordUsageEvent(ctx, mkEvent("t2", "inv-c", "lib", 20, 0))

	repos, err := s.MonthlyTokensByRepoReal(ctx, timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("应有 2 个仓: %+v", repos)
	}
	// app=150（两笔调用）排第一，tasks=1（同任务去重）。
	if repos[0].Repo != "app" || repos[0].Tokens != 150 || repos[0].Tasks != 1 {
		t.Fatalf("app 聚合失真: %+v", repos[0])
	}
	if repos[1].Repo != "lib" || repos[1].Tokens != 20 {
		t.Fatalf("lib 聚合失真: %+v", repos[1])
	}
}

// TestRealLedgerSinceWindow 实销按 occurred_at 落窗：上月的调用不计本月。
func TestRealLedgerSinceWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	ev := mkEvent("t-old", "inv-old", "app", 700, 0)
	ev.OccurredAt = time.Now().AddDate(0, -1, 0)
	if err := s.RecordUsageEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	_ = s.RecordUsageEvent(ctx, mkEvent("t-new", "inv-new", "app", 30, 0))

	total, err := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if total != 30 {
		t.Fatalf("窗口外调用不应计入，got %d（期望 30）", total)
	}
}

// TestFinishBackfillsUsageDelta 终态差额兜底（#32）：埋点是 best-effort，落了
// 一半事件的任务终态时按 tokens_used − 本次执行已落事件补差——丢笔不丢账；
// 重复终态写入不重复补账（幂等）。
func TestFinishBackfillsUsageDelta(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	tk := mkTask("t1", nil, "sha1")
	tk.Status = StatusRunning
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	// 本次执行只落了一笔 60（另一笔写失败），终态 tokens_used=100。
	if err := s.RecordUsageEvent(ctx, mkEvent("t1", "inv-1", "app", 60, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishTaskFenced(ctx, "t1", StatusDone, "approve", "", "", "", 100, 0); err != nil {
		t.Fatal(err)
	}

	total, err := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if total != 100 {
		t.Fatalf("终态后实销应补到 100，got %d", total)
	}
	// 重复终态写入（同 tokens）：差额为 0，不重复补账。
	if _, err := s.FinishTaskFenced(ctx, "t1", StatusDone, "approve", "", "", "", 100, 0); err != nil {
		t.Fatal(err)
	}
	if total, _ := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60)); total != 100 {
		t.Fatalf("重复终态不应重复补账，got %d", total)
	}
}

// TestPruneUsageEvents 账本保留清理（#32）：过期实销事件与已收口预留按 cutoff
// 分批删除；近期事件与活跃预留（仍在占额度）不动。
func TestPruneUsageEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	old := mkEvent("t-old", "inv-old", "app", 100, 0)
	old.OccurredAt = time.Now().AddDate(0, -2, 0)
	if err := s.RecordUsageEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	_ = s.RecordUsageEvent(ctx, mkEvent("t-new", "inv-new", "app", 50, 0))
	// 已核销的旧预留（应随清理走）+ 活跃预留（应保留）。
	if err := s.db.Create(&BudgetReservation{
		TaskID: "t-settled-old", Platform: "gitea", Owner: "acme", Repo: "app",
		Tokens: 10, Status: ReservationSettled,
		ExpiresAt: time.Now().AddDate(0, -2, 0), UpdatedAt: time.Now().AddDate(0, -2, 0),
		CreatedAt: time.Now().AddDate(0, -2, 0),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveRepoBudget(ctx, reserveIn("t-active", 20, 1000, 1000)); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneUsageEvents(ctx, time.Now().AddDate(0, -1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应清理 2 行（旧事件+旧核销预留），got %d", n)
	}
	// 近期事件仍在。
	if total, _ := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60)); total != 50 {
		t.Fatalf("近期事件应保留，got %d", total)
	}
	// 活跃预留仍在占额度。
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 20 {
		t.Fatalf("活跃预留不应被清理，got %d", held)
	}
}

// TestReconcileUsageEventsBackfill 历史实销回填（#32 启动期对账）：升级前已
// 终态、无逐调用事件的任务按 tokens_used 补 settled 事件（invocation_id
// 幂等）；已有事件的任务不动；重跑幂等。
func TestReconcileUsageEventsBackfill(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 历史任务（终态、无事件）。
	hist := mkTask("t-hist", nil, "sha-h")
	hist.Status = StatusDone
	hist.TokensUsed = 500
	if err := s.CreateTask(ctx, hist); err != nil {
		t.Fatal(err)
	}
	// 新口径任务（终态、事件足额）——回填不得重复计。
	fresh := mkTask("t-fresh", nil, "sha-f")
	fresh.Status = StatusFailed
	fresh.TokensUsed = 80
	if err := s.CreateTask(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageEvent(ctx, mkEvent("t-fresh", "inv-f", "app", 80, 0)); err != nil {
		t.Fatal(err)
	}

	n, err := s.ReconcileUsageEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应回填 1 个历史任务，got %d", n)
	}
	total, err := s.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if total != 580 {
		t.Fatalf("回填后实销应 580（500+80），got %d", total)
	}
	// 重跑幂等：已有事件的任务被 NOT EXISTS 排除。
	if n, err := s.ReconcileUsageEvents(ctx); err != nil || n != 0 {
		t.Fatalf("重跑应回填 0 行: n=%d err=%v", n, err)
	}
}
