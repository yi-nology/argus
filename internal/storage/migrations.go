// migrations.go —— 一次性迁移的哨兵机制（v3.8.0）。
//
// 背景：本项目 schema 演进靠 GORM AutoMigrate（幂等增量），但"数据回写型"迁移
// （如指纹 v2 全表重算）不能每次启动都跑——大表全扫 + 半迁移态带病服务。
// SchemaMigration 表记录已应用的一次性迁移版本，fn 成功后才落 marker。
// 注意：marker 语义是"该库已迁移到该版本"；旧二进制接同库属于版本倒挂
// （<v3.7.0 读不懂 v2 指纹，历史抑制会全部失配复活），属 runbook 明令禁止的
// 回滚红线，代码侧无法两全。

package storage

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
	"gorm.io/gorm"
)

// SchemaMigration 一次性数据迁移的应用记录。
type SchemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey;size:64"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

// migrationLockTTL 迁移锁租期：覆盖重建型迁移执行上界（printalias 秒级~十秒级，
// 5min 余量充足）；续约（每 TTL/10）兜住长迁移，崩溃时锁按 TTL 自然过期。
const migrationLockTTL = 5 * time.Minute

// automigrateLeaseKey AutoMigrate/LONGTEXT 升级的迁移租约 key（P0-04，#5）：
// schema DDL 与数据回写迁移共用 leader_leases 机制，key 域区分。
const automigrateLeaseKey = "schema_migration:automigrate"

// migrationRenewalInterval 迁移租约续约间隔（缺省 TTL/10）。包级 var：fencing
// 注入测试缩短到毫秒级（生产恒缺省值，Open 无入口覆盖）。
var migrationRenewalInterval = migrationLockTTL / 10

// acquireMigrationLease 抢迁移租约，阻塞至持有或超时（P0-04，#5）：拿不到 =
// 他副本正在迁移（多副本并发首启），让位轮询等待——AutoMigrate 无 marker 可等，
// 只能等锁本身；超时 fail-closed 拒绝启动。返回释放函数。
func (s *Store) acquireMigrationLease(ctx context.Context, key string) (func(), error) {
	holder := s.instanceID() + ":migration:" + key
	deadline := time.Now().Add(migrationLockTTL + 2*time.Minute)
	for {
		ok, err := s.TryAcquire(ctx, key, holder, migrationLockTTL)
		if err != nil {
			return nil, fmt.Errorf("storage: 迁移租约 %s 获取失败: %w", key, err)
		}
		if ok {
			return func() { _ = s.Release(context.Background(), key, holder) }, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("storage: 等待迁移租约 %s 超时（他实例持有未释放，疑似迁移中崩溃）：请人工核查数据库后重启", key)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// awaitMigrationMarker 让位等待（评估 P0）：抢锁失败 = 他实例正在跑该迁移。
// 轮询 marker 出现（每 2s，上界 = TTL + 2min 余量）：等到 = 视为已应用返回 0；
// 超时 = fail-closed 报错拒绝启动（提示人工核查，不带病服务）；ctx 取消透传。
func (s *Store) awaitMigrationMarker(ctx context.Context, version string) (int, error) {
	deadline := time.Now().Add(migrationLockTTL + 2*time.Minute)
	var marker SchemaMigration
	for {
		err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error
		if err == nil {
			return 0, nil
		}
		if err != gorm.ErrRecordNotFound {
			return 0, err
		}
		if !time.Now().Before(deadline) {
			return 0, fmt.Errorf("等待迁移 %s 超时（他实例持有迁移锁但 marker 未落，疑似迁移中崩溃）：请人工核查 schema_migrations 与数据一致性后重启", version)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ensureMigrationApplied 幂等执行一次性迁移：marker 已存在 → 跳过（返回 0, nil）；
// fn 成功后才写 marker——失败不写，下次启动重试。
func (s *Store) ensureMigrationApplied(ctx context.Context, version string, fn func(ctx context.Context) (int, error)) (applied int, err error) {
	var marker SchemaMigration
	if err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	// 单飞锁（v5.4.8 多副本加固；v6.3.0 评估 P0 修复）：leader_leases 租约互斥——
	// 多副本（pg/MySQL 网络库）同时首启时防并发跑重建型迁移（printalias PR 域
	// 迁移是 DROP+CREATE，并发执行会互相踩）。
	//
	// ⚠️ 修复要点（外部评估实锤）：lease key 全实例固定，holder 必须唯一——
	// v6.3.0 前 holder = "schema_migration:"+version 全实例相同，TryAcquire 的
	// `holder = ? OR expires_at < ?` 条件在 holder 相同时让后启动实例直接把锁
	// 从执行方手里"续约"抢走 → 双实例并发首启同跑迁移（DDL 冲突/数据不一致）。
	// ① holder = instanceID()+":migration:"+version（key 与 holder 分离）。
	// ② 拿不到锁 = 他实例正在跑：让位等 marker 出现（等到=已应用；超时
	// fail-closed 拒绝启动——带半迁移态服务比启动失败更危险），不再静默返回 0
	// （v6.3.0 前的静默让位会让调用方误以为已应用，带着半迁移态直接服务）。
	// ③ 执行期续约：长迁移超 TTL 时锁过期，第三实例可闯入双跑；每 TTL/10 续约。
	key := "schema_migration:" + version
	holder := s.instanceID() + ":migration:" + version
	ok, lerr := s.TryAcquire(ctx, key, holder, migrationLockTTL)
	if lerr != nil {
		return 0, lerr
	}
	if !ok {
		return s.awaitMigrationMarker(ctx, version)
	}
	defer func() { _ = s.Release(ctx, key, holder) }()
	// ③ 执行期续约 + fencing（v6.8.0 P1-01，#6）：长迁移超 TTL 时锁过期，第三
	// 实例可闯入双跑（同一迁移两个有效执行者 → DDL/数据回写互相踩踏）。续约环
	// 每 TTL/10 续约；**续约失败（err≠nil，库断连等）或返回 ok=false（租约被夺/
	// 过期被抢）= 失租**——fencing 取消迁移执行（fn ctx 贯穿，cancel 传导），结果
	// 作废不写 marker（成功才写幂等语义），下次启动重试、他实例接手。v6.8.0 前
	// 续约失败被吞（return 出环继续跑）且 ok 返回值不检查，失租后执行方无感知。
	//
	// 收敛纪律（死锁教训）：主路绝不阻塞等续约环退出——环随 fctx 生命周期终止
	//（fcancel defer 于函数返回时触发）；失租态经 atomic.Bool 传递；marker 写入
	// 前的「终检续约」补齐强语义（fn 成功与写 marker 之间若恰失租，终检 ok=false
	// 拦下，marker 不落）。
	fctx, fcancel := context.WithCancel(ctx)
	defer fcancel()
	var leaseLost atomic.Bool
	async.GoSafe(func() {
		t := time.NewTicker(migrationRenewalInterval)
		defer t.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-t.C:
				ok, err := s.TryAcquire(context.Background(), key, holder, migrationLockTTL)
				if err != nil || !ok {
					leaseLost.Store(true)
					fcancel() // fencing：失租即取消迁移执行
					return
				}
			}
		}
	})
	// 拿到锁后双检 marker（他实例可能已跑完写 marker）。
	if err := s.db.WithContext(fctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	applied, err = fn(fctx)
	if err != nil {
		if leaseLost.Load() {
			applied = 0
			return 0, fmt.Errorf("迁移 %s 执行期间迁移租约丢失（fencing），结果作废不写 marker，待下次启动重试", version)
		}
		return applied, err
	}
	// 终检续约（fencing 强语义）：fn 成功后、写 marker 前再续一次——仍持有 = 刚
	// 续成功的租约覆盖 marker 写入窗口，接手者的 double-check 会看到 marker；
	// 失租（被夺/断连）= 结果作废不写 marker。终检后到 marker 落库之间的残余
	// 窗口（微秒级）由 TTL（5min）量级兜底：接手者只能在本实例 Release 后进入，
	// 届时 marker 已可被 double-check 命中。
	if ok, lerr := s.TryAcquire(context.Background(), key, holder, migrationLockTTL); lerr != nil || !ok {
		return 0, fmt.Errorf("迁移 %s marker 落库前终检续约失败（fencing，ok=%v err=%v），结果作废待重试", version, ok, lerr)
	}
	if err := s.db.WithContext(fctx).Create(&SchemaMigration{Version: version, AppliedAt: time.Now()}).Error; err != nil {
		return applied, err
	}
	return applied, nil
}
