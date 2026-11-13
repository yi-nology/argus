// store_prune.go —— 保留期清理与用量/统计聚合面：Prune 族 + 月度 token + /stats/overview 数据源。

package storage

import (
	"context"
	"encoding/json"
	"time"
)

// PruneTasks 保留策略：删除 created_at 早于 cutoff 的已终结任务
// （done/failed/stale；pending/running 不动）。分批删除（每批 500）——
// 单条大 DELETE 会长事务占住 SQLite 单写连接，队头阻塞心跳（深挖修复）。
// 返回删除行数。
func (s *Store) PruneTasks(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&Task{}).
				Where("status IN ? AND created_at < ?", []string{StatusDone, StatusFailed, StatusStale}, cutoff).
				Limit(500).Pluck("id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("id IN ?", batch).Delete(&Task{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// PruneQASessions 会话保留策略：updated_at 早于 cutoff 的问答会话清理（深挖修复：
// qa_sessions 此前永不清理，history_json 持续追加单调膨胀）。分批删除（每批 500，
// 与 PruneTasks 同理：单条大 DELETE 长事务占住 SQLite 单写连接，v3.9.1 补齐）。
// 返回删除行数。
func (s *Store) PruneQASessions(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&QASession{}).
				Where("updated_at < ?", cutoff).Limit(500).Pluck("session_key", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("session_key IN ?", batch).Delete(&QASession{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// PruneStalePrints 指纹保留策略：LastSeenAt 早于 cutoff 的指纹清理（深挖修复：
// finding_prints 含 LONGTEXT comment 永不清理）。语义安全：长期未复现的指纹
// 遗忘后最坏效果是"同型意见重新从头计数"，不会复活已抑制意见（suppressed
// 状态随行删除，同型意见若真复发会以 open 新行参与——保留期给足即可）。
// v3.8.0：随行清理指向已删指纹的孤儿别名（print_aliases 此前无限增长）。
// v3.9.1：分批删除（每批 500），别名清理跟随本批指纹。
func (s *Store) PruneStalePrints(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&FindingPrint{}).
				Where("last_seen_at < ?", cutoff).Limit(500).Pluck("print", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("print IN ?", batch).Delete(&FindingPrint{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if err := s.db.WithContext(ctx).
				Where("print_fp IN ?", batch).Delete(&PrintAlias{}).Error; err != nil {
				return err
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// RepoTokenUsage 仓库维度 token 用量聚合（月度预算/用量报表，§5 v3.1）。
type RepoTokenUsage struct {
	Platform string `json:"platform"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	Tokens   int64  `json:"tokens"`
	Tasks    int64  `json:"tasks"`
}

// RepoMonthlyTokens 单仓库自 since 起的 token 消耗（done 任务口径——failed 任务的
// 中途消耗不计入预算，避免平台故障误烧预算）。
func (s *Store) RepoMonthlyTokens(ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND created_at >= ?",
			platform, owner, repo, StatusDone, since).
		Select("COALESCE(SUM(tokens_used),0)").Scan(&total).Error
	return total, err
}

// MonthlyTokensByRepo 全部仓库自 since 起的 token 用量（GET /usage/monthly）。
func (s *Store) MonthlyTokensByRepo(ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&Task{}).
		Select("platform, owner, repo, SUM(tokens_used) AS tokens, COUNT(*) AS tasks").
		Where("status = ? AND created_at >= ?", StatusDone, since).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// RepoMonthlyTokensReal 真实消费账（v6.9.0 P1-06 #11 → #32 升级为逐调用实销）：
// 口径从「任务终态聚合」改为 usage_event 逐调用记录（status=settled）——运行中
// 任务的调用即时入账、重试各次调用全部计入（终态聚合会把重试覆盖成末次
// attempt 的 tokens_used），多副本不再等终态才有真实消费可见。配额账
// （RepoMonthlyTokens，done 口径）语义不回退：平台故障误烧不占配额；真实
// 消费账独立上限（ARGUS_REAL_TOKEN_BUDGET）。历史任务经终态 delta 兜底 +
// 一次性回填迁移补齐（backfill* 事件同为 settled，天然并入本口径）。
func (s *Store) RepoMonthlyTokensReal(ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND occurred_at >= ?",
			platform, owner, repo, UsageSettled, since).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&total).Error
	return total, err
}

// MonthlyTokensByRepoReal 全部仓库实销聚合（/usage/monthly 双账本展示）。
// tasks 列 = 触达的不同任务数（同任务多笔调用去重，列义与配额账保持一致）。
func (s *Store) MonthlyTokensByRepoReal(ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Select("platform, owner, repo, SUM(input_tokens + output_tokens) AS tokens, "+
			"COUNT(DISTINCT task_id) AS tasks").
		Where("status = ? AND occurred_at >= ?", UsageSettled, since).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// PruneUsageEvents 账本保留清理（#32）：usage_event 按 occurred_at、已收口
// 预留（settled/released）按 updated_at 早于 cutoff 分批删除（每批 500，与
// PruneTasks 同理——单条大 DELETE 长事务占住 SQLite 单写连接队头阻塞心跳）。
// 活跃预留（reserved）不动：仍在占额度判定（过期的由 ReleaseExpiredReservations
// 先置 released，下轮随龄出局）。与 PruneTasks 同 cutoff 时序安全：任务保留
// ≥ 事件龄（事件 occurred_at ≥ 任务 created_at），任务未清则其事件必在——
// 不存在「任务还在、事件被清 → 启动对账重复回填」的窗口。
func (s *Store) PruneUsageEvents(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var ids []uint
			if err := s.db.WithContext(ctx).Model(&UsageEvent{}).
				Where("occurred_at < ?", cutoff).Limit(500).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&UsageEvent{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(ids) < 500 {
				break
			}
		}
		for {
			var ids []uint
			if err := s.db.WithContext(ctx).Model(&BudgetReservation{}).
				Where("status <> ? AND updated_at < ?", ReservationReserved, cutoff).
				Limit(500).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&BudgetReservation{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(ids) < 500 {
				break
			}
		}
		return nil
	})
	return total, err
}

// jsonUnmarshal 是 encoding/json.Unmarshal 的包内别名，便于测试 mock。
var jsonUnmarshal = jsonUnmarshalFn

func jsonUnmarshalFn(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// RepoStats 仓库维度审查概览聚合行（GET /stats/overview，v3.88.0）。
type RepoStats struct {
	Platform   string `json:"platform"`
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	TasksDone  int64  `json:"tasks_done"`
	Approved   int64  `json:"verdict_approve"`
	Commented  int64  `json:"verdict_comment"`
	Rejected   int64  `json:"verdict_request_changes"`
	Tokens     int64  `json:"tokens_used"`
	Suppressed int64  `json:"findings_suppressed"`
}

// StatsOverview 窗口内按仓聚合任务量 / verdict 分布 / token 用量 / 抑制计数。
// tasks 与 prints 两表各自 GROUP BY 后在 Go 侧合并——抑制计数口径 = 窗口内
// 首次出现的意见中当前处于 suppressed 状态的数量（抑制时刻不入库，以首次
// 出现时间近似）。verdict 分布在 SQL 层用 CASE WHEN（SQLite/MySQL 通用）。
func (s *Store) StatsOverview(ctx context.Context, since time.Time, platform, owner, repo string) ([]RepoStats, error) {
	var aggs []struct {
		Platform  string
		Owner     string
		Repo      string
		TasksDone int64
		Approved  int64
		Commented int64
		Rejected  int64
		Tokens    int64
	}
	tq := s.quietDB().WithContext(ctx).Model(&Task{}).
		Select("platform, owner, repo, COUNT(*) AS tasks_done, "+
			"COALESCE(SUM(CASE WHEN verdict = 'approve' THEN 1 ELSE 0 END),0) AS approved, "+
			"COALESCE(SUM(CASE WHEN verdict = 'comment' THEN 1 ELSE 0 END),0) AS commented, "+
			"COALESCE(SUM(CASE WHEN verdict = 'request_changes' THEN 1 ELSE 0 END),0) AS rejected, "+
			"COALESCE(SUM(tokens_used),0) AS tokens").
		Where("status = ? AND created_at >= ?", StatusDone, since).
		Group("platform, owner, repo")
	if platform != "" {
		tq = tq.Where("platform = ?", platform)
	}
	if owner != "" {
		tq = tq.Where("owner = ?", owner)
	}
	if repo != "" {
		tq = tq.Where("repo = ?", repo)
	}
	if err := tq.Order("tasks_done DESC").Find(&aggs).Error; err != nil {
		return nil, err
	}

	var sups []struct {
		Platform string
		Owner    string
		Repo     string
		N        int64
	}
	sq := s.quietDB().WithContext(ctx).Model(&FindingPrint{}).
		Select("platform, owner, repo, COUNT(*) AS n").
		Where("status = ? AND first_seen_at >= ?", PrintSuppressed, since).
		Group("platform, owner, repo")
	if platform != "" {
		sq = sq.Where("platform = ?", platform)
	}
	if owner != "" {
		sq = sq.Where("owner = ?", owner)
	}
	if repo != "" {
		sq = sq.Where("repo = ?", repo)
	}
	if err := sq.Find(&sups).Error; err != nil {
		return nil, err
	}

	supMap := make(map[string]int64, len(sups))
	for _, x := range sups {
		supMap[x.Platform+"/"+x.Owner+"/"+x.Repo] = x.N
	}
	out := make([]RepoStats, 0, len(aggs))
	for _, a := range aggs {
		row := RepoStats{
			Platform: a.Platform, Owner: a.Owner, Repo: a.Repo,
			TasksDone: a.TasksDone, Approved: a.Approved,
			Commented: a.Commented, Rejected: a.Rejected, Tokens: a.Tokens,
			Suppressed: supMap[a.Platform+"/"+a.Owner+"/"+a.Repo],
		}
		out = append(out, row)
	}
	return out, nil
}
