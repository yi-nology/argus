// runner_usage_test.go —— 逐调用实销埋点（#32）：execContext UsageHandler 的
// 落账行为——usage_event 行构造、成本估算、存储故障 best-effort 不阻断。

package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/oagentkit/llm"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// usageTestRunner 造带真 Store 的 Runner（sqlite 临时库）。
func usageTestRunner(t *testing.T) (*Runner, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{LLMCacheReadRatio: 0.1}
	r := &Runner{
		Cfg: cfg, Store: store, Log: logx.Nop(),
		CostPer1K: func(string) [2]float64 { return [2]float64{0.1, 0.2} }, // 每 1K token
	}
	return r, store
}

// usageTestTask 落一个任务行供埋点归属。
func usageTestTask(t *testing.T, store *storage.Store, id string) *storage.Task {
	t.Helper()
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "app", "1")
	task.ID = id
	task.HeadSHA = id
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

// TestRecordUsageEventWritesLedger 埋点落账：UsageRecord → usage_event 行
// （任务/attempt/模型/三段 token/成本），实销账立即可见（#32 验收①调用点）。
func TestRecordUsageEventWritesLedger(t *testing.T) {
	r, store := usageTestRunner(t)
	ctx := context.Background()
	task := usageTestTask(t, store, "t-usage")

	r.recordUsageEvent(ctx, task, llm.UsageRecord{
		Model: "test-model", PromptTokens: 100, CompletionTokens: 20, CachedTokens: 10,
	})

	total, err := store.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if total != 120 {
		t.Fatalf("埋点后实销应 120，got %d", total)
	}
	// 行字段面：归属/幂等键前缀/三段 token/成本估算。
	events, err := store.TaskUsageEvents(ctx, "t-usage")
	if err != nil || len(events) != 1 {
		t.Fatalf("应有 1 条 usage_event: n=%d err=%v", len(events), err)
	}
	ev := events[0]
	if ev.InvocationID == "" || len(ev.InvocationID) < 3 || ev.InvocationID[:2] != "u-" {
		t.Fatalf("invocation_id 应为 u- 前缀唯一键: %q", ev.InvocationID)
	}
	if ev.AttemptID != "0" || ev.Model != "test-model" || ev.Platform != "gitea" ||
		ev.Owner != "acme" || ev.Repo != "app" {
		t.Fatalf("行归属/模型失真: %+v", ev)
	}
	if ev.InputTokens != 100 || ev.OutputTokens != 20 || ev.CachedTokens != 10 {
		t.Fatalf("三段 token 失真: %+v", ev)
	}
	if ev.Status != storage.UsageSettled {
		t.Fatalf("状态应 settled: %s", ev.Status)
	}
	// 成本估算：输入摊薄 0.1→91/1M（cached10×0.1 折扣）、输出 0.2→200/1M
	// = 91×100/1e6 + 200×20/1e6 = 0.0131。
	if ev.EstimatedCost < 0.0130 || ev.EstimatedCost > 0.0132 {
		t.Fatalf("成本估算失真: %v（期望 ≈0.0131）", ev.EstimatedCost)
	}
}

// TestRecordUsageEventBestEffort 存储故障 best-effort：落账失败只告警不上抛、
// 不 panic——账本绝不阻断审查主链路（丢笔由终态差额兜底补）。
func TestRecordUsageEventBestEffort(t *testing.T) {
	r, store := usageTestRunner(t)
	task := usageTestTask(t, store, "t-closed")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// 库已关：调用必须安全返回（不 panic、不返回 error——sink 契约无返回值）。
	r.recordUsageEvent(context.Background(), task, llm.UsageRecord{
		Model: "m", PromptTokens: 10, CompletionTokens: 1,
	})
}

// TestExecContextWiresUsageRecording 埋点接线（#32 验收①端到端）：execContext
// 注册的 UsageHandler 在模型调用回调（OnStart/OnEnd）后落 usage_event 并回灌
// 任务预算——证明埋点挂在真实调用路径上，而非旁路函数。
func TestExecContextWiresUsageRecording(t *testing.T) {
	r, store := usageTestRunner(t)
	ctx := context.Background()
	task := usageTestTask(t, store, "t-wire")

	budget := dispatch.NewTokenBudget(0)
	taskCost := llm.NewCostTracker()
	wctx := r.execContext(ctx, task, budget, taskCost)

	// 模拟一次 ChatModel 调用（eino 回调路径：EnsureRunInfo → OnStart → OnEnd）。
	wctx = callbacks.EnsureRunInfo(wctx, "test-model", components.ComponentOfChatModel)
	wctx = callbacks.OnStart(wctx, &model.CallbackInput{})
	_ = callbacks.OnEnd(wctx, &model.CallbackOutput{
		TokenUsage: &model.TokenUsage{PromptTokens: 60, CompletionTokens: 15, TotalTokens: 75},
	})

	total, err := store.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if total != 75 {
		t.Fatalf("回调路径埋点后实销应 75，got %d", total)
	}
	if used := budget.Used(); used != 75 {
		t.Fatalf("任务预算应回灌 75，got %d", used)
	}
}

// TestRecordUsageEventIdempotentByInvocation 重复投递（同 invocation_id 由
// 落库侧重放）不重复计费——sink 每次生成新键，幂等收敛在 storage.RecordUsageEvent。
func TestRecordUsageEventIdempotentByInvocation(t *testing.T) {
	r, store := usageTestRunner(t)
	ctx := context.Background()
	task := usageTestTask(t, store, "t-dup")

	r.recordUsageEvent(ctx, task, llm.UsageRecord{Model: "m", PromptTokens: 50})
	first, err := store.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", time.Now().Add(-time.Hour))
	if err != nil || first != 50 {
		t.Fatalf("首笔应 50: got=%d err=%v", first, err)
	}
	// 取既有行的 invocation_id 重放（模拟写库重试同键）：幂等 no-op。
	events, err := store.TaskUsageEvents(ctx, "t-dup")
	if err != nil || len(events) != 1 {
		t.Fatalf("应有 1 条事件: n=%d err=%v", len(events), err)
	}
	dup := events[0]
	dup.ID = 0 // 清主键：走 invocation_id 唯一键的幂等路径（写库重试同键形态）
	if err := store.RecordUsageEvent(ctx, &dup); err != nil {
		t.Fatalf("同键重放应幂等: %v", err)
	}
	if total, _ := store.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", time.Now().Add(-time.Hour)); total != 50 {
		t.Fatalf("重放不应重复计费，got %d", total)
	}
}
