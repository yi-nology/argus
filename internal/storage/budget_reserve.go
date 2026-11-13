// budget_reserve.go —— 预算预留账本（#32）：执行前预留 + 调用后核销。
// 一任务一预留行（task_id 唯一，重试原地刷新），status 生命周期：
// reserved（活跃预留，计入已承诺额度）→ settled（任务终态核销）/
// released（崩溃/心跳死亡/过期释放）。expires_at 为租约到期时间——活跃性由
// 心跳续期（TouchHeartbeatsFor），到期未续 = 持有进程已死，由清扫与查询侧
// 双重兜底释放（#32 验收④）。
//
// 额度判定（#32 验收③）：check-then-insert 的跨副本原子性由 leader_leases
// 短租约串行（与迁移租约同机制，key 按 仓×月 切分）——多副本同时启动任务时
// 「都看到 used=0 都放行」的超支窗口在预留临界区内闭合。判定口径：
//
//	配额账已承诺 = done 任务行聚合 + 他任务活跃预留
//	实销已承诺   = usage_event 实销（排除带活跃预留的任务，其潜在消耗已由
//	               预留额度代表，避免同笔双计）+ 他任务活跃预留
//
// 任一账本剩余额度 ≤0 → 拒绝（不落预留行）；通过 → 预留额度 =
// min(任务预算, 各自剩余额度)，对两本账各记一笔承诺。

package storage

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

// reservationTTL 预留租约时长：心跳（缺省 15s 周期）每跳续期一次，5min =
// 20× 心跳周期余量；进程死亡后心跳停 → 预留最迟 5min 过期（正常路径由
// ResetRunningToPending 心跳复位就地释放，不等租约到期）。
const reservationTTL = 5 * time.Minute

// reservationLeaseTTL 预留临界区租约时长：判定+落行毫秒级，10s 覆盖崩溃
// 残留（到期可被他副本接管）。
const reservationLeaseTTL = 10 * time.Second

// reservationLeaseWait 预留租约等待上界：临界区毫秒级，2s 足够排队；
// 超时上抛由调用方 fail-open 放行（沿用旧预算闸「查询失败放行」语义）。
const reservationLeaseWait = 2 * time.Second

// BudgetReservation budget_reservations 表：任务对本仓月度预算的执行前预留。
type BudgetReservation struct {
	ID uint `gorm:"column:id;primaryKey"`

	// TaskID 一任务一行（uniqueIndex）：重试（attempt 递增）原地刷新而非追加，
	// 防同一任务的多次尝试叠加多份预留。
	TaskID    string `gorm:"column:task_id;size:40;not null;uniqueIndex:idx_bres_task"`
	AttemptID string `gorm:"column:attempt_id;size:32"`

	// platform/owner/repo 冗余自 tasks：预算判定按仓聚合，不 JOIN（同 usage_event）。
	Platform string `gorm:"column:platform;size:16;not null;index:idx_bres_repo,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_bres_repo,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_bres_repo,priority:3"`

	// Tokens 预留额度（= min(任务预算, 判定时剩余额度)）：配额账与真实消费账
	// 共用同一条预留（对两本账各记一笔承诺额度）。
	Tokens int `gorm:"column:tokens"`

	// Status 预留状态（词表见 Reservation* 常量）。
	Status string `gorm:"column:status;size:16;not null;default:reserved;index"`
	// ExpiresAt 租约到期：活跃预留由心跳续期；到期未续由清扫置 released，
	// 查询侧亦按 expires_at > now 过滤（崩溃恢复双兜底）。
	ExpiresAt time.Time `gorm:"column:expires_at;index"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (BudgetReservation) TableName() string { return "budget_reservations" }

// 预留状态词表。
const (
	// ReservationReserved 活跃预留（计入已承诺额度）。
	ReservationReserved = "reserved"
	// ReservationSettled 任务终态核销（预留生命周期正常结束——任务已收口，
	// 实销由 usage_event 逐调用在账，预留不再代表未来消耗）。
	ReservationSettled = "settled"
	// ReservationReleased 预留释放（心跳复位/租约过期，#32 验收④崩溃恢复）。
	ReservationReleased = "released"
)

// ReserveVerdict 执行前预留判定结果。
type ReserveVerdict int

const (
	// ReserveOK 额度内，预留已落（或无需预留：双账本全关/任务预算非正）。
	ReserveOK ReserveVerdict = iota
	// ReserveQuotaExceeded 配额账（done 口径 + 预留）剩余额度不足。
	ReserveQuotaExceeded
	// ReserveRealExceeded 真实消费账（usage_event 实销 + 预留）剩余额度不足。
	ReserveRealExceeded
)

// ReserveInput 执行前预留入参。
type ReserveInput struct {
	TaskID      string
	AttemptID   string
	Platform    string
	Owner       string
	Repo        string
	TaskBudget  int           // 本任务预算（预留额度上界；≤0 = 无界任务，不占预留）
	QuotaBudget int           // 配额账月度上限（0 = 关）
	RealBudget  int           // 真实消费账月度上限（0 = 关）
	Since       time.Time     // 月度窗口起点（配额/实销聚合口径）
	TTL         time.Duration // 预留租约时长（0 = reservationTTL）
}

// ReserveResult 预留判定输出：Used 字段为拒绝时的已承诺额度（失败播报用）。
type ReserveResult struct {
	Verdict   ReserveVerdict
	QuotaUsed int64 // 配额账已承诺（done 聚合 + 活跃预留）
	RealUsed  int64 // 真实账已承诺（实销 + 活跃预留）
}

// ReserveRepoBudget 执行前预留（#32 验收③）：双账本额度判定 + 落预留行在同一
// 临界区（leader_leases 短租约串行，跨副本原子）。拒绝 = 不落预留行、由调用方
// 走失败终态播报；双账本全关或任务预算非正时零开销直通（不落行）。
// 存储/租约故障上抛——调用方沿用旧闸「查失败放行」的 fail-open 语义。
func (s *Store) ReserveRepoBudget(ctx context.Context, in ReserveInput) (ReserveResult, error) {
	var out ReserveResult
	if in.QuotaBudget <= 0 && in.RealBudget <= 0 {
		return out, nil // 双账本全关：无额度可超，也无须占承诺
	}
	if in.TaskID == "" || in.Platform == "" {
		return out, errors.New("storage: 预算预留缺少 task_id/仓库坐标")
	}
	ttl := in.TTL
	if ttl == 0 {
		ttl = reservationTTL
	}
	// 短租约串行：check-then-insert 的跨副本互斥（同迁移租约机制，key 按
	// 仓×月切分——同仓同月的启动判定排队，跨仓互不相干）。
	key := reservationLeaseKey(in.Platform, in.Owner, in.Repo, in.Since)
	holder := s.instanceID() + ":reserve:" + in.TaskID
	release, err := s.acquireReservationLease(ctx, key, holder)
	if err != nil {
		return out, err
	}
	defer release()

	now := time.Now()
	// 他任务活跃预留（不含本任务——重试重入时旧行将被刷新，不自我双计）。
	held, err := s.activeHeldTokens(ctx, in.Platform, in.Owner, in.Repo, now, in.TaskID)
	if err != nil {
		return out, err
	}
	// 配额账：done 任务行聚合 + 活跃预留（在途任务终态前不落 done，预留补位）。
	if in.QuotaBudget > 0 {
		done, err := s.RepoMonthlyTokens(ctx, in.Platform, in.Owner, in.Repo, in.Since)
		if err != nil {
			return out, err
		}
		out.QuotaUsed = done + held
		if out.QuotaUsed >= int64(in.QuotaBudget) {
			out.Verdict = ReserveQuotaExceeded
			return out, nil
		}
	}
	// 真实账：usage_event 实销（排除带活跃预留的任务——其潜在消耗已由预留
	// 额度代表）+ 活跃预留。运行中任务的调用即时占用额度。
	if in.RealBudget > 0 {
		settled, err := s.settledNonHeldTokens(ctx, in.Platform, in.Owner, in.Repo, in.Since, now, in.TaskID)
		if err != nil {
			return out, err
		}
		out.RealUsed = settled + held
		if out.RealUsed >= int64(in.RealBudget) {
			out.Verdict = ReserveRealExceeded
			return out, nil
		}
	}
	// 额度判定通过 → 预留额度 = min(任务预算, 各自剩余额度)。
	hold := in.TaskBudget
	if in.QuotaBudget > 0 {
		if r := in.QuotaBudget - int(out.QuotaUsed); r < hold {
			hold = r
		}
	}
	if in.RealBudget > 0 {
		if r := in.RealBudget - int(out.RealUsed); r < hold {
			hold = r
		}
	}
	if hold <= 0 {
		return out, nil // 任务预算非正（无界任务）：不占预留，行为同旧闸直通
	}
	if err := s.upsertReservation(ctx, in, hold, now.Add(ttl), now); err != nil {
		return out, err
	}
	return out, nil
}

// activeHeldTokens 本仓他任务活跃预留合计（status=reserved 且租约未过期）。
func (s *Store) activeHeldTokens(ctx context.Context, platform, owner, repo string, now time.Time, excludeTaskID string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND expires_at > ? AND task_id <> ?",
			platform, owner, repo, ReservationReserved, now, excludeTaskID).
		Select("COALESCE(SUM(tokens),0)").Scan(&total).Error
	return total, err
}

// settledNonHeldTokens 实销账已承诺的实销部分：usage_event 实销中「无活跃
// 预留」的任务之和——带活跃预留的任务其潜在消耗由预留额度代表，避免同笔双计。
func (s *Store) settledNonHeldTokens(ctx context.Context, platform, owner, repo string,
	since, now time.Time, excludeTaskID string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND occurred_at >= ? "+
			"AND task_id NOT IN (SELECT task_id FROM budget_reservations "+
			"WHERE status = ? AND expires_at > ? AND task_id <> ?)",
			platform, owner, repo, UsageSettled, since,
			ReservationReserved, now, excludeTaskID).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&total).Error
	return total, err
}

// upsertReservation 落/刷新预留行（一任务一行）：先条件更新，无行则插入、
// 撞唯一键（同任务他路径抢先）回退再更新收敛——与 leader_leases 的
// TryAcquire 同形态（MySQL UPDATE 可能回报 0 变更行，幂等重放走此回退）。
func (s *Store) upsertReservation(ctx context.Context, in ReserveInput, tokens int, expires, now time.Time) error {
	updates := map[string]any{
		"attempt_id": in.AttemptID, "platform": in.Platform, "owner": in.Owner,
		"repo": in.Repo, "tokens": tokens, "status": ReservationReserved,
		"expires_at": expires, "updated_at": now,
	}
	res := s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id = ?", in.TaskID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	err := s.quietDB().WithContext(ctx).Create(&BudgetReservation{
		TaskID: in.TaskID, AttemptID: in.AttemptID,
		Platform: in.Platform, Owner: in.Owner, Repo: in.Repo,
		Tokens: tokens, Status: ReservationReserved,
		ExpiresAt: expires, CreatedAt: now, UpdatedAt: now,
	}).Error
	if err == nil {
		return nil
	}
	if !isDuplicateKeyErr(err) {
		return err
	}
	// 撞唯一键：行已存在（并发/重入），回退条件更新收敛。
	res = s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id = ?", in.TaskID).Updates(updates)
	return res.Error
}

// acquireReservationLease 预留短租约：拿不到 = 他副本正在判定（临界区毫秒级），
// 20ms 轮询至 2s 上界；超时 fail-closed 上抛（调用方 fail-open 放行）。
func (s *Store) acquireReservationLease(ctx context.Context, key, holder string) (func(), error) {
	deadline := time.Now().Add(reservationLeaseWait)
	for {
		ok, err := s.TryAcquire(ctx, key, holder, reservationLeaseTTL)
		if err != nil {
			return nil, fmt.Errorf("storage: 预算预留租约获取失败: %w", err)
		}
		if ok {
			return func() { _ = s.Release(context.Background(), key, holder) }, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("storage: 等待预算预留租约超时（他副本判定中）: %s", key)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// reservationLeaseKey 预留租约 key：仓×月切分，fnv64a 哈希压到 128 字符内
// （owner/repo 各可达 128，裸拼会超 leader_leases.lease_key 上限）。哈希碰撞
// 只会让两个无关仓的预留判定串行（额外排队，不影响正确性）。
func reservationLeaseKey(platform, owner, repo string, since time.Time) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%s/%s:%s", platform, owner, repo, since.Format("2006-01"))
	return fmt.Sprintf("budget_reserve:%016x", h.Sum64())
}

// RepoMonthlyReserved 本仓当前活跃预留合计（/usage/monthly 预留展示、预算闸
// 与测试断言共用口径）：status=reserved 且租约未过期。
func (s *Store) RepoMonthlyReserved(ctx context.Context, platform, owner, repo string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND expires_at > ?",
			platform, owner, repo, ReservationReserved, time.Now()).
		Select("COALESCE(SUM(tokens),0)").Scan(&total).Error
	return total, err
}

// MonthlyReservedByRepo 全部仓库活跃预留聚合（/usage/monthly 预留 vs 实销）。
func (s *Store) MonthlyReservedByRepo(ctx context.Context) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Select("platform, owner, repo, SUM(tokens) AS tokens, COUNT(DISTINCT task_id) AS tasks").
		Where("status = ? AND expires_at > ?", ReservationReserved, time.Now()).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// ReleaseExpiredReservations 过期预留清扫（#32 验收④租约/超时路径）：持有
// 进程死亡 → 心跳停 → expires_at 过期 → 置 released 归还额度。查询侧本就按
// expires_at > now 过期（自愈），本清扫只做状态收敛与观测；重复执行幂等。
func (s *Store) ReleaseExpiredReservations(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).Model(&BudgetReservation{}).
			Where("status = ? AND expires_at < ?", ReservationReserved, now).
			Updates(map[string]any{"status": ReservationReleased, "updated_at": now})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// releaseTaskReservations 任务维度的预留收口（供 FinishTaskFenced / 复位路径
// 在既有写门内调用——单条 UPDATE，不嵌套 withWrite）。to = settled（终态核销）
// 或 released（复位/崩溃释放）。失败只由调用方告警：预留过期兜底仍在
// （expires_at + 清扫），不阻断任务终态写入。
func (s *Store) releaseTaskReservations(ctx context.Context, taskIDs []string, to string, now time.Time) error {
	if len(taskIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id IN ? AND status = ?", taskIDs, ReservationReserved).
		Updates(map[string]any{"status": to, "updated_at": now}).Error
}

// renewTaskReservations 预留租约随心跳续期（供 TouchHeartbeatsFor 等在既有
// 写门内调用）：expires_at 与任务活性绑定——进程活着预留不掉，进程死心跳停
// 预留到期释放。
func (s *Store) renewTaskReservations(ctx context.Context, taskIDs []string, now time.Time) error {
	if len(taskIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id IN ? AND status = ?", taskIDs, ReservationReserved).
		Updates(map[string]any{
			"expires_at": now.Add(reservationTTL), "updated_at": now,
		}).Error
}
