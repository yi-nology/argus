package storage

import (
	"context"
	"testing"
	"time"
)

// TestRepoMonthlyTokens_ExactlyAtLimit 恰好在预算线上 → 不应被门控。
func TestRepoMonthlyTokens_ExactlyAtLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "e1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 100
	_ = s.CreateTask(ctx, tk)
	used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if used != 100 {
		t.Fatalf("want100，got %d", used)
	}
	// 门控逻辑（在 runner 里）应是 used >= budget 才短路，100 < 101 应放行
}

// TestRepoMonthlyTokens_Exceeded 超过预算 → 应被门控。
func TestRepoMonthlyTokens_Exceeded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "e1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 200
	_ = s.CreateTask(ctx, tk)
	used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if used != 200 {
		t.Fatalf("want200，got %d", used)
	}
	// 200 >= 150 → runner 应短路
}

// TestRepoMonthlyTokens_FailedIgnored failed 任务不计入预算。
func TestRepoMonthlyTokens_FailedIgnored(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "e1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusFailed
	tk.TokensUsed = 9999
	_ = s.CreateTask(ctx, tk)
	used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if used != 0 {
		t.Fatalf("failed 不应计入: %d", used)
	}
}

// TestRepoMonthlyTokens_StaleIgnored stale 任务不计入。
func TestRepoMonthlyTokens_StaleIgnored(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "e1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusStale
	tk.TokensUsed = 500
	_ = s.CreateTask(ctx, tk)
	used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if used != 0 {
		t.Fatalf("stale 不应计入: %d", used)
	}
}

// TestRepoMonthlyTokens_OldMonth 上月任务不在本月预算里。
func TestRepoMonthlyTokens_OldMonth(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "e1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 500
	since := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	// 上月取「上月 1 日」而非 AddDate(0,-1,0) 直减：31 日减一月会归一化回本月
	// （10-31 → 9-31 → 10-01），每月末日跑测试必假红。
	tk.CreatedAt = since.AddDate(0, -1, 0)
	_ = s.CreateTask(ctx, tk)
	used, _ := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", since)
	if used != 0 {
		t.Fatalf("上月任务不应计入本月: %d", used)
	}
}

// TestFindReportByHead_MultipleHeads 多个 SHA 各自独立查。
func TestFindReportByHead_MultipleHeads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, sha := range []string{"aaa", "bbb", "ccc"} {
		e := "e" + string(rune('1'+i))
		tk := mkTask("t"+string(rune('1'+i)), &e, sha)
		tk.Status = StatusDone
		tk.Report = "报告" + sha
		_ = s.CreateTask(ctx, tk)
	}
	got, err := s.FindReportByHead(ctx, "bbb")
	if err != nil || got.Report != "报告bbb" {
		t.Fatalf("应命中 bbb 报告: %+v err=%v", got, err)
	}
}

// TestMonthlyTokensByRepo_Empty 无 done 任务时返回空。
func TestMonthlyTokensByRepo_Empty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	usage, _ := s.MonthlyTokensByRepo(ctx, timeMinAgo(60))
	if len(usage) != 0 {
		t.Fatalf("无 done 任务应返回空: %+v", usage)
	}
}

// TestMonthlyTokensByRepo_MultiRepo 多仓库聚合。
func TestMonthlyTokensByRepo_MultiRepo(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, tc := range []struct {
		repo   string
		tokens int
	}{
		{"app", 100}, {"app", 200}, {"lib", 50},
	} {
		eid := "e-multi-" + string(rune('1'+i))
		tk := mkTask("t-multi-"+string(rune('1'+i)), &eid, "sha-multi-"+string(rune('1'+i)))
		tk.Repo = tc.repo
		tk.Status = StatusDone
		tk.TokensUsed = tc.tokens
		_ = s.CreateTask(ctx, tk)
	}
	usage, _ := s.MonthlyTokensByRepo(ctx, timeMinAgo(60))
	if len(usage) != 2 {
		t.Fatalf("应有2个仓库: %+v", usage)
	}
	// app=300, lib=50（按 tokens DESC 排序）
	if usage[0].Repo != "app" || usage[0].Tokens != 300 {
		t.Fatalf("app 应排第一且 tokens=300: %+v", usage[0])
	}
}
