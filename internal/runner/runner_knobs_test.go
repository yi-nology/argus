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
	"github.com/yi-nology/argus/internal/worker"
)

func knobsTestRunner(t *testing.T) (*Runner, *storage.Store, context.Context) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "knobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{
		RepoMonthlyTokenBudget: 0, // 全局关
		TaskTokenBudget:        400_000,
		MaxDiffChars:           120_000,
	}
	r := &Runner{Cfg: cfg, Log: logx.Nop(), Store: store}
	return r, store, context.Background()
}

func seedRepoSetting(t *testing.T, store *storage.Store, platform, owner, repo, body string) {
	t.Helper()
	if err := store.UpsertRepoSetting(context.Background(), &storage.RepoSetting{
		Platform: platform, Owner: owner, Repo: repo,
		SettingsJSON: body, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertRepoSetting: %v", err)
	}
}

func TestLoadTaskKnobsOverride(t *testing.T) {
	r, store, ctx := knobsTestRunner(t)
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "web", "1")
	seedRepoSetting(t, store, "gitea", "acme", "web",
		`{"monthly_token_budget": 5000000, "task_token_budget": 800000, "max_diff_chars": 200000}`)

	k := r.loadTaskKnobs(ctx, task)
	if k.monthlyBudget != 5_000_000 || k.taskTokenBudget != 800_000 || k.maxDiffChars != 200_000 {
		t.Fatalf("覆盖未生效: %+v", k)
	}
}

func TestLoadTaskKnobsDefaultsAndPartial(t *testing.T) {
	r, store, ctx := knobsTestRunner(t)
	// 无覆盖行 → 全局默认。
	plain := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "api", "1")
	k := r.loadTaskKnobs(ctx, plain)
	if k.monthlyBudget != 0 || k.taskTokenBudget != 400_000 || k.maxDiffChars != 120_000 {
		t.Fatalf("默认态不符: %+v", k)
	}
	// 部分覆盖：未覆盖旋钮落全局值。
	seedRepoSetting(t, store, "gitea", "acme", "api", `{"max_diff_chars": 200000}`)
	k = r.loadTaskKnobs(ctx, plain)
	if k.maxDiffChars != 200_000 || k.taskTokenBudget != 400_000 || k.monthlyBudget != 0 {
		t.Fatalf("部分覆盖合并错: %+v", k)
	}
	// 落库值损坏（绕过 service 校验直写）→ 降级默认，不阻断任务。
	seedRepoSetting(t, store, "gitea", "acme", "broken", `not-json`)
	broken := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "broken", "1")
	k = r.loadTaskKnobs(ctx, broken)
	if k.taskTokenBudget != 400_000 || k.maxDiffChars != 120_000 {
		t.Fatalf("损坏配置未降级默认: %+v", k)
	}
}

// TestRepoBudgetGateUsesRepoKnob 月度预算闸吃按仓覆盖值：全局预算关闭
// （RepoMonthlyTokenBudget=0）时，仓库覆盖 monthly_token_budget=1 仍开启闸——
// 同仓已消耗 1 token（done 口径）的新任务被拦截落 failed。
func TestRepoBudgetGateUsesRepoKnob(t *testing.T) {
	r, store, ctx := knobsTestRunner(t)

	// 前置：同仓记 1 token 的 done 任务（claim→fenced 终态，归属凭据成立）。
	done := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "web", "1")
	if err := store.CreateTask(ctx, done); err != nil {
		t.Fatal(err)
	}
	q := &worker.StoreQueue{Store: store}
	if _, ok, err := q.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim done 前置: ok=%v err=%v", ok, err)
	}
	if _, err := store.FinishTaskFenced(ctx, done.ID, storage.StatusDone,
		"approve", "", "", "", 1, 0); err != nil {
		t.Fatalf("done 前置: %v", err)
	}

	// 无覆盖（全局预算关）→ 不拦截。
	target := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "web", "2")
	if err := store.CreateTask(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim target: ok=%v err=%v", ok, err)
	}
	if r.budgetGate(ctx, target, r.loadTaskKnobs(ctx, target), 0) {
		t.Fatal("全局关 + 无覆盖不应拦截")
	}

	// 仓库覆盖 monthly=1：loadTaskKnobs 合并出生效预算 1，已用 1 ≥ 1 → 拦截。
	seedRepoSetting(t, store, "gitea", "acme", "web", `{"monthly_token_budget": 1}`)
	knobs := r.loadTaskKnobs(ctx, target)
	if knobs.monthlyBudget != 1 {
		t.Fatalf("合并生效预算应为 1: %+v", knobs)
	}
	if !r.budgetGate(ctx, target, knobs, 0) {
		t.Fatal("按仓覆盖预算应拦截（used=1 >= budget=1）")
	}
	row, err := store.GetTask(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusFailed || !strings.Contains(row.FailReason, "预算已耗尽") {
		t.Fatalf("拦截终态错: status=%s fail=%s", row.Status, row.FailReason)
	}
}
