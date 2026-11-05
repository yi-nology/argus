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
	"time"

	"gorm.io/gorm"
)

// SchemaMigration 一次性数据迁移的应用记录。
type SchemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey;size:64"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

// ensureMigrationApplied 幂等执行一次性迁移：marker 已存在 → 跳过（返回 0, nil）；
// fn 成功后才写 marker——失败不写，下次启动重试。
func (s *Store) ensureMigrationApplied(ctx context.Context, version string, fn func(ctx context.Context) (int, error)) (applied int, err error) {
	var marker SchemaMigration
	if err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	// 单飞锁（v5.4.8 多副本加固）：leader_leases 租约互斥——多副本（pg/MySQL
	// 网络库）同时首启时防并发跑重建型迁移（printalias PR 域迁移是
	// DROP+CREATE，并发执行会互相踩）。拿不到锁 = 他实例正在跑：本次让位返回
	// 0（marker 未写，该实例下次启动重试；语义与「失败不写 marker」一致）。
	// 拿到锁后双检 marker（他实例可能已跑完写 marker）。
	holder := "schema_migration:" + version
	if ok, lerr := s.TryAcquire(ctx, holder, holder, 5*time.Minute); lerr != nil || !ok {
		return 0, lerr
	}
	defer func() { _ = s.Release(ctx, holder, holder) }()
	if err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	applied, err = fn(ctx)
	if err != nil {
		return applied, err
	}
	if err := s.db.WithContext(ctx).Create(&SchemaMigration{Version: version, AppliedAt: time.Now()}).Error; err != nil {
		return applied, err
	}
	return applied, nil
}
