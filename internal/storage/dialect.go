// dialect.go —— SQL 方言单源（v6.2.0 收敛）：Dialect 枚举 + DSN 路由的
// dialector 工厂 + 跨方言错误翻译。此前方言判定三源并存（Store.sqlite/
// Store.mysql 双 bool + isSQLite(tx Dialector.Name())），分支面漂移无从察觉。
package storage

import (
	"errors"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Dialect SQL 方言。Open 按 DSN 前缀判定后写入 Store，此后一切方言分支
// （写门/行锁/部分唯一索引/迁移 DDL）只看这一个字段。
type Dialect int

const (
	DialectSQLite Dialect = iota // 零值=SQLite：单文件缺省形态
	DialectMySQL
	DialectPostgres
)

// mysqlOpen MySQL dialect（集群部署时把 ARGUS_DB_DSN 换成 MySQL DSN 即切）。
func mysqlOpen(dsn string) gorm.Dialector {
	return mysql.Open(dsn)
}

// postgresOpen PostgreSQL dialect（v5.4.8：ARGUS_DB_DSN 以 postgres:// 或
// postgresql:// 前缀即切；sslmode 由 DSN 参数携带，内网实例配 sslmode=disable）。
func postgresOpen(dsn string) gorm.Dialector {
	return postgres.Open(dsn)
}

// isDuplicateKeyErr 跨方言唯一/主键冲突判定：MySQL errno 1062、PostgreSQL
// SQLSTATE 23505、SQLite（glebarez/modernc）"UNIQUE constraint failed"。
// v6.2.0 补 pg 形态：此前只认 MySQL+SQLite——生产方言 pg（v5.4.8 起）撞
// 唯一键会被当普通错误上抛，MarkCommentDelivery 的幂等命中误报失败、
// CreateTaskForWebhook/CreateTaskDirectIfIdle 的 duplicate 回退分支永不触发。
// 字符串兜底覆盖错误类型被中间层抹掉的形态（pg "duplicate key value
// violates unique constraint"）。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	var me *mysqldriver.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return true
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "duplicate key value violates")
}
