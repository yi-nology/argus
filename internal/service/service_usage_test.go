// service_usage_test.go —— /usage/monthly 双账本展示（#32 验收⑤）：预留 vs 实销。

package service

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// openUsageStore 用量测试用真 Store（sqlite 临时库）。
func openUsageStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestUsageMonthlyReservedVsSettled /usage/monthly 展示预留 vs 实销（#32 验收⑤）：
// 配额账（done 任务行）、实销（usage_event 逐调用）、预留（活跃预留）三口径
// 各自独立透出，互不串账。
func TestUsageMonthlyReservedVsSettled(t *testing.T) {
	st := openUsageStore(t)
	ctx := context.Background()
	s := &Service{Cfg: &config.Config{
		RepoMonthlyTokenBudget: 1000, RealTokenBudget: 2000,
	}, Store: st}

	// 配额账：done 任务 120。
	done := storage.NewTask("code_review", "gitea", "acme", "app", "1")
	done.ID = "t-done"
	done.HeadSHA = "t-done"
	if err := st.CreateTask(ctx, done); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishTaskFenced(ctx, "t-done", storage.StatusDone,
		"approve", "", "", "", 120, 0); err != nil {
		t.Fatal(err)
	}
	// 实销：运行中任务的两笔逐调用 80+70=150（不在配额账里）。
	for i, ev := range []struct {
		inv string
		tok int
	}{{"inv-mu-1", 80}, {"inv-mu-2", 70}} {
		if err := st.RecordUsageEvent(ctx, &storage.UsageEvent{
			TaskID: "t-run", AttemptID: "0", InvocationID: ev.inv,
			Platform: "gitea", Owner: "acme", Repo: "app",
			Model: "m", InputTokens: ev.tok, Status: storage.UsageSettled,
			OccurredAt: time.Now(),
		}); err != nil {
			t.Fatalf("第 %d 笔实销: %v", i, err)
		}
	}
	// 预留：另一任务执行前预留 300。
	if res, err := st.ReserveRepoBudget(ctx, storage.ReserveInput{
		TaskID: "t-hold", AttemptID: "0",
		Platform: "gitea", Owner: "acme", Repo: "app",
		TaskBudget: 300, QuotaBudget: 1000, Since: time.Now(),
	}); err != nil || res.Verdict != storage.ReserveOK {
		t.Fatalf("预留前置: %+v err=%v", res, err)
	}

	body, code := s.UsageMonthly(ctx)
	if code != http.StatusOK {
		t.Fatalf("应 200，got %d", code)
	}
	if body["total"].(int64) != 120 {
		t.Fatalf("配额账 total 应 120: %v", body["total"])
	}
	// 实销 = 全部真实消耗：done 任务的 120（终态差额兜底入账）+ 运行中 150 = 270。
	if body["real_total"].(int64) != 270 {
		t.Fatalf("实销 real_total 应 270: %v", body["real_total"])
	}
	if body["reserved_total"].(int64) != 300 {
		t.Fatalf("预留 reserved_total 应 300: %v", body["reserved_total"])
	}
	if body["budget"].(int) != 1000 || body["real_budget"].(int) != 2000 {
		t.Fatalf("预算面失真: budget=%v real_budget=%v", body["budget"], body["real_budget"])
	}
	// 分仓列表：实销 270（tasks=2 去重：t-done + t-run）、预留 300。
	realRepos := body["real_repos"].([]storage.RepoTokenUsage)
	if len(realRepos) != 1 || realRepos[0].Tokens != 270 || realRepos[0].Tasks != 2 {
		t.Fatalf("real_repos 失真: %+v", realRepos)
	}
	reservedRepos := body["reserved_repos"].([]storage.RepoTokenUsage)
	if len(reservedRepos) != 1 || reservedRepos[0].Tokens != 300 {
		t.Fatalf("reserved_repos 失真: %+v", reservedRepos)
	}
}
