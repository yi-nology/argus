// Package storage 持久化（design §5.2）：GORM + SQLite 单文件起步（dialect 可切 MySQL）。
// 七张表（+qa_sessions/leader_leases/pattern_signals/print_aliases，另有 comment_deliveries/schema_migrations 辅助表）解决四个隐含承诺：
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
	// ReviewPosted 完整评审已发成功标记（v3.36.0）：poster 重跑据此短路
	// ListReviews 平台查询（查库代替查平台，大 PR 提速；AutoMigrate 增量加列）。
	ReviewPosted bool `gorm:"column:review_posted;default:false"`
	// Priority 队列优先级（v3.40.0）：大值先认领。10=人类在等（评论重审/API
	// 手工）、5=webhook push、0=poller 批量（缺省）。AutoMigrate 增量加列。
	Priority int `gorm:"column:priority;default:0"`
	// DebugJSON 调试产物（ARGUS_DEBUG=1 时留存 R1 spec/R2 摘要/R3 原始输出）
	DebugJSON string `gorm:"column:debug_json;type:text"`
	// StageArtifacts 阶段产物 checkpoint（v3.12.0 G2 任务级断点续审）：R1 spec/
	// R2 摘要在成功完成后即时持久化（JSON map）；同任务重试（failed 退避/心跳
	// 过期重置）复用，省掉已成功阶段的重复 LLM 调用。只在任务内有效——新任务
	// 永远全量重跑，跨任务复用会踩需求文本/diff 已变化的 staleness。
	StageArtifacts string `gorm:"column:stage_artifacts;type:text"`

	// RunnerID 领取本任务的实例 ID（心跳归属；多副本共库时滚动发布不误伤他实例在途任务）
	RunnerID string `gorm:"column:runner_id;size:64;index"`
	// HeartbeatAt 最近心跳（worker 池周期刷新 running 任务；超时未续才会被重置回 pending）
	HeartbeatAt *time.Time `gorm:"column:heartbeat_at"`

	// Attempt 已失败重试次数（v3.8.0 failed 退避策略）：FinishTask(failed) 时 +1，
	// ResetFailedForRetry 按 ARGUS_TASK_MAX_RETRIES 上限把到期的 failed 重置 pending。
	Attempt int `gorm:"column:attempt;default:0"`
	// NextRetryAt 下次自动重试时间（指数退避 5m×3^attempt；nil = 不重试）。
	NextRetryAt *time.Time `gorm:"column:next_retry_at"`

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
	// Plugin 产出该意见的插件名（f.By；多插件合并时取首个）。学习环 suppress 信号归属用。
	Plugin string `gorm:"column:plugin;size:64"`

	FirstTaskID string    `gorm:"column:first_task_id;size:40"`
	LastTaskID  string    `gorm:"column:last_task_id;size:40"`
	TimesSeen   int       `gorm:"column:times_seen"`
	RegretCount int       `gorm:"column:regret_count"` // 被 R3.5 反思剔除的累计次数（误报历史）
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

	LastSummaryCommentID int64 `gorm:"column:last_summary_comment_id"`
	// KnownCommentIDs 本 bot 历史报告评论 ID 缓存（JSON [id...]，新追加旧淘汰，
	// 封顶 10）：报告评论被顶出 ListIssueComments 第一页后扫描兜底不中，缓存可
	// 直连更新（收尾批修复，绕开 SDK 分页限制）。
	KnownCommentIDs string `gorm:"column:known_comment_ids;size:256"`
	LastHeadSHA     string `gorm:"column:last_head_sha;size:64"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (ReviewTarget) TableName() string { return "review_targets" }

// Store 存储门面。SQLite 下用连接数=1 + 写锁实现"查询与落库同一事务"的串行语义
// （design §5.2：SQLite 单写串行下单事务即闭环）；MySQL 下并发正确性由行锁/
// SKIP LOCKED/唯一索引承担，进程级互斥只制造吞吐天花板，写锁仅 SQLite 生效
// （v3.53.0 writeLock/writeUnlock）。
type Store struct {
	db      *gorm.DB
	writeMu sync.Mutex
	sqlite  bool
	// RunnerID 本实例标识（Open 后由 main 赋值；claim/心跳用它归属 running 任务）
	RunnerID string
}

// writeLock/writeUnlock 进程内写串行门（v3.53.0 SQLite 感知）：writeMu 为 SQLite
// 单写者语义而生。MySQL 路径各写点的并发正确性均有 DB 侧机制承担——claim 走
// SKIP LOCKED 事务、webhook 建任务对 target 行 FOR UPDATE、幂等靠唯一索引；
// 单任务执行体唯一，attempt 读改写与 checkpoint 合并无同任务并发。多副本
// MySQL 横向扩容前的必要解锁。
func (s *Store) writeLock() {
	if s.sqlite {
		s.writeMu.Lock()
	}
}

func (s *Store) writeUnlock() {
	if s.sqlite {
		s.writeMu.Unlock()
	}
}

// ErrNotFound 查询无结果的边界哨兵：上层判 404/无此对象以此为准，不再感知
// gorm.ErrRecordNotFound——ORM 错误语义不出 storage 包。
var ErrNotFound = errors.New("storage: record not found")

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
		// glebarez/sqlite 纯 Go 驱动；缺省注入 busy_timeout + WAL（§5.2 并发注意）。
		// DSN 已带 query 时以 & 追加（v3.9.1：此前含 ? 的 DSN 会静默丢失
		// busy_timeout/WAL，并发写行为与默认路径漂移）。
		if strings.Contains(dsn, "?") {
			dsn += "&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		} else {
			dsn += "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		}
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 sqlite 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		// WAL 下读写可并发（单写多读），连接数 1 会让大查询/大 DELETE 队头阻塞
		// 心跳读，错过 stale 判定窗口触发误重置双跑（深挖修复）。写-写竞争由
		// 进程内 writeMu + busy_timeout(10s) 兜底；幂等/去重正确性来自唯一索引
		// 与事务，不依赖连接数=1。
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4) // idle=max-open 避免连接反复开关的 WAL 开销
		// SQLite 不设 ConnMaxLifetime：单文件单连接，文件替换由 OS 语义保证一致性
		s.db, s.sqlite = db, true
	}
	if err := s.db.AutoMigrate(&Task{}, &FindingPrint{}, &PrintAlias{}, &ReviewTarget{}, &LeaderLease{}, &QASession{}, &PatternSignal{}, &SchemaMigration{}, &CommentDelivery{}); err != nil {
		return nil, fmt.Errorf("storage: 自动迁移失败: %w", err)
	}
	if !s.sqlite {
		if err := s.upgradeMySQLLongText(); err != nil {
			return nil, err
		}
	}
	// 指纹 v2 存量迁移（v3.7.0）：v1 行按存量 file/line/comment 重算回写。
	// v3.8.0 起由 SchemaMigration 哨兵门控（成功一次后不再每次启动全表扫）；
	// 失败不阻断启动（留待下次重试），只告警。
	if n, merr := s.ensureMigrationApplied(context.Background(), "fingerprint_v2", s.migratePrintFingerprintsV2); merr != nil {
		s.db.Logger.Warn(context.Background(), "指纹 v2 迁移失败（下次启动重试）: %v", merr)
	} else if n > 0 {
		s.db.Logger.Info(context.Background(), "指纹 v2 迁移完成: %d 行", n)
	}
	// print_aliases PR 域迁移（v3.9.2）：alias_fp 全局主键 → (PR, alias_fp) 复合
	// 主键——两个 PR 同款措辞漂移不再撞主键回滚；存量按 print_fp 反查 PR 回填。
	if n, merr := s.ensureMigrationApplied(context.Background(), "print_alias_pr_scope", s.migratePrintAliasPRScope); merr != nil {
		s.db.Logger.Warn(context.Background(), "print_aliases PR 域迁移失败（下次启动重试）: %v", merr)
	} else if n > 0 {
		s.db.Logger.Info(context.Background(), "print_aliases PR 域迁移完成: %d 行", n)
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

// NewTaskID 生成任务 ID（service/poller 共用单一出处）。
func NewTaskID() string {
	return "t-" + strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16] + time.Now().Format("01021504")
}

// DecodeJSONMap 解码 Task 的 JSON 对象列（artifacts/debug/stage_artifacts）：
// 空串/坏数据安全返回空表。runner 写入与 service/MCP 读取共用单源（v3.99.0，
// 此前两端各持一份同形解码，字段契约无钉）。
func DecodeJSONMap(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// wherePR PR 四元组定位条件（storage 各表通用单源，v3.99.0：8 处同形
// Where 字符串收敛）。
func wherePR(q *gorm.DB, platform, owner, repo, number string) *gorm.DB {
	return q.Where("platform = ? AND owner = ? AND repo = ? AND number = ?", platform, owner, repo, number)
}

// quietDB 预期内"查无记录"的路径专用：GORM 默认把 ErrRecordNotFound 打成红色错误日志，
// worker 每 500ms 空轮询一次会刷屏掩盖真实告警——这些路径静默。
func (s *Store) quietDB() *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: logger.Discard})
}

// retryBackoff 第 attempt 次失败的退避：base×3^(attempt-1)，封顶 1h。
func retryBackoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 3
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

// GetReviewTarget 查询（不存在返回 nil,nil）。
func (s *Store) GetReviewTarget(ctx context.Context, platform, owner, repo, number string) (*ReviewTarget, error) {
	var t ReviewTarget
	err := wherePR(s.quietDB().WithContext(ctx), platform, owner, repo, number).First(&t).Error
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
	s.writeLock()
	defer s.writeUnlock()
	return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		var t ReviewTarget
		err := wherePR(tx, platform, owner, repo, number).First(&t).Error
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
