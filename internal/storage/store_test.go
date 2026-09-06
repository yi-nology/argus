package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"argus/internal/plugin"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if mysqlDSN := os.Getenv("ARGUS_TEST_MYSQL_DSN"); mysqlDSN != "" {
		dsn = setupTestMySQL(t, mysqlDSN)
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// setupTestMySQL creates an isolated test database from the base DSN.
// The base DSN should point to the server without a specific database,
// e.g. "root:test@tcp(localhost:3306)/argus_test?parseTime=true".
// The dbname portion is used as a prefix; a unique suffix is appended per test.
func setupTestMySQL(t *testing.T, baseDSN string) string {
	t.Helper()
	// Extract the database name from the DSN to use as a prefix.
	// DSN format: user:pass@tcp(host:port)/dbname?params
	dbName := "argus_test"
	afterSlash := baseDSN
	if idx := strings.Index(baseDSN, "/"); idx >= 0 {
		afterSlash = baseDSN[idx+1:]
	}
	if qIdx := strings.Index(afterSlash, "?"); qIdx >= 0 {
		dbName = afterSlash[:qIdx]
	} else if afterSlash != "" {
		dbName = afterSlash
	}

	// Build a server-level DSN (no database) for admin operations.
	serverDSN := baseDSN
	if idx := strings.Index(baseDSN, "/"); idx >= 0 {
		q := ""
		if qIdx := strings.Index(baseDSN[idx:], "?"); qIdx >= 0 {
			q = baseDSN[idx+qIdx:]
		}
		serverDSN = baseDSN[:idx+1] + q
	}

	admin, err := sql.Open("mysql", serverDSN)
	if err != nil {
		t.Fatalf("openTestStore: sql.Open admin: %v", err)
	}
	if err := admin.Ping(); err != nil {
		t.Fatalf("openTestStore: admin ping: %v", err)
	}

	// Sanitize test name for use as a database name.
	safe := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_").Replace(t.Name())
	testDB := fmt.Sprintf("%s_%s", dbName, safe)
	if len(testDB) > 64 {
		testDB = testDB[:64]
	}

	_, err = admin.Exec("CREATE DATABASE `" + testDB + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	if err != nil {
		t.Fatalf("openTestStore: create db %q: %v", testDB, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + testDB + "`")
		_ = admin.Close()
	})

	// Return a DSN pointing at the new test database.
	// Replace the database name in the original DSN.
	testDSN := strings.Replace(baseDSN, dbName, testDB, 1)
	return testDSN
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
	// 重置（模拟启动恢复；staleAfter=0 ⇒ 心跳必过期）
	n, err := s.ResetRunningToPending(ctx, 0)
	if err != nil || n != 1 {
		t.Fatalf("重置失败: n=%d err=%v", n, err)
	}
}

// v3.0.2 回归：心跳新鲜的 running 任务不得被重置（多副本共库时滚动重启
// 不误伤其他实例在途任务的核心不变量）；心跳死亡（阈值归零）才重置。
func TestResetRunningRespectsHeartbeat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateTask(ctx, mkTask("t1", nil, "")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim 失败: ok=%v err=%v", ok, err)
	}
	// claim 即打心跳（刚刚）：90s 阈值下不得重置——丢了 heartbeat_at 条件会双执行
	if n, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil || n != 0 {
		t.Fatalf("心跳新鲜不应重置: n=%d err=%v", n, err)
	}
	// 阈值归零 ⇒ 心跳必过期：应重置（崩溃恢复路径）
	if n, err := s.ResetRunningToPending(ctx, 0); err != nil || n != 1 {
		t.Fatalf("过期心跳应重置: n=%d err=%v", n, err)
	}
}

// v3.0.2 回归：空 head 的手动任务不构成 staleness——此前"手动任务先压 stale 再失败"
// 会让 webhook 任务本轮审查永久静默丢失。
func TestStalenessIgnoresEmptyHeadTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("t1", nil, "sha-a") // webhook 任务（带 head）
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	manual := mkTask("t2", nil, "") // 手动任务（head 为空，执行期才回填）
	if err := s.CreateTask(ctx, manual); err != nil {
		t.Fatal(err)
	}
	// 更晚的手动任务 pending：不压制 t1
	stale, err := s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || stale {
		t.Fatalf("空 head 手动任务不应构成 staleness: stale=%v err=%v", stale, err)
	}
	// 对照：带 head 的更晚任务仍构成 staleness
	_ = s.CreateTask(ctx, mkTask("t3", nil, "sha-b"))
	stale, err = s.HasNewerNonFailedTask(ctx, "gitea", "acme", "app", "7", "t1", t1.CreatedAt)
	if err != nil || !stale {
		t.Fatalf("带 head 的更晚任务应构成 staleness: stale=%v err=%v", stale, err)
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

func TestLeaderLeaseAtomicity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 首次获取
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("首次获取应成功: %v %v", ok, err)
	}
	// 他人持有未过期 → 拒绝
	if ok, err := s.TryAcquire(ctx, "poller", "B", time.Minute); err != nil || ok {
		t.Fatalf("他人持有未过期应拒绝: %v %v", ok, err)
	}
	// 持有者自续约 → 成功
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("持有者续约应成功: %v %v", ok, err)
	}
	// 主动让位
	if err := s.Release(ctx, "poller", "A"); err != nil {
		t.Fatal(err)
	}
	// B 立即当选
	if ok, err := s.TryAcquire(ctx, "poller", "B", time.Minute); err != nil || !ok {
		t.Fatalf("让位后 B 应当选: %v %v", ok, err)
	}
	// 非持有者 Release 无效（B 仍持有）
	if err := s.Release(ctx, "poller", "A"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || ok {
		t.Fatalf("非持有者释放后仍应拒绝: %v %v", ok, err)
	}
	// 过期换主：直改 expires_at 模拟失联
	if err := s.db.Model(&LeaderLease{}).Where("lease_key = ?", "poller").
		Update("expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryAcquire(ctx, "poller", "A", time.Minute); err != nil || !ok {
		t.Fatalf("过期后应换主: %v %v", ok, err)
	}
}
