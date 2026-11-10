// models.go —— tasks / finding_prints / review_targets 三张核心表的模型定义
// 与状态词表（v6.2.0 自 store.go 拆出：store.go 只留 Store 生命周期与装配面）。
// 操作面按域归位：tasks→store_task.go，finding_prints→prints/printalias.go，
// review_targets→reviewtarget.go。

package storage

import "time"

// Task tasks 表记录。Status 词表钉死：pending / running / done / failed / stale（§10.4）。
// idx_budget 复合索引（v5.4.1）：RepoMonthlyTokens/MonthlyTokensByRepo 的
// WHERE platform+owner+repo+status+created_at 全命中——此前靠 idx_pr 前缀回表
// 过滤 status/created_at，仓库任务量随月累积后预算闸每次启动任务都扫全量历史。
type Task struct {
	ID       string `gorm:"column:id;primaryKey;size:40"`
	Kind     string `gorm:"column:kind;size:32;not null"`
	Platform string `gorm:"column:platform;size:16;not null;index:idx_pr,priority:1;index:idx_budget,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_pr,priority:2;index:idx_budget,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_pr,priority:3;index:idx_budget,priority:3"`
	Number   string `gorm:"column:number;size:16;not null;index:idx_pr,priority:4"`
	HeadSHA  string `gorm:"column:head_sha;size:64;index"`
	// EventType 触发本任务的平台事件（webhook 填 cr.opened/...；CLI 任务为空）
	EventType string `gorm:"column:event_type;size:32"`
	// EventID = 平台 delivery ID（X-Gitea-Delivery 等），幂等键。
	// 缺 header 时保持 NULL（唯一索引允许多个 NULL；置空字符串会误判 duplicate）。
	// 注意：MySQL 8.0+ / SQLite 支持 UNIQUE 列多 NULL；MariaDB 10.5- 行为不同，需 COALESCE 兼容。
	EventID    *string `gorm:"column:event_id;size:128;uniqueIndex"`
	Status     string  `gorm:"column:status;size:16;not null;default:pending;index;index:idx_budget,priority:4"`
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

	CreatedAt  time.Time  `gorm:"column:created_at;index;index:idx_budget,priority:5"`
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
	// 封顶 10）：定位缓存丢失或评论列表 API 故障时可直连更新（收尾批修复）。
	KnownCommentIDs string `gorm:"column:known_comment_ids;size:256"`
	LastHeadSHA     string `gorm:"column:last_head_sha;size:64"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (ReviewTarget) TableName() string { return "review_targets" }
