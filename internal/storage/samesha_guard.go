// samesha_guard.go —— 多副本"同 PR 同 SHA 双跑"防线（深挖 P0）：
// 事务内 in-flight Count 检查是单实例纪律（SQLite 单写串行 / writeMu），两个副本
// 的租约过期窗口内可同时读到 0 → 双任务双烧 token。最终防线必须落在数据库约束上。
//
// 设计约束：TaskAPI 手工对同 SHA 的 done 任务重审是合法用途（CreateTaskDirectIfIdle
// "done 任务不拦"），因此不能对全量行建唯一索引——
//   - SQLite：status IN (pending, running) 的部分唯一索引（partial index），
//     只禁止两份"在飞"任务并存；
//   - MySQL：无 partial index，改由 CreateTaskForWebhook 事务内对 review_targets
//     行 FOR UPDATE 串行化（跨实例互斥；首审 PR 无锚行的残余竞窗可接受）。
package storage

// sameSHAIndex 旧版部分唯一索引名（v3.74.2 前不含 kind）——升级时删除。
const sameSHAIndex = "idx_tasks_same_sha_live"

// sameSHAIndexV2 现行部分唯一索引：含 kind 列——同 PR 不同 kind（审查/写作/
// CI 归因）的任务允许并存，"在飞唯一"只约束同 kind 同 SHA。
const sameSHAIndexV2 = "idx_tasks_same_sha_live_v2"

// EnsureSameSHAGuard 建立在飞同 SHA 唯一防线。active=false + err=nil 表示因
// 历史在飞重复未能建立（v3.5.12 之前无此约束）——双跑防线退化为事务内检查，
// 调用方应告警；在飞任务收敛后重启自愈。active=false + err≠nil 为真实执行
// 错误（权限/磁盘等），调用方须带原始错误告警（v3.9.1：此前整体吞掉，运维
// 会按"历史在飞重复"的 hint 清数据、永远无效）。MySQL 恒返回 (true, nil)
// （防线在事务锁，见文件头注释）。
func (s *Store) EnsureSameSHAGuard() (active bool, err error) {
	if !s.isSQLite() {
		return true, nil
	}
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
