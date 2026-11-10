// types.go —— 影响面快照的数据契约。Snapshot JSON 落 tasks.artifacts_json
// （artifact 键 impact_view），是 MCP get_impact / API / HTML 三出口的共同
// 数据面；改字段只加不改（消费方按字段名取值，缺字段零值安全）。

package impactview

import "time"

// SnapshotVersion 快照结构版本（破坏性变更时递增；消费方按版本分支）。
const SnapshotVersion = 1

// 关系词表（Edge.Rel）。
const (
	RelImport = "import"  // 受影响方 import 了改动包（L1 反向闭包方向已折算：From=改动源）
	RelCaller = "caller"  // 受影响方调用了改动符号（L2）
	RelTestOf = "test_of" // 受影响测试覆盖改动包（L3）
	RelCouple = "coupled" // 历史共变（P2，方向对称）
)

// 节点种类词表（Node.Kind）。
const (
	KindFile    = "file"
	KindPackage = "package"
	KindSymbol  = "symbol"
	KindTest    = "test"
)

// 风险词表（Risk.Level）。
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// 调用方来源档位（Stats.CallerSource，v6.0.0）。
const (
	CallerSrcGraphPrecise = "graph_precise" // 全部经消歧符号图，且索引处精确边模式（类型解析级）
	CallerSrcGraphLexical = "graph_lexical" // 全部经消歧符号图（词法邻接）
	CallerSrcCodeintel    = "codeintel"     // 全部回退 codeintel 词法调用位
	CallerSrcMixed        = "mixed"         // 图与词法回退混合
)

// Snapshot 影响面快照（JSON 序列化面；渲染函数消费同一结构）。
// 边方向契约：From=变更源（改动文件/符号/包），To=受影响方。coupled 除外
// （方向对称，From 恒为改动文件）。
type Snapshot struct {
	Version     int         `json:"version"`
	GeneratedAt time.Time   `json:"generated_at"`
	Risk        Risk        `json:"risk"`
	Changed     []string    `json:"changed"`              // 纳入审查的改动文件（相对路径，有序）
	Nodes       []Node      `json:"nodes"`                // 节点（含 changed 标记的源节点）
	Edges       []Edge      `json:"edges"`                // 边（见方向契约）
	Packages    []PkgImpact `json:"packages,omitempty"`   // 受影响包聚合（含测试覆盖数与对外暴露面）
	Coupling    []Couple    `json:"coupling,omitempty"`   // 历史共变 Top（P2；未开/未命中无此字段）
	FuncDiffs   []FuncDiff  `json:"func_diffs,omitempty"` // 函数/方法级图 diff（v3.185.0；无 base 参照/非 Go 变更无此字段）
	Stats       Stats       `json:"stats"`
	Notes       []string    `json:"notes,omitempty"` // 降级/截断注明（逐层失败不阻断）
}

// Node 影响面节点。
type Node struct {
	ID      string `json:"id"`                // kind 前缀唯一键（file:a/b.go / pkg:dir / sym:file:Recv.Name / test:a_test.go）
	Kind    string `json:"kind"`              // file|package|symbol|test
	Label   string `json:"label"`             // 短标签（渲染用，已收敛为可读形态）
	Changed bool   `json:"changed,omitempty"` // true = 本轮直接改动
	Detail  string `json:"detail,omitempty"`  // 补充信息（file:line / 完整路径 / 调用方函数）
}

// Edge 影响边（From=变更源，To=受影响方；Rel 见词表）。
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rel  string `json:"rel"`
}

// PkgImpact 受影响包聚合（L1 产出，风险分与 HTML 侧栏消费）。
type PkgImpact struct {
	Path    string `json:"path"`          // 仓库内目录路径（根包为 "."）
	Files   int    `json:"files"`         // 包内 Go 文件数（不含测试）
	Tests   int    `json:"tests"`         // 包内 *_test.go 数（0 = 无测试覆盖）
	Exposed bool   `json:"exposed"`       // 是否被受影响集之外的包 import（对外暴露面）
	Depth   int    `json:"depth"`         // 距改动包的依赖跳数（0 = 改动包自身）
	Direct  bool   `json:"direct_change"` // 包内含直接改动文件
}

// Risk 确定性风险评分（算法与理由一并落快照，可复现可解释）。
type Risk struct {
	Level   string   `json:"level"`   // low|medium|high
	Score   int      `json:"score"`   // 0-100
	Reasons []string `json:"reasons"` // 逐条加分依据
}

// Couple 历史共变对（P2）：File 与 Other 在同一 commit 共现 Count 次。
type Couple struct {
	File  string `json:"file"`
	Other string `json:"other"`
	Count int    `json:"count"`
}

// Stats 构建期统计（可观测面）。
type Stats struct {
	GoFiles      int    `json:"go_files"`                // import 图扫描到的 Go 文件数
	Packages     int    `json:"packages"`                // 仓库内含 Go 文件的目录数
	Symbols      int    `json:"symbols,omitempty"`       // 触达符号数（L2）
	CallSites    int    `json:"call_sites"`              // 调用方命中数（L2，词法+AST 合并后）
	CallerSource string `json:"caller_source,omitempty"` // 调用方来源档位（v6.0.0：graph_precise|graph_lexical|codeintel|mixed）
	Commits      int    `json:"commits,omitempty"`       // 共变扫描的 commit 数（P2）
	FuncDiffs    int    `json:"func_diffs,omitempty"`    // 函数级 diff 条数（v3.185.0）
	Truncated    bool   `json:"truncated,omitempty"`     // 任一层触发封顶截断
	TookMS       int64  `json:"took_ms"`
}

// 容量封顶（防巨型仓库/巨型 PR 把快照与渲染吃爆；触发即记 Notes+Truncated）。
const (
	MaxImportFiles    = 4000 // L1 扫描的 Go 文件上限（与 codeintel.NewIndex 同量级）
	MaxPackages       = 400  // 受影响包闭包上限（BFS 展开封顶）
	MaxImportDepth    = 3    // 反向闭包跳数
	MaxSymbols        = 15   // 触达符号入快照数
	MaxCallersPerSym  = 8    // 单符号调用方数
	MaxCoupling       = 10   // 共变对上限
	MinCoupleCount    = 3    // 共变入榜最低共现次数
	MaxMermaidNodes   = 24   // mermaid 节点上限（超出聚合到包级）
	MaxMermaidRunes   = 4400 // mermaid 源字符上限（Gitea/Forgejo 默认 5000 上界留余量）
	DefaultCommits    = 100  // 共变扫描默认深度
	MaxCommits        = 1000 // 共变扫描深度上限
	MaxPromptPackages = 12   // prompt 段包清单/无测试包清单展示封顶（预算守卫）
)
