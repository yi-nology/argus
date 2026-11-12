package storage

import (
	"context"
	"testing"
	"time"
)

func timeMinAgo(min int) time.Time { return time.Now().Add(-time.Duration(min) * time.Minute) }

// TestRepoMonthlyTokens 月度聚合只统计 done 任务，且按仓库维度过滤。
func TestRepoMonthlyTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "d1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 300
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	tk2 := mkTask("t2", nil, "sha1")
	tk2.Status = StatusFailed // failed 不计入预算
	tk2.TokensUsed = 5000
	_ = s.CreateTask(ctx, tk2)

	used, err := s.RepoMonthlyTokens(ctx, "gitea", "acme", "app", timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if used != 300 {
		t.Fatalf("want 300（只算 done），got %d", used)
	}
	used, _ = s.RepoMonthlyTokens(ctx, "gitea", "acme", "other", timeMinAgo(60))
	if used != 0 {
		t.Fatalf("其他仓库应为 0，got %d", used)
	}
}

// TestMonthlyTokensByRepo 分组聚合。
func TestMonthlyTokensByRepo(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "d1"
	tk := mkTask("t1", &e, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 120
	_ = s.CreateTask(ctx, tk)

	usage, err := s.MonthlyTokensByRepo(ctx, timeMinAgo(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].Tokens != 120 || usage[0].Repo != "app" {
		t.Fatalf("聚合结果不符: %+v", usage)
	}
}

// TestFindReportByHead 按 SHA 查最近成功报告；无报告的 SHA 404。
func TestFindReportByHead(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	e := "d1"
	tk := mkTask("t1", &e, "abc123")
	tk.Status = StatusDone
	tk.Report = "# 报告"
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindReportByHead(ctx, "abc123", "", "", "")
	if err != nil || got.Report != "# 报告" {
		t.Fatalf("want 命中，got %v err=%v", got, err)
	}
	if _, err := s.FindReportByHead(ctx, "missing", "", "", ""); err == nil {
		t.Fatal("未命中应报 NotFound")
	}
}
