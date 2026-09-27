package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestQASessionRoundtripAndExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	key := "gitea:o/r#12"

	// 不存在 → nil
	if sess, err := s.GetQASession(ctx, key, time.Hour); err != nil || sess != nil {
		t.Fatalf("无会话应返回 nil: %v %v", sess, err)
	}
	// 保存 → 读回
	hist := []QATurn{{Question: "为什么这里没用锁?", Answer: "因为只读…", At: time.Now().UTC().Format(time.RFC3339)}}
	hj, _ := json.Marshal(hist)
	if err := s.SaveQASession(ctx, &QASession{SessionKey: key, HistoryJSON: string(hj)}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetQASession(ctx, key, time.Hour)
	if err != nil || sess == nil {
		t.Fatalf("应读到会话: %v %v", sess, err)
	}
	var back []QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &back); err != nil || len(back) != 1 {
		t.Fatalf("历史应往返一致: %v", err)
	}
	// 过期 → 视为无会话
	if err := s.db.Model(&QASession{}).Where("session_key = ?", key).
		Update("updated_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if sess, err := s.GetQASession(ctx, key, time.Hour); err != nil || sess != nil {
		t.Fatalf("过期会话应视为不存在: %v %v", sess, err)
	}
}

// 幂等直建：同 PR 已有 pending/running 任务时不再新建，返回既有任务；
// 无在飞任务时正常创建。TaskAPI 手工触发路径的防重复审查防线。
func TestCreateTaskDirectIfIdle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	existing, created, err := s.CreateTaskDirectIfIdle(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !created || existing != nil {
		t.Fatalf("first create: created=%v existing=%v, want true/nil", created, existing)
	}

	// pending 在飞 → 幂等命中
	second := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	existing, created, err = s.CreateTaskDirectIfIdle(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if created || existing == nil || existing.ID != first.ID {
		t.Fatalf("in-flight dedup: created=%v existing=%v, want false/%s", created, existing, first.ID)
	}

	// 不同 PR 不受影响
	other := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "2", Status: StatusPending, CreatedAt: time.Now(),
	}
	if _, created, err = s.CreateTaskDirectIfIdle(ctx, other); err != nil || !created {
		t.Fatalf("other PR should create: created=%v err=%v", created, err)
	}

	// 在飞任务完成后允许再次直建（手工强制重审是合法用途）
	if err := s.FinishTask(ctx, first.ID, StatusDone, "comment", "report", "{}", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTask(ctx, other.ID, StatusDone, "comment", "report", "{}", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	again := &Task{
		ID: NewTaskID(), Kind: "requirement_compliance", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1", Status: StatusPending, CreatedAt: time.Now(),
	}
	if _, created, err = s.CreateTaskDirectIfIdle(ctx, again); err != nil || !created {
		t.Fatalf("re-create after done: created=%v err=%v", created, err)
	}
}

// 收尾批修复回归：历史报告评论 ID 缓存——去重追加、封顶淘汰、新→旧读出。
func TestReviewTargetKnownCommentsRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, id := range []int64{101, 102, 103} {
		if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", id); err != nil {
			t.Fatal(err)
		}
	}
	// 重复追加去重
	if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", 102); err != nil {
		t.Fatal(err)
	}
	ids, err := s.KnownReviewTargetComments(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 103 || ids[2] != 101 {
		t.Fatalf("应 3 条且新→旧，got %v", ids)
	}
	// 无缓存仓库返回 nil 而非错误
	if ids, err := s.KnownReviewTargetComments(ctx, "gitea", "acme", "app", "8"); err != nil || ids != nil {
		t.Fatalf("无缓存应返回 nil/nil，got %v %v", ids, err)
	}
	// 非法 id 忽略
	if err := s.AppendReviewTargetComment(ctx, "gitea", "acme", "app", "7", 0); err != nil {
		t.Fatalf("id<=0 应静默忽略: %v", err)
	}
}

// v3.8.0 回归：FinishTaskFenced 归属 fencing——stale 重置后旧执行体（runner_id
// 不匹配或任务已易主）的终态写入被丢弃，新执行体的写入不受影响。
func TestFinishTaskFenced(t *testing.T) {
	s := openTestStore(t)
	s.RunnerID = "runner-a"
	ctx := context.Background()

	claim := func(id string) {
		if err := s.db.Model(&Task{}).Where("id = ?", id).
			Updates(map[string]any{"status": StatusRunning, "runner_id": s.RunnerID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t1 := mkTask("ft-1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	claim("ft-1")
	fenced, err := s.FinishTaskFenced(ctx, "ft-1", StatusDone, "comment", "r", "{}", "", 5, 0)
	if err != nil || fenced {
		t.Fatalf("归属匹配应成功写入: fenced=%v err=%v", fenced, err)
	}

	// 模拟 stale 重置后被 runner-b 认领：旧执行体 runner-a 的写入必须被拒
	if err := s.db.Model(&Task{}).Where("id = ?", "ft-1").
		Updates(map[string]any{"status": StatusRunning, "runner_id": "runner-b"}).Error; err != nil {
		t.Fatal(err)
	}
	fenced, err = s.FinishTaskFenced(ctx, "ft-1", StatusFailed, "", "", "", "zombie", 1, 0)
	if err != nil || !fenced {
		t.Fatalf("僵尸写入应被 fencing 拒绝: fenced=%v err=%v", fenced, err)
	}
	got, _ := s.GetTask(ctx, "ft-1")
	if got.Status != StatusRunning || got.FailReason != "" {
		t.Fatalf("僵尸写入不得改动任务: %+v", got)
	}

	// RunnerID 未装配（合成路径）：跳过 fencing，直接可写
	s2 := openTestStore(t)
	t2 := mkTask("ft-2", nil, "sha-b")
	if err := s2.CreateTask(ctx, t2); err != nil {
		t.Fatal(err)
	}
	if err := s2.FinishTask(ctx, "ft-2", StatusDone, "comment", "r", "{}", "", 0, 0); err != nil {
		t.Fatalf("未装配 RunnerID 时应跳过 fencing: %v", err)
	}
}

// v3.8.0 回归：failed 退避重试——failed 时 attempt+1 并写 next_retry_at；
// ResetFailedForRetry 只重置 attempt 未超限且已到期的任务。
func TestFailedRetryBackoff(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t1 := mkTask("fr-1", nil, "sha-a")
	if err := s.CreateTask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	// 模拟首次失败（RunnerID 空 → 无 fencing，同合成路径）
	if err := s.FinishTask(ctx, "fr-1", StatusFailed, "", "", "", "boom", 0, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, "fr-1")
	if got.Attempt != 1 || got.NextRetryAt == nil {
		t.Fatalf("失败应记 attempt+1 与退避时间: %+v", got)
	}
	// 退避未到期：不重置
	if n, err := s.ResetFailedForRetry(ctx, 2, got.NextRetryAt.Add(-time.Second)); err != nil || n != 0 {
		t.Fatalf("未到期不得重置: n=%d err=%v", n, err)
	}
	// 到期：重置回 pending（attempt 保留）
	if n, err := s.ResetFailedForRetry(ctx, 2, got.NextRetryAt.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("到期应重置: n=%d err=%v", n, err)
	}
	got, _ = s.GetTask(ctx, "fr-1")
	if got.Status != StatusPending || got.Attempt != 1 {
		t.Fatalf("重置后 pending 且 attempt 保留: %+v", got)
	}
	// 超限：attempt 达到 maxRetries 后不再重置
	_ = s.db.Model(&Task{}).Where("id = ?", "fr-1").Update("attempt", 2).Error
	_ = s.db.Model(&Task{}).Where("id = ?", "fr-1").Updates(map[string]any{
		"status": StatusFailed, "next_retry_at": time.Now().Add(-time.Minute)}).Error
	if n, err := s.ResetFailedForRetry(ctx, 2, time.Now()); err != nil || n != 0 {
		t.Fatalf("超限不得重置: n=%d err=%v", n, err)
	}
}

// v3.12.0 G2 断点续审：阶段产物 checkpoint 合并写入/回流。
func TestMergeTaskStageArtifacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := &Task{ID: "t-ckpt", Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7"}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	id := task.ID
	if err := s.MergeTaskStageArtifacts(ctx, id, map[string]string{"checkpoint_r1_spec": `{"source":"x"}`}); err != nil {
		t.Fatal(err)
	}
	// 第二次合并：保留既有键 + 覆盖/新增
	if err := s.MergeTaskStageArtifacts(ctx, id, map[string]string{
		"checkpoint_r1_spec":    `{"source":"y"}`,
		"checkpoint_r2_summary": "新增了登录",
	}); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(task.StageArtifacts), &got); err != nil {
		t.Fatal(err)
	}
	if got["checkpoint_r1_spec"] != `{"source":"y"}` || got["checkpoint_r2_summary"] != "新增了登录" {
		t.Fatalf("合并结果不符: %+v", got)
	}
}

// MarkReviewPosted + Task.ReviewPosted 回读（v3.36.0 poster 库标记短路的数据面）。
func TestMarkReviewPosted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tk := &Task{ID: "t-rp", Kind: "review", Platform: "gitea", Owner: "acme", Repo: "app", Number: "1", Status: "running"}
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "t-rp")
	if err != nil || got.ReviewPosted {
		t.Fatalf("初始应为 false，err=%v posted=%v", err, got.ReviewPosted)
	}
	if err := s.MarkReviewPosted(ctx, "t-rp"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTask(ctx, "t-rp")
	if err != nil || !got.ReviewPosted {
		t.Fatalf("标记后应为 true，err=%v posted=%v", err, got.ReviewPosted)
	}
}

// 优先级队列（v3.40.0）：priority 大值先认领，同优先级保持 FIFO。
func TestClaimNextPendingPriority(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(id string, pri int, offset int) {
		tk := &Task{ID: id, Kind: "review", Platform: "gitea", Owner: "acme", Repo: "app",
			Number: "1", Status: StatusPending, Priority: pri,
			CreatedAt: time.Now().Add(time.Duration(offset) * time.Second)}
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	mk("t-low-poll", 0, 0)     // 更早创建
	mk("t-high-human", 10, 60) // 更晚创建但优先级高
	got, ok, err := s.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if got.ID != "t-high-human" {
		t.Fatalf("高优先级应先认领，got %s", got.ID)
	}
}
