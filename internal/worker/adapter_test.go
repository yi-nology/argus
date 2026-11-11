package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	queueworker "github.com/oagentkit/queue/worker"

	"github.com/yi-nology/argus/internal/storage"
)

// StoreQueue 必须实现 worker.ScopedHeartbeater——Pool 的心跳按在飞任务集续期
// 靠这条断言在编译期兜住（接口漂移在这里红，而不是生产里静默退化成盲续，
// 那时症状是「panic/报错退出的任务永久卡 running」，oagentkit/queue#42）。
var _ queueworker.ScopedHeartbeater = (*StoreQueue)(nil)

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.RunnerID = "test-runner" // 装配纪律：main 先赋 RunnerID 再启动 worker
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 装配纪律负控：RunnerID 未装配时心跳续期显式拒绝，不静默 no-op。
func TestHeartbeatsRejectUnassembledRunnerID(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q := &StoreQueue{Store: s}
	if err := q.TouchRunningHeartbeats(ctx); err == nil {
		t.Error("touch without RunnerID should fail")
	}
	// Scoped 续期同守装配纪律（RunnerID 守卫先于空集早退——续期无法归属即显式失败）
	if err := q.TouchHeartbeatsFor(ctx, []string{"t1"}); err == nil {
		t.Error("touch-for without RunnerID should fail")
	}
	if err := q.TouchHeartbeatsFor(ctx, nil); err == nil {
		t.Error("touch-for(nil) without RunnerID should fail")
	}
	// ResetRunningToPending 是 reaper 语义（收编任何实例的死任务），不设 RunnerID 守卫
	if _, err := q.ResetRunningToPending(ctx, 0); err != nil {
		t.Errorf("reset without RunnerID should work: %v", err)
	}
}

// 空队列：不占位、不报错。
func TestClaimNextPending_Empty(t *testing.T) {
	q := &StoreQueue{Store: openTestStore(t)}
	id, ok, err := q.ClaimNextPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || id != "" {
		t.Errorf("empty queue claim = (%q, %v), want (\"\", false)", id, ok)
	}
}

// 有 pending：返回 taskID 且只被领一次。
func TestClaimNextPending_ReturnsTaskIDOnce(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	task := &storage.Task{ID: storage.NewTaskID(), Status: "pending"}
	if err := q.Store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	id, ok, err := q.ClaimNextPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id != task.ID {
		t.Fatalf("claim = (%q, %v), want (%q, true)", id, ok, task.ID)
	}
	if id2, ok2, err2 := q.ClaimNextPending(ctx); err2 != nil || ok2 || id2 != "" {
		t.Errorf("second claim = (%q, %v, %v), want (\"\", false, nil)", id2, ok2, err2)
	}
}

// 心跳续期委托到 Store，running 任务续期不报错。
func TestTouchRunningHeartbeats(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	task := &storage.Task{ID: storage.NewTaskID(), Status: "pending"}
	if err := q.Store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := q.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatalf("touch: %v", err)
	}
}

// 心跳过期的 running 任务重置回 pending，可再次被抢占。
func TestResetRunningToPending(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	task := &storage.Task{ID: storage.NewTaskID(), Status: "pending"}
	if err := q.Store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// staleAfter=0：刚写的心跳也视为过期，立即重置
	n, err := q.ResetRunningToPending(ctx, 0)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n < 1 {
		t.Fatalf("reset count = %d, want >= 1", n)
	}
	if id, ok, err := q.ClaimNextPending(ctx); err != nil || !ok || id != task.ID {
		t.Errorf("re-claim = (%q, %v, %v), want (%q, true, nil)", id, ok, err, task.ID)
	}
	// staleAfter 很大：仍在跑的任务不被重置
	if err := q.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := q.ResetRunningToPending(ctx, time.Hour); err != nil || n != 0 {
		t.Errorf("reset(staleAfter=1h) = (%d, %v), want (0, nil)", n, err)
	}
}

// hbOf 读任务当前心跳（DB 回读，两侧同精度可比）。
func hbOf(t *testing.T, q *StoreQueue, id string) time.Time {
	t.Helper()
	task, err := q.Store.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if task.HeartbeatAt == nil {
		t.Fatalf("%s heartbeat should not be nil", id)
	}
	return *task.HeartbeatAt
}

// claimTwo 建两个 pending 并连续领走，返回领取顺序的 taskID。
func claimTwo(t *testing.T, q *StoreQueue) (string, string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{storage.NewTaskID(), storage.NewTaskID()} {
		if err := q.Store.CreateTask(ctx, &storage.Task{ID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	id1, ok, err := q.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("claim 1: ok=%v err=%v", ok, err)
	}
	id2, ok, err := q.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("claim 2: ok=%v err=%v", ok, err)
	}
	return id1, id2
}

// scoped 续期只动续期集内的在飞任务：集外 running 行心跳必须冻结
// （oagentkit/queue#42 的实质修复点——runTask 已退出的行不再被续活）。
func TestTouchHeartbeatsForScoped(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	alive, gone := claimTwo(t, q)
	hbAlive, hbGone := hbOf(t, q, alive), hbOf(t, q, gone)

	time.Sleep(200 * time.Millisecond) // 让续期后的时点可与领取时点区分
	if err := q.TouchHeartbeatsFor(ctx, []string{alive}); err != nil {
		t.Fatal(err)
	}
	if got := hbOf(t, q, alive); !got.After(hbAlive) {
		t.Errorf("alive 在续期集内，心跳应被刷新: %v !> %v", got, hbAlive)
	}
	if got := hbOf(t, q, gone); !got.Equal(hbGone) {
		t.Errorf("gone 不在续期集内，心跳必须冻结: %v != %v", got, hbGone)
	}
}

// 空集 = 不发语句：不得触碰任何行（绝不能退化成「无过滤条件」的盲续）。
func TestTouchHeartbeatsForEmptySetNoop(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	id, _ := claimTwo(t, q)
	hb := hbOf(t, q, id)
	for _, ids := range [][]string{nil, {}} {
		if err := q.TouchHeartbeatsFor(ctx, ids); err != nil {
			t.Fatalf("empty set should be a no-op: %v", err)
		}
	}
	if got := hbOf(t, q, id); !got.Equal(hb) {
		t.Errorf("空集不得续期任何行: %v != %v", got, hb)
	}
}

// 语义核对（盲续→scoped 的因果链钉死）：
//  1. 盲续 TouchRunningHeartbeats 把已退出任务（gone，模拟 runTask 返回后不再
//     被任何续期集包含）也续活 → ResetRunningToPending 永不命中（#42 缺陷本体）；
//  2. scoped 续期只点名在飞的 alive → gone 心跳冻结，staleAfter 后被复位重跑
//     （崩溃恢复契约生效），同时仍在飞的 alive 不被误复位。
func TestScopedHeartbeatUnblocksStaleRecovery(t *testing.T) {
	ctx := context.Background()
	q := &StoreQueue{Store: openTestStore(t)}
	alive, gone := claimTwo(t, q)

	// ---- ① 盲续对照：gone 已「退出」，盲续仍把它续活，复位命中 0 ----
	time.Sleep(200 * time.Millisecond)
	if err := q.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := q.ResetRunningToPending(ctx, 100*time.Millisecond); err != nil || n != 0 {
		t.Fatalf("盲续后复位应命中 0（僵尸被续活）: n=%d err=%v", n, err)
	}

	// ---- ② scoped：只续在飞的 alive，gone 心跳冻结 → 复位命中 gone ----
	time.Sleep(200 * time.Millisecond)
	if err := q.TouchHeartbeatsFor(ctx, []string{alive}); err != nil {
		t.Fatal(err)
	}
	n, err := q.ResetRunningToPending(ctx, 100*time.Millisecond)
	if err != nil || n != 1 {
		t.Fatalf("冻结心跳的任务应被复位: n=%d err=%v", n, err)
	}
	// 对照组：在飞的 alive 心跳新鲜，不得被复位
	if task, err := q.Store.GetTask(ctx, alive); err != nil || task.Status != "running" {
		t.Errorf("alive 仍在飞，不应被复位: status=%v err=%v", task.Status, err)
	}
	// 复位的 gone 回 pending，可再次被领取——崩溃恢复闭环
	if id, ok, err := q.ClaimNextPending(ctx); err != nil || !ok || id != gone {
		t.Errorf("re-claim = (%q, %v, %v), want (%q, true, nil)", id, ok, err, gone)
	}
}
