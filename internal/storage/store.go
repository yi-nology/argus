// Package storage 持久化（design §5.2）：GORM + SQLite 单文件起步（dialect 可切 MySQL）。
// 三张表解决四个隐含承诺：
//
//	tasks          —— TaskAPI 异步任务重启不丢 + webhook 幂等（event_id=平台 delivery ID 唯一约束）
//	finding_prints —— Merger 指纹跨轮次持久化（status=open|suppressed，反馈通道 v3.1）
//	review_targets —— 总结评论定位 + last_head_sha 同 SHA 去重查询源之一
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"git.example.com/enjoydream/ekit/data/gormx"
)

// Task tasks 表记录。Status 词表钉死：pending / running / done / failed / stale（§10.4）。
type Task struct {
	ID       string `gorm:"column:id;primaryKey;size:40"`
	Kind     string `gorm:"column:kind;size:32;not null"`
	Platform string `gorm:"column:platform;size:16;not null;index:idx_pr,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_pr,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_pr,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;index:idx_pr,priority:4"`
	HeadSHA  string `gorm:"column:head_sha;size:64;index"`
	// EventType 触发本任务的平台事件（webhook 填 cr.opened/...；CLI 任务为空）
	EventType string `gorm:"column:event_type;size:32"`
	// EventID = 平台 delivery ID（X-Gitea-Delivery 等），幂等键。
	// 缺 header 时保持 NULL（唯一索引允许多个 NULL；置空字符串会误判 duplicate）。
	// 注意：MySQL 8.0+ / SQLite 支持 UNIQUE 列多 NULL；MariaDB 10.5- 行为不同，需 COALESCE 兼容。
	EventID    *string `gorm:"column:event_id;size:128;uniqueIndex"`
	Status     string  `gorm:"column:status;size:16;not null;default:pending;index"`
	Verdict    string  `gorm:"column:verdict;size:32"`
	Report     string  `gorm:"column:report;type:text"`
	ResultJSON string  `gorm:"column:result_json;type:text"`
	FailReason string  `gorm:"column:fail_reason;type:text"`
	TokensUsed int     `gorm:"column:tokens_used"`
	// SummaryCommentID 最近一次成功回帖的总结评论 ID（任务结束回填，供观测）。
	SummaryCommentID int64  `gorm:"column:summary_comment_id"`
	ArtifactsJSON    string `gorm:"column:artifacts_json;type:text"`
	// DebugJSON 调试产物（ARGUS_DEBUG=1 时留存 R1 spec/R2 摘要/R3 原始输出）
	DebugJSON string `gorm:"column:debug_json;type:text"`

	// RunnerID 领取本任务的实例 ID（心跳归属；多副本共库时滚动发布不误伤他实例在途任务）
	RunnerID string `gorm:"column:runner_id;size:64;index"`
	// HeartbeatAt 最近心跳（worker 池周期刷新 running 任务；超时未续才会被重置回 pending）
	HeartbeatAt *time.Time `gorm:"column:heartbeat_at"`

	CreatedAt  time.Time  `gorm:"column:created_at;index"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	StartedAt  *time.Time `gorm:"column:started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at"`
}

func (Task) TableName() string { return "tasks" }

// 任务状态词表（§10.4）。
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusStale   = "stale" // 审查完成但判过期：findings 入库、不回帖
)

// FindingPrint finding_prints 表：repo+number+指纹 唯一。
type FindingPrint struct {
	ID uint `gorm:"column:id;primaryKey"`

	Platform string `gorm:"column:platform;size:16;not null;uniqueIndex:idx_print,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;uniqueIndex:idx_print,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;uniqueIndex:idx_print,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;uniqueIndex:idx_print,priority:4"`
	Print    string `gorm:"column:print;size:80;not null;uniqueIndex:idx_print,priority:5"`

	File     string `gorm:"column:file;size:512"`
	Line     int    `gorm:"column:line"`
	Severity string `gorm:"column:severity;size:16"`
	Comment  string `gorm:"column:comment;type:text"`
	Status   string `gorm:"column:status;size:16;not null;default:open"` // open | suppressed

	FirstTaskID string    `gorm:"column:first_task_id;size:40"`
	LastTaskID  string    `gorm:"column:last_task_id;size:40"`
	TimesSeen   int       `gorm:"column:times_seen"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (FindingPrint) TableName() string { return "finding_prints" }

// 指纹反馈状态词表（v3.0 仅表结构预留，反馈通道 v3.1 落地）。
const (
	PrintOpen       = "open"
	PrintSuppressed = "suppressed"
)

// ReviewTarget review_targets 表。
type ReviewTarget struct {
	ID uint `gorm:"column:id;primaryKey"`

	Platform string `gorm:"column:platform;size:16;not null;uniqueIndex:idx_target,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;uniqueIndex:idx_target,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;uniqueIndex:idx_target,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;uniqueIndex:idx_target,priority:4"`

	LastSummaryCommentID int64  `gorm:"column:last_summary_comment_id"`
	LastHeadSHA          string `gorm:"column:last_head_sha;size:64"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (ReviewTarget) TableName() string { return "review_targets" }

// Store 存储门面。SQLite 下用连接数=1 + 写锁实现"查询与落库同一事务"的串行语义
// （design §5.2：SQLite 单写串行下单事务即闭环）；MySQL 下写锁退化为空操作亦无害。
type Store struct {
	db      *gorm.DB
	writeMu sync.Mutex
	sqlite  bool
	// RunnerID 本实例标识（Open 后由 main 赋值；claim/心跳用它归属 running 任务）
	RunnerID string
}

// Open 打开数据库并自动迁移。DSN 以 "mysql://" 前缀或含 "@tcp("（go-sql-driver 形态）
// 视为 MySQL，否则 SQLite——不做子串 "mysql" 判断：路径含 mysql 的 SQLite 文件会误判。
func Open(dsn string) (*Store, error) {
	s := &Store{}
	if strings.HasPrefix(strings.ToLower(dsn), "mysql://") || strings.Contains(dsn, "@tcp(") {
		// mysql:// 是便利前缀（go-sql-driver 不认识），大小写不敏感剥离
		if len(dsn) >= 8 && strings.EqualFold(dsn[:8], "mysql://") {
			dsn = dsn[8:]
		}
		db, err := gorm.Open(mysqlOpen(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 mysql 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		sqlDB.SetConnMaxLifetime(time.Hour)
		s.db, s.sqlite = db, false
	} else {
		// glebarez/sqlite 纯 Go 驱动；缺省注入 busy_timeout + WAL（§5.2 并发注意）
		if !strings.Contains(dsn, "?") {
			dsn += "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		}
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 sqlite 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		sqlDB.SetMaxOpenConns(1) // 单写串行：webhook 并发幂等/去重的正确性基础
		sqlDB.SetMaxIdleConns(1) // idle=max-open 避免连接反复开关的 WAL 开销
		// SQLite 不设 ConnMaxLifetime：单文件单连接，文件替换由 OS 语义保证一致性
		s.db, s.sqlite = db, true
	}
	if err := s.db.AutoMigrate(&Task{}, &FindingPrint{}, &ReviewTarget{}, &LeaderLease{}); err != nil {
		return nil, fmt.Errorf("storage: 自动迁移失败: %w", err)
	}
	if !s.sqlite {
		if err := s.upgradeMySQLLongText(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// longtextColumns MySQL 下需要 LONGTEXT 的大文本列。GORM `type:text` 在 MySQL 生成
// TEXT（64KB 上限）：大报告/调试产物写入失败 → 任务卡 running → 重启重置重跑 →
// 再失败，每次重启白烧一遍 LLM。AutoMigrate 之后幂等升级。
var longtextColumns = map[string][]string{
	"tasks":          {"report", "result_json", "fail_reason", "artifacts_json", "debug_json"},
	"finding_prints": {"comment"},
}

func (s *Store) upgradeMySQLLongText() error {
	for table, cols := range longtextColumns {
		for _, col := range cols {
			var cnt int64
			// 探测失败必须暴露：静默跳过 ALTER 会让 64KB 死循环修复无声失效
			if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND DATA_TYPE = 'text'",
				table, col).Scan(&cnt).Error; err != nil {
				return fmt.Errorf("storage: 探测 %s.%s 列类型失败: %w", table, col, err)
			}
			if cnt == 0 {
				continue
			}
			if err := s.db.Exec(fmt.Sprintf("ALTER TABLE `%s` MODIFY `%s` LONGTEXT", table, col)).Error; err != nil {
				return fmt.Errorf("storage: %s.%s 升级 LONGTEXT 失败: %w", table, col, err)
			}
		}
	}
	return nil
}

// Close 关闭底层连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping 健康检查（readyz）。
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(cctx)
}

// ResetRunningToPending 把心跳过期的 running 任务重置回 pending
// （§10.4：崩溃任务否则静默丢失）。staleAfter 取心跳间隔的 ≥3 倍：
// 只有 heartbeat_at 已死（或遗留的 NULL 心跳旧行）才重置——多副本共库时，
// 一个实例滚动重启不会把其他存活实例正在执行的任务也重置（那会双执行、双回帖）。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cutoff := time.Now().Add(-staleAfter)
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("status = ? AND (heartbeat_at IS NULL OR heartbeat_at < ?)", StatusRunning, cutoff).
		Update("status", StatusPending)
	return res.RowsAffected, res.Error
}

// TouchRunningHeartbeats 续本实例全部 running 任务的心跳（worker 池周期调用）。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
	if s.RunnerID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Model(&Task{}).
		Where("status = ? AND runner_id = ?", StatusRunning, s.RunnerID).
		Update("heartbeat_at", time.Now()).Error
}

// NewTaskID 生成任务 ID（service/poller 共用单一出处）。
func NewTaskID() string {
	return "t-" + strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16] + time.Now().Format("01021504")
}

// GetTask 按 ID 查询。
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetTaskByEventID webhook 幂等查询：命中 = 平台重发。
func (s *Store) GetTaskByEventID(ctx context.Context, eventID string) (*Task, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).Where("event_id = ?", eventID).First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTask 新建任务（CLI/TaskAPI 提交；无幂等键）。
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Create(t).Error
}

// CreateTaskForWebhook 同步完成"同 SHA 去重检查 + 落库"（同一事务，§5.1 规则 4 的 TOCTOU 防线）。
// 返回 dupReason 非空表示命中去重（"last_head_sha" | "in_flight"），existingID 给出可指向的任务。
func (s *Store) CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	txErr := gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		// 规则 4a：review_targets.last_head_sha == 本次 head
		var target ReviewTarget
		err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			t.Platform, t.Owner, t.Repo, t.Number).First(&target).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err // 瞬时 DB 错误不能静默跳过去重检查
		}
		if err == nil && target.LastHeadSHA != "" && target.LastHeadSHA == t.HeadSHA {
			dupReason = "last_head_sha"
			return nil
		}
		// 规则 4b：存在同 head_sha 的 pending/running 任务（防 in-flight 竞态）
		var cnt int64
		err = tx.Model(&Task{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.HeadSHA, []string{StatusPending, StatusRunning}).
			Count(&cnt).Error
		if err != nil {
			return err
		}
		if cnt > 0 {
			dupReason = "in_flight"
			var existing Task
			_ = tx.Select("id").Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.HeadSHA,
				[]string{StatusPending, StatusRunning}).First(&existing).Error
			existingID = existing.ID
			return nil
		}
		return tx.Create(t).Error
	})
	if txErr != nil {
		// event_id 唯一冲突（同 delivery 并发重发：幂等预查与落库之间存在窗口）——
		// 回查既有任务按幂等重复处理，避免以 500 暴露（GitHub 对连续 500 会禁用 webhook）。
		if t.EventID != nil {
			if exist, e := s.GetTaskByEventID(ctx, *t.EventID); e == nil && exist != nil {
				return "event_id", exist.ID, nil
			}
		}
		return "", "", txErr
	}
	return dupReason, existingID, nil
}

// quietDB 预期内"查无记录"的路径专用：GORM 默认把 ErrRecordNotFound 打成红色错误日志，
// worker 每 500ms 空轮询一次会刷屏掩盖真实告警——这些路径静默。
func (s *Store) quietDB() *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: logger.Discard})
}

// ClaimNextPending worker 原子抢占一个 pending 任务（pending→running）。
// 返回 ok=false 表示当前无任务。
func (s *Store) ClaimNextPending(ctx context.Context) (*Task, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var claimed *Task
	err := s.quietDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		err := tx.Where("status = ?", StatusPending).Order("created_at ASC, id ASC").First(&t).Error
		if err != nil {
			return err // gorm.ErrRecordNotFound → 无任务
		}
		now := time.Now()
		res := tx.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusPending).
			Updates(map[string]any{"status": StatusRunning, "started_at": &now,
				"runner_id": s.RunnerID, "heartbeat_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound // 被并发抢走（理论上不会：连接数=1）
		}
		t.Status = StatusRunning
		t.StartedAt = &now
		t.RunnerID = s.RunnerID
		t.HeartbeatAt = &now
		claimed = &t
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return claimed, claimed != nil, nil
}

// ListTasksByHead 查询同 PR 同 head 的任务（E2E 幂等断言/诊断用）。
func (s *Store) ListTasksByHead(ctx context.Context, platform, owner, repo, number, headSHA string) ([]Task, error) {
	var out []Task
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
		platform, owner, repo, number, headSHA).Order("created_at ASC").Find(&out).Error
	return out, err
}

// UpdateTaskRunningStatus 局部更新状态字段（任务执行中的落点）。
func (s *Store) FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now()
	updates := map[string]any{
		"status":      status,
		"verdict":     verdict,
		"report":      report,
		"result_json": resultJSON,
		"fail_reason": failReason,
		"tokens_used": tokensUsed,
		"finished_at": &now,
	}
	if summaryCommentID > 0 {
		updates["summary_comment_id"] = summaryCommentID
	}
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).Updates(updates).Error
}

// SetTaskHeadSHA 回填 CLI 任务的 head SHA（执行期从平台获取）。
func (s *Store) SetTaskHeadSHA(ctx context.Context, id, sha string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("head_sha", sha).Error
}

// SetTaskDebug 留存阶段调试产物（ARGUS_DEBUG=1）。
func (s *Store) SetTaskDebug(ctx context.Context, id, debugJSON string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("debug_json", debugJSON).Error
}

// HasNewerNonFailedTask staleness 单条件判定（§10.2 五轮评审修正）：
// 存在同 PR 更晚创建的非 failed 且带 head 的任务 ⇒ 本任务 stale。
// 只统计 head_sha <> ” 的任务（webhook/poller 来源）：手动任务 head 为空（执行期才回填），
// 若它先压 stale 再失败（手动任务参数/平台错误率高），本轮审查会被永久静默吞掉。
// 数据全来自 tasks 表（head_sha/created_at），不依赖 review_targets.last_head_sha。
func (s *Store) HasNewerNonFailedTask(ctx context.Context, platform, owner, repo, number, taskID string, createdAt time.Time) (bool, error) {
	var cnt int64
	err := s.db.WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND id <> ? AND created_at > ? AND status <> ? AND head_sha <> ''",
			platform, owner, repo, number, taskID, createdAt, StatusFailed).
		Count(&cnt).Error
	return cnt > 0, err
}

// PruneTasks 保留策略：删除 created_at 早于 cutoff 的已终结任务
// （done/failed/stale；pending/running 不动）。返回删除行数。
func (s *Store) PruneTasks(ctx context.Context, cutoff time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res := s.db.WithContext(ctx).Where(
		"status IN ? AND created_at < ?",
		[]string{StatusDone, StatusFailed, StatusStale}, cutoff).Delete(&Task{})
	return res.RowsAffected, res.Error
}

// TaskCounts 队列深度（pending/running），供指标暴露。
func (s *Store) TaskCounts(ctx context.Context) (pending, running int64, err error) {
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusPending).Count(&pending).Error
	if err != nil {
		return 0, 0, err
	}
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusRunning).Count(&running).Error
	return pending, running, err
}

// ListTasks 任务列表（GET /tasks）：status 过滤（空=全部），created_at 倒序，limit 收口。
func (s *Store) ListTasks(ctx context.Context, status string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.quietDB().WithContext(ctx).Order("created_at DESC, id DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []Task
	err := q.Find(&out).Error
	return out, err
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

// FindReportByHead 按 head SHA 查最近一次成功的报告（GET /reports/:ref）。
func (s *Store) FindReportByHead(ctx context.Context, headSHA string) (*Task, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).
		Where("head_sha = ? AND status = ? AND report != ''", headSHA, StatusDone).
		Order("created_at DESC").First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetReviewTarget 查询（不存在返回 nil,nil）。
func (s *Store) GetReviewTarget(ctx context.Context, platform, owner, repo, number string) (*ReviewTarget, error) {
	var t ReviewTarget
	err := s.quietDB().WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ?",
		platform, owner, repo, number).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// UpsertReviewTarget 回帖成功后更新定位（staleness 不读它，五轮评审定案）。
func (s *Store) UpsertReviewTarget(ctx context.Context, platform, owner, repo, number string,
	summaryCommentID int64, headSHA string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		var t ReviewTarget
		err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).First(&t).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			t = ReviewTarget{Platform: platform, Owner: owner, Repo: repo, Number: number}
		}
		if summaryCommentID > 0 {
			t.LastSummaryCommentID = summaryCommentID
		}
		if headSHA != "" {
			t.LastHeadSHA = headSHA
		}
		return tx.Save(&t).Error
	})
}

// GetLastReviewedDiff 查询同 PR 最近一次 done 任务的 diff 文本（增量审查 diff-of-diffs）。
// 返回空字符串表示无前置审查记录。
func (s *Store) GetLastReviewedDiff(ctx context.Context, platform, owner, repo, number string) (string, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ?",
			platform, owner, repo, number, StatusDone).
		Order("created_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if t.ResultJSON == "" {
		return "", nil
	}
	// 从 result_json 中提取 diff_text 字段（轻量解析，不反序列化整个结构）
	var payload struct {
		DiffText string `json:"diff_text"`
	}
	if err := jsonUnmarshal([]byte(t.ResultJSON), &payload); err != nil {
		return "", nil // result_json 格式异常不阻塞审查
	}
	return payload.DiffText, nil
}

// jsonUnmarshal 是 encoding/json.Unmarshal 的包内别名，便于测试 mock。
var jsonUnmarshal = jsonUnmarshalFn

func jsonUnmarshalFn(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
