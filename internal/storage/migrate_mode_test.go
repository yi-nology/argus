package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateModeValidation ARGUS_MIGRATE_MODE 值词表（#29）：""/job/verify
// 合法，未知值 fail-closed——拼写错误静默回落启动期迁移会让 verify 副本悄悄执行
// DDL，违背 Migration Job 拆分意图。
func TestMigrateModeValidation(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"job", "job", false},
		{"verify", "verify", false},
		{"veirfy", "", true}, // 拼写错误必须拒绝，不能回落默认
		{"Job", "", true},    // 大小写敏感（env 语义直读，不做折叠）
		{"1", "", true},
	}
	for _, c := range cases {
		t.Setenv("ARGUS_MIGRATE_MODE", c.in)
		got, err := MigrateMode()
		if c.wantErr {
			if err == nil {
				t.Errorf("MigrateMode(%q) 应报错，got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("MigrateMode(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("MigrateMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOpenVerifyMode verify 模式三态（#29）：已迁移库通过 + 空库 fail-closed +
// 非法值拒绝。用 SQLite 临时库（verify 的表/哨兵/防线复核与方言无关）。
func TestOpenVerifyMode(t *testing.T) {
	migrated := filepath.Join(t.TempDir(), "migrated.db")

	// 1. 缺省模式打开 = 完成迁移（job 与缺省同路径，此处一并覆盖）
	t.Setenv("ARGUS_MIGRATE_MODE", "")
	s, err := Open(migrated)
	if err != nil {
		t.Fatalf("缺省 Open 失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 2. verify 模式重开已迁移库：表/哨兵/防线齐备 → 通过
	t.Setenv("ARGUS_MIGRATE_MODE", "verify")
	s2, err := Open(migrated)
	if err != nil {
		t.Fatalf("verify 模式对已迁移库应通过: %v", err)
	}
	if !s2.InFlightGuardActive() {
		t.Error("verify 模式应复核出在飞防线已建立")
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. verify 模式对空库：关键表缺失 → fail-closed 拒绝启动
	t.Setenv("ARGUS_MIGRATE_MODE", "verify")
	if _, err := Open(filepath.Join(t.TempDir(), "fresh.db")); err == nil {
		t.Fatal("verify 模式对空库应拒绝启动（fail-closed）")
	} else if !strings.Contains(err.Error(), "ARGUS_MIGRATE_MODE=verify") {
		t.Errorf("错误应指向 verify 模式与 Job 修复路径: %v", err)
	}

	// 4. 非法值：Open 直接拒绝（与 MigrateMode 校验单点一致）
	t.Setenv("ARGUS_MIGRATE_MODE", "veirfy")
	if _, err := Open(filepath.Join(t.TempDir(), "bad.db")); err == nil {
		t.Fatal("非法 ARGUS_MIGRATE_MODE 应拒绝 Open")
	}
}

// TestOpenJobMode job 模式 = 与缺省同走迁移路径（进程退出判定在 main，storage
// 面只需保证迁移完成、库可用）。
func TestOpenJobMode(t *testing.T) {
	t.Setenv("ARGUS_MIGRATE_MODE", "job")
	s, err := Open(filepath.Join(t.TempDir(), "job.db"))
	if err != nil {
		t.Fatalf("job 模式 Open 应完成迁移: %v", err)
	}
	defer func() { _ = s.Close() }()
	// 迁移完成判据：任务表可写（建行即证明 schema 就位）
	if err := s.db.Exec("SELECT 1 FROM tasks LIMIT 1").Error; err != nil {
		t.Errorf("job 模式迁移后 tasks 表不可读: %v", err)
	}
}
