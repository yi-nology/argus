// event_outbox_test.go —— 事件 Outbox 回归（P1-05，#10 / P2-03，#14 对账面）。
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/progress"
)

// TestOutboxAppendListPrune 落表/游标分页/基线/保留期清理闭环。
func TestOutboxAppendListPrune(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if maxID, err := s.MaxOutboxID(ctx); err != nil || maxID != 0 {
		t.Fatalf("空表基线应 0: maxID=%d err=%v", maxID, err)
	}
	for i := range 3 {
		ev := progress.Event{ID: progress.NewEventID(), Type: progress.EventTaskCreated,
			TaskID: "t1", Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}
		_ = i
		if err := s.AppendOutboxEvent(ctx, ev); err != nil {
			t.Fatalf("AppendOutboxEvent: %v", err)
		}
	}
	rows, err := s.ListOutboxEvents(ctx, 0, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("分页 limit=2 应取 2 行: n=%d err=%v", len(rows), err)
	}
	rows2, err := s.ListOutboxEvents(ctx, rows[1].ID, 200)
	if err != nil || len(rows2) != 1 {
		t.Fatalf("游标续读应剩 1 行: n=%d err=%v", len(rows2), err)
	}
	if rows[0].Event.TaskID != "t1" || rows[0].Event.Platform != "gitea" {
		t.Fatalf("载荷解析失真: %+v", rows[0].Event)
	}
	maxID, err := s.MaxOutboxID(ctx)
	if err != nil || maxID != rows2[0].ID {
		t.Fatalf("MaxOutboxID 应为最后一行: %d vs %d", maxID, rows2[0].ID)
	}
	if n, err := s.PruneOutboxEvents(ctx, time.Now().Add(time.Hour)); err != nil || n != 3 {
		t.Fatalf("保留期清理: n=%d err=%v", n, err)
	}
}

// TestOutOfSyncDeliveries 副作用对账（P2-03，#14）：回帖成功但任务 failed、
// 任务 done 但回帖缺失两形态可检出；正常形态零检出。
func TestOutOfSyncDeliveries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mkTask := func(id, status string, summaryID int64) {
		task := NewTask("code_review", "gitea", "o", "recon", "1")
		task.ID = id
		task.HeadSHA = id
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FinishTaskFenced(ctx, id, status, "", "", "", "", 0, summaryID); err != nil {
			t.Fatal(err)
		}
	}
	mkTask("t-ok", StatusDone, 100)         // done + 回帖：一致
	mkTask("t-mismatch", StatusFailed, 101) // failed + 回帖：不一致①
	mkTask("t-noreply", StatusDone, 0)      // done 无回帖：不一致②

	pbf, dwr, err := s.OutOfSyncDeliveries(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if pbf != 1 || dwr != 1 {
		t.Fatalf("对账检出失真: postedButFailed=%d doneWithoutReply=%d（期望 1/1）", pbf, dwr)
	}
}

// TestCommentDeliveryOutcome 投递账本终态回填（P2-03，#14）。
func TestCommentDeliveryOutcome(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if fresh, err := s.MarkCommentDelivery(ctx, "d-1", time.Now()); err != nil || !fresh {
		t.Fatalf("首投递应 fresh: %v %v", fresh, err)
	}
	if err := s.MarkCommentDeliveryOutcome(ctx, "d-1", "ok", 826, "o/r#1"); err != nil {
		t.Fatalf("终态回填: %v", err)
	}
	var row CommentDelivery
	if err := s.db.Where("delivery_id = ?", "d-1").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ReplyStatus != "ok" || row.CommentID != 826 || row.Ref != "o/r#1" {
		t.Fatalf("账本三分离列失真: %+v", row)
	}
	// 空投递 ID 安全跳过（delivery header 缺失形态）
	if err := s.MarkCommentDeliveryOutcome(ctx, "", "ok", 0, ""); err != nil {
		t.Fatalf("空 deliveryID 应跳过: %v", err)
	}
}

// TestDualLedgerQueries 双账本口径（P1-06 #11 → #32 逐调用升级）：配额账只计
// done（任务行口径不回退）；真实消费账 = usage_event 逐调用实销——失败/运行中
// 任务的调用照计，不再等任务终态聚合。
func TestDualLedgerQueries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(id, status string, tokens int) {
		task := NewTask("code_review", "gitea", "o", "ledger", "1")
		task.ID = id
		task.HeadSHA = id
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		// 逐调用埋点（#32）：实销来自 usage_event，不依赖任务行终态。
		if tokens > 0 {
			if err := s.RecordUsageEvent(ctx, &UsageEvent{
				TaskID: id, AttemptID: "0", InvocationID: "inv-" + id,
				Platform: "gitea", Owner: "o", Repo: "ledger",
				Model: "m", InputTokens: tokens, Status: UsageSettled,
				OccurredAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.FinishTaskFenced(ctx, id, status, "", "", "", "", tokens, 0); err != nil {
			t.Fatal(err)
		}
	}
	mk("t-done", StatusDone, 100)
	mk("t-failed", StatusFailed, 30)
	mk("t-stale", StatusStale, 20)
	// 运行中任务（未终态）的调用同样入账——#32 缺口「运行中消费不在任一账本」。
	running := NewTask("code_review", "gitea", "o", "ledger", "2")
	running.ID = "t-running"
	running.HeadSHA = "t-running"
	running.Status = StatusRunning
	if err := s.CreateTask(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsageEvent(ctx, &UsageEvent{
		TaskID: "t-running", AttemptID: "0", InvocationID: "inv-running",
		Platform: "gitea", Owner: "o", Repo: "ledger",
		Model: "m", InputTokens: 40, Status: UsageSettled,
		OccurredAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-time.Hour)
	quota, err := s.RepoMonthlyTokens(ctx, "gitea", "o", "ledger", since)
	if err != nil || quota != 100 {
		t.Fatalf("配额账（done 口径）= %d err=%v（期望 100，语义不回退）", quota, err)
	}
	real, err := s.RepoMonthlyTokensReal(ctx, "gitea", "o", "ledger", since)
	if err != nil || real != 190 {
		t.Fatalf("实销 = %d err=%v（期望 190=100+30+20+运行中 40）", real, err)
	}
	repos, err := s.MonthlyTokensByRepoReal(ctx, since)
	if err != nil || len(repos) != 1 || repos[0].Tokens != 190 {
		t.Fatalf("实销聚合: %+v err=%v", repos, err)
	}
}
