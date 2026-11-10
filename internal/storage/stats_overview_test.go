package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// TestStatsOverview v3.88.0：按仓聚合只算 done 任务、verdict 分布、抑制计数、
// 可选过滤；failed 任务与窗口外数据不计入。
func TestStatsOverview(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mk := func(id, verdict string, tokens int, status string) {
		e := id + "-ev"
		tk := mkTask(id, &e, "sha-"+id)
		tk.Status = status
		tk.Verdict = verdict
		tk.TokensUsed = tokens
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	mk("t1", "approve", 100, StatusDone)
	mk("t2", "comment", 50, StatusDone)
	mk("t3", "approve", 9999, StatusFailed) // failed 不计入
	// 窗口外（2h 前）不计入
	old := mkTask("t4", nil, "sha-t4")
	old.Status = StatusDone
	old.Verdict = "request_changes"
	old.TokensUsed = 7777
	old.CreatedAt = time.Now().Add(-2 * time.Hour)
	if err := s.CreateTask(ctx, old); err != nil {
		t.Fatal(err)
	}

	// prints：同 PR 两条意见，其中一条抑制
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp0aaaaaaaaaaaa",
		domain.Finding{File: "a.go", Line: 1, Severity: "low", Comment: "x", By: "correctness"}, "t1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp0bbbbbbbbbbbb",
		domain.Finding{File: "b.go", Line: 2, Severity: "medium", Comment: "y", By: "correctness"}, "t2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", "fp0aaaaaaaaaaaa",
		domain.Finding{File: "a.go", Comment: "x", By: "correctness"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	rows, err := s.StatsOverview(ctx, timeMinAgo(60), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应恰 1 仓聚合行: %+v", rows)
	}
	r := rows[0]
	if r.TasksDone != 2 || r.Approved != 1 || r.Commented != 1 || r.Rejected != 0 {
		t.Fatalf("任务/verdict 聚合不符: %+v", r)
	}
	if r.Tokens != 150 {
		t.Fatalf("tokens 应 150（只算 done），got %d", r.Tokens)
	}
	if r.Suppressed != 1 {
		t.Fatalf("抑制计数应 1，got %d", r.Suppressed)
	}

	// 过滤：命中返回行，不命中返回空
	if rows, _ = s.StatsOverview(ctx, timeMinAgo(60), "gitea", "acme", "app"); len(rows) != 1 {
		t.Fatalf("过滤命中应 1 行，got %d", len(rows))
	}
	if rows, _ = s.StatsOverview(ctx, timeMinAgo(60), "gitea", "acme", "nope"); len(rows) != 0 {
		t.Fatalf("过滤不命中应 0 行，got %d", len(rows))
	}
}
