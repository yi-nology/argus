// contract_alias.go —— 契约类型桥（v3.173.0 引擎/契约内聚拆分的接线件）：
// 契约类型留驻 internal/plugin（全仓公共面），引擎经本别名组引用——
// 门体内未限定符的历史写法零改动。别名与被代理类型同一（Go 类型别名语义），
// 跨包传递无装箱无转换。
package dispatch

import "github.com/yi-nology/argus/internal/plugin"

type (
	// AgentManifest 插件清单（契约）。
	AgentManifest = plugin.AgentManifest
	// AgentPlugin 插件执行接口（契约）。
	AgentPlugin = plugin.AgentPlugin
	// Result 单插件执行产物（契约）。
	Result = plugin.Result
	// Task 审查任务上下文（契约）。
	Task = plugin.Task
	// PRContext PR 上下文（契约）。
	PRContext = plugin.PRContext
	// RepoConfig 仓库主权配置（契约）。
	RepoConfig = plugin.RepoConfig
	// SquadDecl 编队声明（plugins.yaml）。
	SquadDecl = plugin.SquadDecl
	// Finding 审查意见（domain 契约的插件别名）。
	Finding = plugin.Finding
	// TokenBudget 任务内 token 累计器（别名留 plugin，Task.Budget 契约面）。
	TokenBudget = plugin.TokenBudget
	// MergedFindings 合并产物（契约侧输出类型）。
	MergedFindings = plugin.MergedFindings
)

// 引擎内部使用的契约侧谓词/围栏（函数值桥；签名即契约）。
var (
	MatchAnyGlob = plugin.MatchAnyGlob
	ListOverlap  = plugin.ListOverlap
	DataFence    = plugin.DataFence
)

// 严重度词表与指纹谓词（severity.go 契约侧单源）。
const (
	SeverityLow    = plugin.SeverityLow
	SeverityMedium = plugin.SeverityMedium
	SeverityHigh   = plugin.SeverityHigh
	// DegradationMarker 降级注明标记（severity.go）。
	DegradationMarker = plugin.DegradationMarker
	// SquadModeConsensus 黑板共识模式词表（config.go）。
	SquadModeConsensus = plugin.SquadModeConsensus
)

var (
	NormalizeSeverity = plugin.NormalizeSeverity
	HigherSeverity    = plugin.HigherSeverity
	FingerprintNorm   = plugin.FingerprintNorm
	// 引擎内部沿用的契约侧谓词（types.go 导出版；小写桥保门体零改动）。
	matchAnyGlob = plugin.MatchAnyGlob
	listOverlap  = plugin.ListOverlap
)

// 成本词表与分层/观测助手（types.go·hooks.go 契约侧）。
const (
	CostStandard = plugin.CostStandard
	CostHeavy    = plugin.CostHeavy
	LayerMerger  = plugin.LayerMerger
)

var (
	obsPluginDuration = plugin.ObsPluginDuration
	obsFindings       = plugin.ObsFindings
	SeverityRank      = plugin.SeverityRank
	Fingerprint       = plugin.Fingerprint
)

const ArtifactChangeSummary = plugin.ArtifactChangeSummary

// RoutingRule 条件启用声明（config.go 契约）。
type RoutingRule = plugin.RoutingRule

// 插件形态词表（types.go）。
const (
	KindBuiltin = plugin.KindBuiltin
	KindCLI     = plugin.KindCLI
	KindRemote  = plugin.KindRemote
	KindACPX    = plugin.KindACPX
)

// 技能词表（types.go）。
const (
	SkillCodeReview  = plugin.SkillCodeReview
	SkillVisualCheck = plugin.SkillVisualCheck
)

var EmptyRepoConfig = plugin.EmptyRepoConfig

// Trigger 触发声明（types.go 契约）。
type Trigger = plugin.Trigger

// DiffContext 增量 diff 上下文（types.go 契约）。
type DiffContext = plugin.DiffContext

// DiffFile diff 单文件视图（types.go 契约）。
type DiffFile = plugin.DiffFile
