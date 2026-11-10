// Package storage 持久化（design §5.2）：GORM + 多方言（SQLite 单文件起步 /
// MySQL / PostgreSQL，v5.4.8 生产切 pg；方言判定单源见 dialect.go）。模型
// 定义见 models.go，操作面按域归位（store_task/prints/printalias/
// reviewtarget/...）；另有评论投递幂等（comment_delivery.go）、samesha
// 去重守卫、跨实例租约（lease.go）、保留期清理（store_prune.go）与迁移
// （migrations.go）。ORM 错误语义不出 storage 包。
package storage
