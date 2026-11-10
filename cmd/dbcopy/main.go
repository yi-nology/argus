// dbcopy 一次性迁移工具（sqlite → postgres，逐表全量拷贝；v5.4.8 argus 切 pg）。
// 表清单与 storage.Open 的 AutoMigrate 同源；目标库须为空库（dbcopy 不做清库，
// 重跑前先清目标）。schema_migrations 一并拷贝：目标库哨兵直接视为已应用，
// 跳过 printalias 重建迁移（防重建表把灌入数据清掉）。
//
// 用法：ARGUS_SRC_DSN=<sqlite 文件路径> ARGUS_DST_DSN=<postgres dsn> dbcopy
package main

import (
	"fmt"
	"os"

	"github.com/yi-nology/argus/internal/storage"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// copyAll 迁移表清单（与 storage.Open 的 AutoMigrate 同源；schema_migrations
// 一并拷，目标库哨兵视为已应用——防 printalias 重建迁移清掉灌入数据）。
func copyAll(src, dst *gorm.DB) (int, error) {
	total := 0
	steps := []struct {
		name string
		run  func() (int64, error)
	}{
		{"tasks", func() (int64, error) { return copyTable[storage.Task](src, dst) }},
		{"finding_prints", func() (int64, error) { return copyTable[storage.FindingPrint](src, dst) }},
		{"print_aliases", func() (int64, error) { return copyTable[storage.PrintAlias](src, dst) }},
		{"review_targets", func() (int64, error) { return copyTable[storage.ReviewTarget](src, dst) }},
		{"leader_leases", func() (int64, error) { return copyTable[storage.LeaderLease](src, dst) }},
		{"qa_sessions", func() (int64, error) { return copyTable[storage.QASession](src, dst) }},
		{"pattern_signals", func() (int64, error) { return copyTable[storage.PatternSignal](src, dst) }},
		{"comment_deliveries", func() (int64, error) { return copyTable[storage.CommentDelivery](src, dst) }},
		{"repo_settings", func() (int64, error) { return copyTable[storage.RepoSetting](src, dst) }},
		{"schema_migrations", func() (int64, error) { return copyTable[storage.SchemaMigration](src, dst) }},
	}
	for _, st := range steps {
		n, err := st.run()
		if err != nil {
			return total, fmt.Errorf("%s: %w", st.name, err)
		}
		fmt.Printf("%-20s %d rows\n", st.name, n)
		total += int(n)
	}
	return total, nil
}

// copyTable 单表全量拷贝（泛型：源 Find 全量 → 目标逐条 Create；目标须空库，
// 主键/唯一冲突即失败暴露，不做静默去重）。
func copyTable[T any](src, dst *gorm.DB) (int64, error) {
	var rows []T
	if err := src.Find(&rows).Error; err != nil {
		return 0, err
	}
	for i := range rows {
		if err := dst.Create(&rows[i]).Error; err != nil {
			return int64(i), err
		}
	}
	return int64(len(rows)), nil
}

func main() {
	src := os.Getenv("ARGUS_SRC_DSN")
	dst := os.Getenv("ARGUS_DST_DSN")
	if src == "" || dst == "" {
		fmt.Fprintln(os.Stderr, "usage: ARGUS_SRC_DSN=<sqlite path> ARGUS_DST_DSN=<postgres dsn> dbcopy")
		os.Exit(2)
	}
	sdb, err := gorm.Open(sqlite.Open(src+"?_pragma=busy_timeout(10000)"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fatal("打开 sqlite 源失败", err)
	}
	dddb, err := gorm.Open(postgres.Open(dst), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fatal("打开 postgres 目标失败", err)
	}
	// 目标库建 schema（与 storage.Open 的 AutoMigrate 同源；不跑哨兵迁移——
	// schema_migrations 由数据拷贝带入，哨兵视为已应用）
	if err := dddb.AutoMigrate(
		&storage.Task{}, &storage.FindingPrint{}, &storage.PrintAlias{},
		&storage.ReviewTarget{}, &storage.LeaderLease{}, &storage.QASession{},
		&storage.PatternSignal{}, &storage.CommentDelivery{}, &storage.RepoSetting{},
		&storage.SchemaMigration{},
	); err != nil {
		fatal("目标库建 schema 失败", err)
	}
	total, err := copyAll(sdb, dddb)
	if err != nil {
		fatal("拷贝失败", err)
	}
	fmt.Println("TOTAL", total)
}

func fatal(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}
