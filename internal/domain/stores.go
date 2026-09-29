package domain

import (
	"context"
	"time"

	"argus/internal/learning"
)

// PrintUpsert 批量入库的一条输入（指纹 + 本轮意见）。
type PrintUpsert struct {
	FP      string
	Finding Finding
}

// PrintUpsertResult 批量入库的逐条结果。
// Prior 为入库前状态（nil = 全新指纹），供 Merger 读取 TimesSeen/RegretCount/原文。
type PrintUpsertResult struct {
	Prior      *PrintState
	Suppressed bool
	Err        error // 逐条错误：单条失败不拖垮整批（与单条 UpsertPrint 语义一致）
}

// PrintStore 指纹持久化的最小接口（由 storage.Store 实现；Merger 不 import storage，
// 避免 plugin ↔ storage 循环依赖）。契约住在 domain：消费方（plugin.Merger）与
// 实现方（storage）都只依赖本叶子包。
type PrintStore interface {
	// GetPrintState 返回既有指纹状态；不存在返回 (nil, nil)。
	GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*PrintState, error)
	// UpsertPrint 跨轮次合并入库；返回该指纹当前是否 suppressed（true 时本轮报告剔除该条）。
	UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
		f Finding, taskID string, at time.Time) (suppressed bool, err error)
	// UpsertPrints 批量入库：整批共用一次进程内写锁，避免多意见 PR 逐条抢 writeMu。
	// 逐条语义与 UpsertPrint 相同（含近重复并入/别名/抑制状态延续）；返回与 items 等长。
	UpsertPrints(ctx context.Context, platform, owner, repo, number, taskID string,
		at time.Time, items []PrintUpsert) []PrintUpsertResult
}

// PatternStore 模式级学习环读写接口（由 storage.Store 实现）。
// LearningEnabled=false 时 Merger/Runner 全部旁路，可为 nil。
// 模式数学与状态投影（PatternKey/Confidence/learning.PatternState 等）在 internal/learning。
type PatternStore interface {
	// ListLowConfidencePatterns 拉取本仓库低于 maxConfidence 且样本 ≥ minSamples 的模式。
	ListLowConfidencePatterns(ctx context.Context, platform, owner, repo string,
		minSamples int, maxConfidence float64, limit int) ([]learning.PatternState, error)
	// PatternPosted 意见进入报告后记账。
	PatternPosted(ctx context.Context, platform, owner, repo, pluginName string, f Finding, at time.Time) error
}
