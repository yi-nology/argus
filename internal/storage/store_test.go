package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"argus/internal/plugin"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkTask(id string, eventID *string, headSHA string) *Task {
	return &Task{
		ID: id, Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: headSHA, EventID: eventID, Status: StatusPending,
		CreatedAt: time.Now(),
	}
}

// 幂等：同 delivery ID 重发 → 命中既有任务。
func TestEventIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	delivery := "d-111"
	dup := delivery

	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t1", &dup, "sha-a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskByEventID(ctx, delivery)
	if err != nil || got.ID != "t1" {
		t.Fatalf("幂等查询失败: %v %+v", err, got)
	}
	// 再次插入同 event_id（绕过业务检查直接 create）应违反唯一约束
	if err := s.CreateTask(ctx, mkTask("t2", &delivery, "sha-b")); err == nil {
		t.Fatal("同 event_id 第二次插入应被唯一索引拒绝")
	}
}

// 同 head SHA 去重：last_head_sha 命中 / in-flight 命中。
func TestHeadSHADedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 场景 A：无任何历史 → 创建成功
	d1 := "d1"
	reason, existing, err := s.CreateTaskForWebhook(ctx, mkTask("t1", &d1, "sha-1"))
	if err != nil || reason != "" {
		t.Fatalf("首次创建不应去重: reason=%q err=%v existing=%q", reason, err, existing)
	}

	// 场景 B：同 SHA 任务 pending 中 → in_flight 去重
	d2 := "d2"
	reason, existing, err = s.CreateTaskForWebhook(ctx, mkTask("t2", &d2, "sha-1"))
	if err != nil || reason != "in_flight" || existing != "t1" {
		t.Fatalf("in_flight 去重失败: reason=%q existing=%q err=%v", reason, existing, err)
	}

	// 模拟 t1 完成（done）并更新 review_targets.last_head_sha
	_ = s.FinishTask(ctx, "t1", StatusDone, "approve", "report", "{}", "", 100, 42)
	if err := s.UpsertReviewTarget(ctx, "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}

	// 场景 C：last_head_sha == 本次 head → 去重
	d3 := "d3"
	reason, _, err = s.CreateTaskForWebhook(ctx, mkTask("t3", &d3, "sha-1"))
	if err != nil || reason != "last_head_sha" {
		t.Fatalf("last_head_sha 去重失败: reason=%q err=%v", reason, err)
	}

	// 场景 D：新 SHA → 正常创建
	d4 := "d4"
	reason, _, err = s.CreateTaskForWebhook(ctx, mkTask("t4", &d4, "sha-2"))
	if err != nil || reason != "" {
		t.Fatalf("新 SHA 不应去重: reason=%q err=%v", reason, err)
	}
}

// 缺 delivery header → event_id 为 NULL，多条 NULL 共存不冲突。
func TestNullEventId(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t1", nil, "sha-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTaskForWebhook(ctx, mkTask("t2", nil, "sha-2")); err != nil {
		t.Fatalf("多个 NULL event_id 应共存: %v", err)
	}
}

// worker 抢占：pending→running 原子；启动重置 running→pending。
func TestClaimAndReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateTask(ctx, mkTask("t1", nil, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, mkTask("t2", nil, "")); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.ClaimNextPending(ctx)
	if err != nil || !ok || task.ID != "t1" {
		t.Fatalf("抢占顺序错误: %v %v %+v", ok, err, task)
	}
	if task.Status != StatusRunning {
		t.Fatalf("抢占后应 running: %s", task.Status)
	}
	// 重置（模拟启动恢复）
	n, err := s.ResetRunningToPending(ctx)
	if err != nil || n != 1 {
		t.Fatalf("重置失败: n=%d err=%v", n, err)
	}
}

// staleness 单条件：存在更晚创建的非 failed 任务 ⇒ stale。
func TestStaleness(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("t1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // 保证 created_at 可区分
	t2 := mkTask("t2", nil, "sha-b")
	if err := s.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}

	// t1（更早）：存在更晚的 t2（pending）→ stale
	stale, err := s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || !stale {
		t.Fatalf("t1 应 stale: %v %v", stale, err)
	}
	// t2（最晚）：无更晚任务 → 非 stale
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t2", t2.CreatedAt)
	if err != nil || stale {
		t.Fatalf("t2 不应 stale: %v %v", stale, err)
	}
	// 更晚的任务 failed 时不构成 staleness
	_ = s.FinishTask(ctx, "t2", StatusFailed, "", "", "", "boom", 0, 0)
	t2AfterFail, _ := s.GetTask(ctx, "t2")
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || stale || t2AfterFail.Status != StatusFailed {
		t.Fatalf("failed 任务不应构成 staleness: stale=%v err=%v", stale, err)
	}
}

// 指纹跨轮次：更新而非新增；suppressed 保留并回报。
func TestPrintUpsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := plugin.Finding{File: "a.go", Line: 10, Severity: "high", Comment: "SQL 注入"}
	now := time.Now()

	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f, "t1", now)
	if err != nil || suppressed {
		t.Fatalf("首次入库: suppressed=%v err=%v", suppressed, err)
	}
	f2 := plugin.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "SQL 注入"}
	suppressed, err = s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f2, "t2", now)
	if err != nil || suppressed {
		t.Fatalf("二次应为 open: %v %v", suppressed, err)
	}
	prints, err := s.ListFindingPrints(ctx, "gitea", "acme", "app", "7")
	if err != nil || len(prints) != 1 {
		t.Fatalf("同指纹应只有一条: %v %d", err, len(prints))
	}
	p := prints[0]
	if p.TimesSeen != 2 || p.Line != 11 || p.LastTaskID != "t2" {
		t.Fatalf("跨轮次更新错误: seen=%d line=%d last=%s", p.TimesSeen, p.Line, p.LastTaskID)
	}
	// 标记 suppressed 后，新轮次保持抑制状态
	if err := s.db.WithContext(ctx).Model(&FindingPrint{}).Where("print = ?", "fp1").
		Update("status", PrintSuppressed).Error; err != nil {
		t.Fatal(err)
	}
	suppressed, err = s.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp1", f, "t3", now)
	if err != nil || !suppressed {
		t.Fatalf("suppressed 应保持: %v %v", suppressed, err)
	}
}

// 保留策略：只清已终结的超期任务，pending/running 不动。
func TestPruneTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	for _, tc := range []*Task{
		{ID: "old-done", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "1",
			Status: StatusDone, CreatedAt: old},
		{ID: "old-failed", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "2",
			Status: StatusFailed, CreatedAt: old},
		{ID: "new-done", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "3",
			Status: StatusDone, CreatedAt: time.Now()},
		{ID: "old-pending", Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r", Number: "4",
			Status: StatusPending, CreatedAt: old},
	} {
		if err := s.CreateTask(ctx, tc); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneTasks(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("应删除 2 条超期已终结任务，got n=%d err=%v", n, err)
	}
	if _, err := s.GetTask(ctx, "old-pending"); err != nil {
		t.Fatal("pending 不应被清理")
	}
	if _, err := s.GetTask(ctx, "new-done"); err != nil {
		t.Fatal("未过期任务不应被清理")
	}
	p, r, err := s.TaskCounts(ctx)
	if err != nil || p != 1 || r != 0 {
		t.Fatalf("TaskCounts 错误: pending=%d running=%d err=%v", p, r, err)
	}
}
