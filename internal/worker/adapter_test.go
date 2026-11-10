package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

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
