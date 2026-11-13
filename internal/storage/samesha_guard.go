// samesha_guard.go —— 多副本"同 PR 同 SHA 双跑"防线（深挖 P0；P1-03 #8 扩展为
// 三方言在飞唯一约束）：事务内 in-flight Count 检查是单实例纪律（SQLite 单写
// 串行 / writeMu），两个副本的租约过期窗口内可同时读到 0 → 双任务双烧 token。
// 最终防线必须落在数据库约束上。
//
// 设计约束：TaskAPI 手工对同 SHA 的 done 任务重审是合法用途（CreateTaskDirectIfIdle
// "done 任务不拦"），因此不能对全量行建唯一索引——只禁止两份"在飞"任务并存：
//   - SQLite：partial unique index（status IN (pending,running)）；
//   - PostgreSQL：partial unique index（同形态，v6.8.0 P1-03 补齐——生产方言
//     此前零约束，首审并发双建任务双烧 LLM）；
//   - MySQL：无 partial index → in_flight_key 生成列（在飞行拼接身份键、终态行
//     NULL）+ 唯一索引（MySQL 8 生成列；唯一索引对 NULL 天然放行）。
//
// 覆盖组合：webhook 在飞任务（head_sha 非空）按 (platform,owner,repo,number,kind,
// head_sha) 唯一；TaskAPI 手工任务（head_sha 空串）按空 SHA 参与唯一——同 PR 同
// kind 的两份手工在飞任务同样拦截（与 CreateTaskDirectIfIdle 的 in-flight 去重
// 语义对齐）。撞约束后由 CreateTaskForWebhook / CreateTaskDirectIfIdle 的
// isDuplicateKeyErr 回查收敛为幂等语义（不 500）。

package storage

import "fmt"

// sameSHAIndex 旧版部分唯一索引名（v3.74.2 前不含 kind）——升级时删除。
const sameSHAIndex = "idx_tasks_same_sha_live"

// sameSHAIndexV2 现行 SQLite 部分唯一索引：含 kind 列——同 PR 不同 kind（审查/
// 写作/CI 归因）的任务允许并存，"在飞唯一"只约束同 kind 同 SHA。
const sameSHAIndexV2 = "idx_tasks_same_sha_live_v2"

// inFlightIndexPG PostgreSQL 部分唯一索引（P1-03 #8）。
const inFlightIndexPG = "idx_tasks_in_flight"

// inFlightColumnMySQL / inFlightIndexMySQL MySQL 生成列与唯一索引名。
const (
	inFlightColumnMySQL = "in_flight_key"
	inFlightIndexMySQL  = "idx_tasks_in_flight"
)

// EnsureInFlightGuard 建立在飞任务唯一防线（三方言）。active=false + err=nil
// 表示因历史在飞重复未能建立——双跑防线退化为事务内检查，调用方应告警；在飞
// 任务收敛后重启自愈。active=false + err≠nil 为真实执行错误（权限/磁盘等），
// 调用方须带原始错误告警（v3.9.1：此前整体吞掉，运维会按"历史在飞重复"的
// hint 清数据、永远无效）。
// 调用时点：storage.Open 的迁移租约内（AutoMigrate 之后）——建索引/加列是 DDL，
// 多副本并发执行必须串行（与 P0-04 automigrate 租约同窗）。
func (s *Store) EnsureInFlightGuard() (active bool, err error) {
	switch {
	case s.isSQLite():
		return s.ensureSQLiteSameSHAGuard()
	case s.dialect == DialectPostgres:
		return s.ensurePGInFlightGuard()
	case s.dialect == DialectMySQL:
		return s.ensureMySQLInFlightGuard()
	}
	return false, fmt.Errorf("storage: 未知方言 %v，在飞唯一防线未建立", s.dialect)
}

// EnsureSameSHAGuard 兼容别名（v6.8.0 更名 EnsureInFlightGuard：防线从 SQLite
// 专属扩为三方言在飞唯一约束）。
func (s *Store) EnsureSameSHAGuard() (bool, error) { return s.EnsureInFlightGuard() }

// checkInFlightGuard 在飞防线只读复核（#29 verify 模式专用）：查索引/生成列
// 是否已建立，不执行任何 DDL——verify 的「只校验不迁移」承诺由本函数承担。
// 返回 (active, err)：active=false + err=nil = 防线不存在（历史重复未建或
// Job 未跑），语义与 EnsureInFlightGuard 的未建立态对齐；err≠nil = 探测本身失败。
func (s *Store) checkInFlightGuard() (bool, error) {
	switch {
	case s.isSQLite():
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?",
			sameSHAIndexV2).Scan(&n).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 SQLite 防线索引失败: %w", err)
		}
		return n > 0, nil
	case s.dialect == DialectPostgres:
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE tablename='tasks' AND indexname=?",
			inFlightIndexPG).Scan(&n).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 PG 防线索引失败: %w", err)
		}
		return n > 0, nil
	case s.dialect == DialectMySQL:
		var colCnt int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = ?",
			inFlightColumnMySQL).Scan(&colCnt).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 MySQL 防线列失败: %w", err)
		}
		if colCnt == 0 {
			return false, nil
		}
		var idxCnt int64
		if err := s.db.Raw("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = ?",
			inFlightIndexMySQL).Scan(&idxCnt).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 MySQL 防线索引失败: %w", err)
		}
		return idxCnt > 0, nil
	}
	return false, fmt.Errorf("storage: 未知方言 %v，无法复核在飞防线", s.dialect)
}

// ensureSQLiteSameSHAGuard 既有 SQLite 部分唯一索引（v3.74.2 形态不变）。
func (s *Store) ensureSQLiteSameSHAGuard() (bool, error) {
	// v3.74.2 索引升级：v1（不含 kind）删除，重建 v2（含 kind）。DROP 幂等，
	// 每次启动执行一次，在飞行数少无锁窗风险。
	_ = s.db.Exec("DROP INDEX IF EXISTS " + sameSHAIndex).Error
	// 幂等：IF NOT EXISTS 对已存在的同名索引（含非唯一形态）不重建
	cerr := s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + sameSHAIndexV2 +
		" ON tasks(platform, owner, repo, number, kind, head_sha) " +
		"WHERE status IN ('pending', 'running')").Error
	if cerr == nil {
		// v2 建成后清一次 v1 残留：滚动升级窗口里旧副本可能按旧镜像重启把 v1
		// 复活（CREATE IF NOT EXISTS），v1 存在会误杀跨 kind 同 SHA 并发（v2 明确
		// 放行的组合）。DROP 幂等零成本，每次启动顺带执行。
		_ = s.db.Exec("DROP INDEX IF EXISTS " + sameSHAIndex).Error
		return true, nil
	}
	if isDuplicateKeyErr(cerr) {
		// 历史在飞重复会让唯一索引创建失败：不阻断启动（数据不满足约束，
		// 非执行错误——调用方按数据冲突告警后照常服务）
		return false, nil
	}
	return false, cerr
}

// ensurePGInFlightGuard PostgreSQL 部分唯一索引（P1-03 #8）：与 SQLite 同形态。
func (s *Store) ensurePGInFlightGuard() (bool, error) {
	cerr := s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + inFlightIndexPG +
		" ON tasks(platform, owner, repo, number, kind, head_sha) " +
		"WHERE status IN ('pending', 'running')").Error
	if cerr == nil {
		return true, nil
	}
	if isDuplicateKeyErr(cerr) {
		// 历史在飞重复（pre-v6.8.0 双建产物）阻塞建索引：不阻断启动，告警自愈路径
		return false, nil
	}
	return false, cerr
}

// ensureMySQLInFlightGuard MySQL 生成列 + 唯一索引（P1-03 #8）：in_flight_key =
// 在飞行按 CONCAT_WS 拼接身份键（STORED 生成列），终态行恒 NULL（唯一索引对
// NULL 放行 = done/failed/stale 不受限）。幂等：information_schema 探测列与
// 索引存在性，缺哪步补哪步（AutoMigrate 不感知该列，滚动升级不回删）。
func (s *Store) ensureMySQLInFlightGuard() (bool, error) {
	var colCnt int64
	if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = ?",
		inFlightColumnMySQL).Scan(&colCnt).Error; err != nil {
		return false, fmt.Errorf("storage: 探测 tasks.%s 列失败: %w", inFlightColumnMySQL, err)
	}
	if colCnt == 0 {
		add := "ALTER TABLE tasks ADD COLUMN " + inFlightColumnMySQL +
			" VARCHAR(512) GENERATED ALWAYS AS (CASE WHEN status IN ('pending','running') " +
			"THEN CONCAT_WS('|', platform, owner, repo, number, kind, head_sha) ELSE NULL END) STORED"
		if err := s.db.Exec(add).Error; err != nil {
			return false, fmt.Errorf("storage: tasks.%s 生成列创建失败: %w", inFlightColumnMySQL, err)
		}
	}
	var idxCnt int64
	if err := s.db.Raw("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = ? AND NON_UNIQUE = 0",
		inFlightIndexMySQL).Scan(&idxCnt).Error; err != nil {
		return false, fmt.Errorf("storage: 探测 %s 索引失败: %w", inFlightIndexMySQL, err)
	}
	if idxCnt == 0 {
		if err := s.db.Exec("ALTER TABLE tasks ADD UNIQUE INDEX " + inFlightIndexMySQL +
			" (" + inFlightColumnMySQL + ")").Error; err != nil {
			if isDuplicateKeyErr(err) {
				// 历史在飞重复（pre-v6.8.0 双建产物）阻塞建唯一索引：不阻断启动
				return false, nil
			}
			return false, fmt.Errorf("storage: tasks.%s 唯一索引创建失败: %w", inFlightIndexMySQL, err)
		}
	}
	return true, nil
}
