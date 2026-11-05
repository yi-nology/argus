package storage

import (
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
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
