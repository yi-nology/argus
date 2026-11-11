// heartbeat_scope_test.go —— TouchHeartbeatsFor（worker.ScopedHeartbeater）行为面：
// 续期集之外的心跳必须冻结，且冻结正是「心跳过期复位」恢复契约生效的前提
// （oagentkit/queue#42）。三方言矩阵走 openTestStore（ARGUS_TEST_*_DSN 复用）。

package storage

import (
	"context"
	"testing"
	"time"
)

// backdate 把任务心跳回拨 d（模拟续期缺席的时间流逝，避免真等 staleAfter）。
func backdate(t *testing.T, s *Store, d time.Duration, ids ...string) {
	t.Helper()
	err := s.withWrite(func() error {
		return s.db.WithContext(context.Background()).Model(&Task{}).
			Where("id IN ?", ids).
			Update("heartbeat_at", time.Now().Add(-d)).Error
	})
	if err != nil {
		t.Fatalf("回拨心跳: %v", err)
	}
}

// hbFresh 心跳是否新鲜（最近 30s 内被续过）。
func hbFresh(t *testing.T, s *Store, id string) bool {
	t.Helper()
	task, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if task.HeartbeatAt == nil {
		t.Fatalf("%s 心跳应非 NULL", id)
	}
	return task.HeartbeatAt.After(time.Now().Add(-30 * time.Second))
}

// TestTouchHeartbeatsForScoped 三重守卫逐条钉住（oagentkit/queue#42）：
// ① task_id IN (...) 是 scoped 修复的实质——集外行心跳冻结；
// ② runner_id 守卫——不得续期他实例的行；
// ③ status='running' 守卫——已终态的行不得被续回新鲜；
// ④ 空集 no-op——绝不退化成「无过滤条件」的盲续。
func TestTouchHeartbeatsForScoped(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	s.RunnerID = "runner-a"

	for _, id := range []string{"ta1", "ta2", "tb1"} {
		if err := s.CreateTask(ctx, mkTask(id, nil, "")); err != nil {
			t.Fatal(err)
		}
	}
	// ta1/ta2 归 runner-a
	for _, want := range []string{"ta1", "ta2"} {
		if task, ok, err := s.ClaimNextPending(ctx); err != nil || !ok || task.ID != want {
			t.Fatalf("claim = (%v, %v, %v), want %s", task, ok, err, want)
		}
	}
	// tb1 归 runner-b（同库另一实例身份）
	s.RunnerID = "runner-b"
	if task, ok, err := s.ClaimNextPending(ctx); err != nil || !ok || task.ID != "tb1" {
		t.Fatalf("claim = (%v, %v, %v), want tb1", task, ok, err)
	}
	s.RunnerID = "runner-a"

	// ---- ① task_id 守卫：只续期 ta1，ta2 必须冻结 ----
	backdate(t, s, time.Hour, "ta1", "ta2", "tb1")
	if err := s.TouchHeartbeatsFor(ctx, []string{"ta1"}); err != nil {
		t.Fatal(err)
	}
	if !hbFresh(t, s, "ta1") {
		t.Error("ta1 在续期集内，心跳应被刷新")
	}
	if hbFresh(t, s, "ta2") {
		t.Error("ta2 不在续期集内，心跳必须冻结——否则退出的任务永不复位（#42 根因）")
	}

	// ---- ② runner_id 守卫：runner-a 点名 tb1 也不得续到它 ----
	backdate(t, s, time.Hour, "tb1")
	if err := s.TouchHeartbeatsFor(ctx, []string{"tb1"}); err != nil {
		t.Fatal(err)
	}
	if hbFresh(t, s, "tb1") {
		t.Error("tb1 归 runner-b，runner-a 不得续期他实例的任务")
	}

	// ---- ③ status 守卫：已终态的行不得被续回新鲜 ----
	if err := s.FinishTask(ctx, "ta1", StatusDone, "", "", "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	backdate(t, s, time.Hour, "ta1")
	if err := s.TouchHeartbeatsFor(ctx, []string{"ta1"}); err != nil {
		t.Fatal(err)
	}
	if hbFresh(t, s, "ta1") {
		t.Error("ta1 已是 done，不得被续期")
	}

	// ---- ④ 空集：不报错、不动任何行 ----
	backdate(t, s, time.Hour, "ta2", "tb1")
	if err := s.TouchHeartbeatsFor(ctx, nil); err != nil {
		t.Fatalf("空集应直接返回: %v", err)
	}
	if err := s.TouchHeartbeatsFor(ctx, []string{}); err != nil {
		t.Fatalf("空切片应直接返回: %v", err)
	}
	if hbFresh(t, s, "ta2") || hbFresh(t, s, "tb1") {
		t.Error("空集不得续期任何行")
	}
}

// TestScopedHeartbeatUnblocksStaleRecovery 语义核对：盲续→scoped 切换后
// 「心跳复位僵尸任务」的恢复契约不变坏，且因果链钉死：
//  1. 盲续对照：TouchRunningHeartbeats 把已退出任务也续活 → 复位命中 0
//     （#42 缺陷本体：僵尸任务永久卡 running）；
//  2. scoped：只续在飞的 alive → gone 心跳冻结 → staleAfter 后复位命中 gone
//     （runner 崩溃/任务退出后心跳停走→复位生效），仍在飞的 alive 不被误复位，
//     复位出的 gone 可被再次领取。
func TestScopedHeartbeatUnblocksStaleRecovery(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	s.RunnerID = "runner-a"
	for _, id := range []string{"alive", "gone"} {
		if err := s.CreateTask(ctx, mkTask(id, nil, "")); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatal("应抢到 alive")
	}
	if _, ok, err := s.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatal("应抢到 gone")
	}

	// ---- ① 盲续对照：gone 已「退出」，盲续仍续活它，复位命中 0 ----
	backdate(t, s, time.Hour, "alive", "gone")
	if err := s.TouchRunningHeartbeats(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ResetRunningToPending(ctx, 90*time.Second); err != nil || n != 0 {
		t.Fatalf("盲续后复位应命中 0（僵尸被续活）: n=%d err=%v", n, err)
	}

	// ---- ② scoped：只续在飞的 alive，gone 冻结 → 复位命中 gone ----
	backdate(t, s, time.Hour, "alive", "gone")
	if err := s.TouchHeartbeatsFor(ctx, []string{"alive"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResetRunningToPending(ctx, 90*time.Second)
	if err != nil || n != 1 {
		t.Fatalf("冻结心跳的任务应被复位: n=%d err=%v", n, err)
	}
	// 对照组：在飞的 alive 心跳新鲜，不得被复位
	if task, err := s.GetTask(ctx, "alive"); err != nil || task.Status != StatusRunning {
		t.Errorf("alive 仍在飞，不应被复位: status=%v err=%v", task.Status, err)
	}
	// 复位的 gone 回 pending，可再次被领取——崩溃恢复闭环
	if task, ok, err := s.ClaimNextPending(ctx); err != nil || !ok || task.ID != "gone" {
		t.Errorf("re-claim = (%v, %v, %v), want (gone, true, nil)", task, ok, err)
	}
}

// 装配纪律负控：RunnerID 未装配时 scoped 续期同样显式拒绝（先于空集早退）。
func TestTouchHeartbeatsForRejectsUnassembledRunnerID(t *testing.T) {
	s := openTestStore(t) // 不赋 RunnerID
	if err := s.TouchHeartbeatsFor(context.Background(), []string{"t1"}); err == nil {
		t.Error("touch-for without RunnerID should fail")
	}
	if err := s.TouchHeartbeatsFor(context.Background(), nil); err == nil {
		t.Error("touch-for(nil) without RunnerID should fail")
	}
}
