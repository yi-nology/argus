// budget_reserve_test.go —— 预算预留账本（#32）：执行前预留/核销、并发不超卖、
// 幂等重入、崩溃释放（租约过期 + 心跳复位）三条路径。

package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// monthStart 本月 1 日（预算窗口起点，与 runner 闸同口径）。
func monthStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
}

// reserveIn 造默认预留入参（gitea/acme/app，窗口=本月）。
func reserveIn(taskID string, taskBudget, quota, real int) ReserveInput {
	return ReserveInput{
		TaskID: taskID, AttemptID: "0",
		Platform: "gitea", Owner: "acme", Repo: "app",
		TaskBudget: taskBudget, QuotaBudget: quota, RealBudget: real,
		Since: monthStart(), TTL: time.Minute,
	}
}

// getReservation 取任务预留行（不存在返回 nil）。
func getReservation(t *testing.T, s *Store, taskID string) *BudgetReservation {
	t.Helper()
	var row BudgetReservation
	err := s.quietDB().Where("task_id = ?", taskID).First(&row).Error
	if err != nil {
		return nil
	}
	return &row
}

// TestReserveRepoBudget_WithinBudget 额度内预留成功：落 reserved 行、额度=
// min(任务预算, 剩余额度)、租约未过期（#32 验收③前半）。
func TestReserveRepoBudget_WithinBudget(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	res, err := s.ReserveRepoBudget(ctx, reserveIn("t1", 300, 1000, 1000))
	if err != nil {
		t.Fatalf("额度内预留应成功: %v", err)
	}
	if res.Verdict != ReserveOK {
		t.Fatalf("应 ReserveOK，got %v", res.Verdict)
	}
	row := getReservation(t, s, "t1")
	if row == nil {
		t.Fatal("应落预留行")
	}
	if row.Status != ReservationReserved || row.Tokens != 300 {
		t.Fatalf("预留行失真: status=%s tokens=%d", row.Status, row.Tokens)
	}
	if !row.ExpiresAt.After(time.Now()) {
		t.Fatalf("租约应未过期: %v", row.ExpiresAt)
	}
	held, err := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app")
	if err != nil || held != 300 {
		t.Fatalf("活跃预留合计应 300: held=%d err=%v", held, err)
	}
}

// TestReserveRepoBudget_QuotaExceeded 配额账剩余额度不足 → 启动前拒绝且不落
// 预留行（#32 验收③：新任务超限拒绝在启动前，非终态后）。
func TestReserveRepoBudget_QuotaExceeded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 前置：同仓 done 任务已耗尽配额（100/100）。
	tk := mkTask("t-done", nil, "sha1")
	tk.Status = StatusDone
	tk.TokensUsed = 100
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	res, err := s.ReserveRepoBudget(ctx, reserveIn("t-new", 300, 100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != ReserveQuotaExceeded {
		t.Fatalf("应 ReserveQuotaExceeded，got %v（used=%d）", res.Verdict, res.QuotaUsed)
	}
	if res.QuotaUsed != 100 {
		t.Fatalf("拒绝时已承诺额度应 100，got %d", res.QuotaUsed)
	}
	if row := getReservation(t, s, "t-new"); row != nil {
		t.Fatalf("拒绝不应落预留行: %+v", row)
	}
}

// TestReserveRepoBudget_RealExceeded 实销账（usage_event 逐调用）剩余额度
// 不足 → 拒绝：运行中任务的调用即时占用额度。
func TestReserveRepoBudget_RealExceeded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.RecordUsageEvent(ctx, mkEvent("t-run", "inv-1", "app", 100, 0)); err != nil {
		t.Fatal(err)
	}

	res, err := s.ReserveRepoBudget(ctx, reserveIn("t-new", 300, 0, 100))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != ReserveRealExceeded {
		t.Fatalf("应 ReserveRealExceeded，got %v（used=%d）", res.Verdict, res.RealUsed)
	}
	if res.RealUsed != 100 {
		t.Fatalf("实销已承诺应 100，got %d", res.RealUsed)
	}
	if row := getReservation(t, s, "t-new"); row != nil {
		t.Fatalf("拒绝不应落预留行: %+v", row)
	}
}

// TestReserveRepoBudget_HoldsBlockNextStart 在途预留计入已承诺额度：后启任务
// 的判定把先启任务的预留算进去、额度按剩余钳制——「多副本并发启动、终态前
// 谁都看不到谁」的超支窗口闭合（#32 验收③核心）。
func TestReserveRepoBudget_HoldsBlockNextStart(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// A：预算 400 内预留 300。
	if res, err := s.ReserveRepoBudget(ctx, reserveIn("tA", 300, 400, 0)); err != nil || res.Verdict != ReserveOK {
		t.Fatalf("A 应预留成功: %+v err=%v", res, err)
	}
	// B：已承诺 300（A 的预留），剩 100 → 额度钳到 100 放行。
	resB, err := s.ReserveRepoBudget(ctx, reserveIn("tB", 300, 400, 0))
	if err != nil || resB.Verdict != ReserveOK {
		t.Fatalf("B 应放行: %+v err=%v", resB, err)
	}
	if row := getReservation(t, s, "tB"); row == nil || row.Tokens != 100 {
		t.Fatalf("B 预留应按剩余额度钳到 100: %+v", row)
	}
	// C：已承诺 400/400 → 拒绝。
	resC, err := s.ReserveRepoBudget(ctx, reserveIn("tC", 300, 400, 0))
	if err != nil {
		t.Fatal(err)
	}
	if resC.Verdict != ReserveQuotaExceeded {
		t.Fatalf("C 应配额拒绝，got %v（used=%d）", resC.Verdict, resC.QuotaUsed)
	}
}

// TestReserveRepoBudget_ConcurrentNoOversell 并发预留不超卖（#32 验收③并发组）：
// 8 个任务并发抢 500 额度（各 100）→ 恰好 5 个成功，其余拒绝，活跃预留合计
// 不超过预算。
func TestReserveRepoBudget_ConcurrentNoOversell(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	okCount := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.ReserveRepoBudget(ctx, reserveIn(fmt.Sprintf("t-conc-%d", i), 100, 500, 500))
			errs[i] = err
			okCount[i] = err == nil && res.Verdict == ReserveOK
		}(i)
	}
	wg.Wait()

	ok := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("并发预留第 %d 个出错: %v", i, errs[i])
		}
		if okCount[i] {
			ok++
		}
	}
	if ok != 5 {
		t.Fatalf("500 额度 / 100 每单 → 应恰好 5 个成功，got %d", ok)
	}
	held, err := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app")
	if err != nil {
		t.Fatal(err)
	}
	if held > 500 {
		t.Fatalf("活跃预留合计 %d 不得超过预算 500", held)
	}
	if held != 500 {
		t.Fatalf("活跃预留合计应恰为 500，got %d", held)
	}
}

// TestReserveRepoBudget_RefreshSameRow 重试重入（幂等）：同任务二次预留原地
// 刷新（一任务一行），attempt/额度更新、不叠加第二份预留。
func TestReserveRepoBudget_RefreshSameRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.ReserveRepoBudget(ctx, reserveIn("t1", 100, 1000, 1000)); err != nil {
		t.Fatal(err)
	}
	in := reserveIn("t1", 150, 1000, 1000)
	in.AttemptID = "1"
	res, err := s.ReserveRepoBudget(ctx, in)
	if err != nil || res.Verdict != ReserveOK {
		t.Fatalf("重入应成功: %+v err=%v", res, err)
	}

	var rows []BudgetReservation
	if err := s.quietDB().Where("task_id = ?", "t1").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("同任务应只有一行预留，got %d", len(rows))
	}
	if rows[0].Tokens != 150 || rows[0].AttemptID != "1" {
		t.Fatalf("预留行未刷新: %+v", rows[0])
	}
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 150 {
		t.Fatalf("刷新后活跃预留应 150（不叠加），got %d", held)
	}
}

// TestReservationSettledOnFinish 任务终态核销（#32 验收③后半）：FinishTaskFenced
// 后预留置 settled，不再计入活跃预留。
func TestReservationSettledOnFinish(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	tk := mkTask("t1", nil, "sha1")
	tk.Status = StatusRunning
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveRepoBudget(ctx, reserveIn("t1", 200, 1000, 1000)); err != nil {
		t.Fatal(err)
	}
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 200 {
		t.Fatalf("终态前活跃预留应 200，got %d", held)
	}

	if _, err := s.FinishTaskFenced(ctx, "t1", StatusDone, "approve", "", "", "", 120, 0); err != nil {
		t.Fatal(err)
	}
	row := getReservation(t, s, "t1")
	if row == nil || row.Status != ReservationSettled {
		t.Fatalf("终态应核销预留: %+v", row)
	}
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 0 {
		t.Fatalf("核销后活跃预留应 0，got %d", held)
	}
}

// TestReservationReleasedOnReset 崩溃恢复（#32 验收④）：心跳死亡的 running
// 任务被复位时，其未核销预留就地释放——不等租约过期。
func TestReservationReleasedOnReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 心跳已死 1 分钟的 running 任务 + 其活跃预留。
	old := time.Now().Add(-time.Minute)
	tk := mkTask("t1", nil, "sha1")
	tk.Status = StatusRunning
	tk.HeartbeatAt = &old
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveRepoBudget(ctx, reserveIn("t1", 200, 1000, 1000)); err != nil {
		t.Fatal(err)
	}

	n, err := s.ResetRunningToPending(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应复位 1 个任务，got %d", n)
	}
	row := getReservation(t, s, "t1")
	if row == nil || row.Status != ReservationReleased {
		t.Fatalf("复位应释放预留: %+v", row)
	}
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 0 {
		t.Fatalf("释放后活跃预留应 0，got %d", held)
	}
}

// TestReservationExpiresByLease 租约过期兜底释放（#32 验收④）：过期预留
// 不计入活跃预留与额度判定；清扫将其置 released。
func TestReservationExpiresByLease(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.ReserveRepoBudget(ctx, reserveIn("t1", 200, 1000, 1000)); err != nil {
		t.Fatal(err)
	}
	// 把租约改到已过期（模拟持有进程死亡后租约超时）。
	if err := s.db.Model(&BudgetReservation{}).Where("task_id = ?", "t1").
		Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	// 过期即不占额度：新任务判定不受影响。
	if held, _ := s.RepoMonthlyReserved(ctx, "gitea", "acme", "app"); held != 0 {
		t.Fatalf("过期预留不应计入活跃预留，got %d", held)
	}
	res, err := s.ReserveRepoBudget(ctx, reserveIn("t2", 300, 1000, 1000))
	if err != nil || res.Verdict != ReserveOK {
		t.Fatalf("过期预留不应挡住新任务: %+v err=%v", res, err)
	}

	// 清扫：过期行置 released。
	n, err := s.ReleaseExpiredReservations(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应清扫 1 行过期预留，got %d", n)
	}
	if row := getReservation(t, s, "t1"); row == nil || row.Status != ReservationReleased {
		t.Fatalf("清扫后应 released: %+v", row)
	}
	// 清扫幂等：已释放行不再命中。
	if n, _ := s.ReleaseExpiredReservations(ctx, time.Now()); n != 0 {
		t.Fatalf("重复清扫应 0 行，got %d", n)
	}
}

// TestTouchHeartbeatsRenewsReservation 心跳续租（#32 验收④租约路径）：活跃
// 预留的 expires_at 随任务心跳推进——进程活着预留不掉，进程死心跳停预留到期。
func TestTouchHeartbeatsRenewsReservation(t *testing.T) {
	s := openTestStore(t)
	s.RunnerID = "runner-1"
	ctx := context.Background()

	tk := mkTask("t1", nil, "sha1")
	tk.Status = StatusRunning
	tk.RunnerID = "runner-1"
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	in := reserveIn("t1", 200, 1000, 1000)
	in.TTL = time.Minute
	if _, err := s.ReserveRepoBudget(ctx, in); err != nil {
		t.Fatal(err)
	}
	before := getReservation(t, s, "t1").ExpiresAt

	time.Sleep(1100 * time.Millisecond) // 跨秒推进，确保续期后时间戳可分辨
	if err := s.TouchHeartbeatsFor(ctx, []string{"t1"}); err != nil {
		t.Fatal(err)
	}
	after := getReservation(t, s, "t1").ExpiresAt
	if !after.After(before) {
		t.Fatalf("心跳应续租: before=%v after=%v", before, after)
	}
}
