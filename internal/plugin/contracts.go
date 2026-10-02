package plugin

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
)

// 本文件是 workflow 与插件外壳之间的边界契约（v3.5.6 自 adapter_builtin /
// requirement 上提）：runner 装配 WorkflowFactory 交给 compliance 插件外壳时，
// 编排层不应为了类型名反向 import 具体适配器。

// WorkflowOutput R1→R5 workflow 产出（requirement.Output 的 canonical 定义，
// requirement 侧保留同名别名）。
type WorkflowOutput struct {
	Verdict    string // approve | comment | request_changes
	Rule       string // R5.1..R5.5 | R5.C（完备性降级门）
	Spec       *RequirementSpec
	Report     *ComplianceReport
	Merged     *MergedFindings
	PluginsRun []string
	Summary    string // R2 变更摘要
	Elapsed    time.Duration
	Tokens     int
	Reflect    *ReflectSummary // R3.5 反思结果（nil = 未启用）
	// Completeness 完备性裁判产出（v3.11.0 G4；nil = 未启用）。R5 管结论对不对，
	// 完备性管审得全不全（插件缺失/截断/AC 漏判/降级审查），策略词表见
	// CompletenessPolicy（annotate 默认 / demote 降级门 rule=R5.C）。
	Completeness *CompletenessReport
	Debug        map[string]string // 阶段产物（ARGUS_DEBUG=1 时由 runner 留存）
}

// ReflectSummary R3.5 反思结果（WorkflowOutput.Reflect；nil = 未启用或无 findings）。
type ReflectSummary struct {
	Rounds    int  // 实际批判轮数
	Converged bool // 是否通过 Rubric 收敛（false = 达轮次上限取末稿）
	Removed   int  // 剔除的误报嫌疑条数
	Adjusted  int  // 修正（file/line/severity/comment）的条数
	Kept      int  // 保留条数
	// RemovedPrints 被剔除条目的指纹（File+Comment）：runner 据此做 regret 跨轮
	// 入库——产出 vs 终审的差值即误报历史，供后续轮次标注可信度。
	RemovedPrints []string
	// RemovedFindings 被剔除的完整条目（含 By/File/Comment）：runner 据此做
	// 模式级 PatternRegret（学习环）。
	RemovedFindings []Finding
	// DroppedNoEvidence 证据门 strict 模式下因缺 evidence/counterexample 剔除的条数。
	DroppedNoEvidence int
	// DegradedNoEvidence 证据门 soft 模式下因缺证据强制降为 low 的条数。
	DegradedNoEvidence int
}

// WorkflowRunner workflow 的最小接口（窄依赖：适配器外壳只看到 Run）。
type WorkflowRunner interface {
	Run(ctx context.Context, task Task, budget *TokenBudget) (*WorkflowOutput, error)
}

// WorkflowFactory 每任务构造 workflow（budget 决定其 LLM 客户端的预算累计器）。
type WorkflowFactory func(budget *TokenBudget) WorkflowRunner

// ToolsHook R4 复用审查工具表的钩子（bootstrap 由 runner 注入；nil = 单轮调用模式）。
// 返回 (工具表, 策略提示, 错误)；每次调用应重建工具表（toolprior 限流计数按尝试重置）。
type ToolsHook func(ctx context.Context, task Task, budget *TokenBudget) ([]tool.BaseTool, string, error)

// ---------- R3/R4 与 QA 共用工具 wire 契约（v3.102.0 单源） ----------

// ToolGetFileDesc get_file 工具描述（两面共用，防措辞漂移——此前 R3/QA 各持
// 一份已经走样）。
const ToolGetFileDesc = "读取仓库某文件在 PR head 提交时的完整内容，用于核实 diff 中看不到的实现。" +
	"参数 path 为仓库相对路径。返回内容每行带行号前缀（行号|内容），引用证据 file:line 时使用该行号。"

// ToolFileIn/ToolFileOut get_file 工具入出参（此前 requirement/qa 各持同形
// getFileIn/getFileOut 与 fileIn/fileOut）。
type ToolFileIn struct {
	Path string `json:"path" jsonschema:"description=仓库相对路径"`
}

type ToolFileOut struct {
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}
