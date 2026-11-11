// migrations_test.go —— 一次性迁移单飞锁回归（外部评估 P0）。
package storage

import (
	"context"
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
