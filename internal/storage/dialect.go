package storage

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// mysqlOpen MySQL dialect（集群部署时把 ARGUS_DB_DSN 换成 MySQL DSN 即切）。
func mysqlOpen(dsn string) gorm.Dialector {
	return mysql.Open(dsn)
}
