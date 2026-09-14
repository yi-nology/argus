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

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// sameSHAIndex SQLite 部分唯一索引名。
const sameSHAIndex = "idx_tasks_same_sha_live"

// EnsureSameSHAGuard 建立在飞同 SHA 唯一防线。active=false 表示因历史在飞重复
// 未能建立（v3.5.12 之前无此约束）——双跑防线退化为事务内检查，调用方应告警；
// 在飞任务收敛后重启自愈。MySQL 恒返回 (true, nil)（防线在事务锁，见文件头注释）。
func (s *Store) EnsureSameSHAGuard() (active bool, err error) {
	if !s.sqlite {
		return true, nil
	}
	// 幂等：IF NOT EXISTS 对已存在的同名索引（含非唯一形态）不重建
	err = s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + sameSHAIndex +
		" ON tasks(platform, owner, repo, number, head_sha) "+
		"WHERE status IN ('pending', 'running')").Error
	if err != nil {
		// 历史在飞重复会让索引创建失败：不阻断启动（与"探测失败必须暴露"的分寸
		// 一致——这不是探测失败，是数据不满足约束，调用方告警后照常服务）
		return false, nil
	}
	return true, nil
}

// isDuplicateKeyErr 跨方言唯一/主键冲突判定：MySQL errno 1062；
// SQLite（glebarez/modernc）"UNIQUE constraint failed: ..."。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
