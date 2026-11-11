// migrations_test.go —— 一次性迁移单飞锁回归（外部评估 P0）。
package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnsureMigrationAppliedConcurrent 双实例并发首启同跑同一迁移（评估 P0 回归）：
// 两独立 Store（不同实例 ID=不同 holder）同库并发执行同一未应用迁移——
// fn 必须恰好执行一次；让位方等 marker 成功返回（不再静默 0 带病通过）。
// 修复前 holder = "schema_migration:"+version 全实例相同，TryAcquire 的
// `holder = ? OR expires_at < ?` 条件让后启动实例直接把锁"续约"抢走，
// 两实例同跑重建型迁移（DDL 冲突/数据不一致）。
func TestEnsureMigrationAppliedConcurrent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "mig.db")
	if mysqlDSN := os.Getenv("ARGUS_TEST_MYSQL_DSN"); mysqlDSN != "" {
		dsn = setupTestMySQL(t, mysqlDSN)
	}
	if pgDSN := os.Getenv("ARGUS_TEST_PG_DSN"); pgDSN != "" {
		dsn = setupTestPG(t, pgDSN)
	}
	open := func(id string) *Store {
		t.Helper()
		s, err := Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		s.RunnerID = id
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	sa := open("pod-a")
	sb := open("pod-b")

	var calls atomic.Int32
	fn := func(ctx context.Context) (int, error) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond) // 放大并发窗口，让让位方真实等待
		return 1, nil
	}
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = sa.ensureMigrationApplied(context.Background(), "p0_concurrent_probe", fn)
	}()
	go func() {
		defer wg.Done()
		_, errB = sb.ensureMigrationApplied(context.Background(), "p0_concurrent_probe", fn)
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("并发迁移返回错误: A=%v B=%v", errA, errB)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("迁移 fn 执行 %d 次（期望恰好 1 次——单飞锁失效）", got)
	}
}

// TestRunOneTimeMigrationFailClosed 迁移 fail-closed 语义（P0-01，#2 回归）：
// 缺省（无逃生门 env）迁移 fn 失败 → storage.Open 必须返回 error 拒绝启动
// ——修复前仅告警继续（fail-open），半迁移态库带病服务、根因性失败陷入
// 「启动重试-失败-告警」循环。显式 ARGUS_MIGRATION_FAIL_OPEN=1 才降级 warn-only。
func TestRunOneTimeMigrationFailClosed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	failFn := func(ctx context.Context) (int, error) { return 0, errors.New("注入：方言差异/权限不足") }

	// 缺省 fail-closed：错误必须上抛（Open 由此拒绝启动）
	if err := s.runOneTimeMigration(ctx, "p0_failclosed_probe", "测试迁移", failFn); err == nil {
		t.Fatal("缺省迁移失败应 fail-closed 返回 error（修复前 fail-open 只告警）")
	}
	// marker 未写：失败不落哨兵，下次启动重试语义保持
	var n int64
	if err := s.db.Model(&SchemaMigration{}).Where("version = ?", "p0_failclosed_probe").Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("失败迁移不应写 marker: n=%d err=%v", n, err)
	}

	// 逃生门：显式 env=1 才降级告警继续
	t.Setenv("ARGUS_MIGRATION_FAIL_OPEN", "1")
	if err := s.runOneTimeMigration(ctx, "p0_failclosed_probe", "测试迁移", failFn); err != nil {
		t.Fatalf("ARGUS_MIGRATION_FAIL_OPEN=1 应降级 warn-only: %v", err)
	}

	// 成功路径 marker 落库
	okFn := func(ctx context.Context) (int, error) { return 1, nil }
	if err := s.runOneTimeMigration(ctx, "p0_failclosed_ok", "测试迁移", okFn); err != nil {
		t.Fatalf("成功迁移不应报错: %v", err)
	}
	if err := s.db.Model(&SchemaMigration{}).Where("version = ?", "p0_failclosed_ok").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("成功迁移应写 marker: n=%d err=%v", n, err)
	}
}

// TestOpenSchemaLeaseSerial 双实例并发 storage.Open（P0-04，#5 回归）：
// AutoMigrate 先于迁移租约执行时，多副本并发首启并发 DDL（MySQL 死锁/索引名
// 冲突）。修复后 schema 迁移（AutoMigrate+LONGTEXT）在 automigrate 租约内串行
// ——两实例并发 Open 同一库：双方均启动成功、租约释放干净（第三实例可即刻取锁）。
func TestOpenSchemaLeaseSerial(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "lease.db")
	if mysqlDSN := os.Getenv("ARGUS_TEST_MYSQL_DSN"); mysqlDSN != "" {
		dsn = setupTestMySQL(t, mysqlDSN)
	}
	if pgDSN := os.Getenv("ARGUS_TEST_PG_DSN"); pgDSN != "" {
		dsn = setupTestPG(t, pgDSN)
	}
	// 并发 Open 仅在 pg/MySQL 矩阵下执行（SQLite 单文件两连接并发 gorm.Open 会在
	// pragma 阶段撞 SQLITE_BUSY——文件锁层的事，与租约无关；SQLite 无并发首启问题）。
	if networked := os.Getenv("ARGUS_TEST_MYSQL_DSN") != "" || os.Getenv("ARGUS_TEST_PG_DSN") != ""; networked {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		for i := range errs {
			go func(i int) {
				defer wg.Done()
				s, err := Open(dsn)
				if s != nil {
					s.RunnerID = fmt.Sprintf("pod-%c", 'A'+i)
					_ = s.Close()
				}
				errs[i] = err
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("并发 Open 副本 %d 失败: %v", i, err)
			}
		}
	}
	// 顺序双 Open 复验 sqlite 路径启动无损；随后租约释放干净：第三实例即刻可
	// Open（不触发等待超时——持锁方退出时已 Release）
	s3, err := Open(dsn)
	if err != nil {
		t.Fatalf("租约未释放，第三实例 Open 失败: %v", err)
	}
	_ = s3.Close()
}
