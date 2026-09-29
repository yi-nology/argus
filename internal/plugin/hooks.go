package plugin

import (
	"time"

	"git.example.com/enjoydream/agentkit/agentrun"
	"git.example.com/enjoydream/ekit/observability/logx"
)

// 观测钩子：本包不直接依赖 prometheus，由 obs 包在装配期注入实现（design §10.1）。
// 保持 L4 纯逻辑、可在无指标环境（单测）下运行。
var (
	// HookTaskTotal 任务终态计数（platform, kind, status）。
	HookTaskTotal func(platform, kind, status string)
	// HookStageDuration 阶段耗时（stage: R1..R5 | plugin:<name> | total）。
	HookStageDuration func(stage string, d time.Duration)
	// HookPluginDuration 插件耗时。
	HookPluginDuration func(plugin string, d time.Duration)
	// HookPluginFailure 插件失败计数（含 poster）。
	HookPluginFailure func(plugin string)
	// HookLLMTokens LLM token 消耗（stage, model）——只做观测，不参与预算判定。
	HookLLMTokens func(stage, model string, tokens int)
	// HookFindings 审查产出分布（severity, plugin）。
	HookFindings func(severity, plugin string, n int)
	// HookReflectDrop R3.5 证据门 strict 丢弃。
	HookReflectDrop func(reason string)
	// HookReflectDegrade R3.5 证据门 soft 降级。
	HookReflectDegrade func(reason string)
	// HookLayerExec 分层执行探针：层被真正走到时 +1（活检；名义存在但零执行 = 告警）。
	HookLayerExec func(layer string)
	// HookVerdict 裁决分布（platform, kind, verdict, rule）——R5 真值表命中分布。
	HookVerdict func(platform, kind, verdict, rule string)
	// HookFenceNeutralized 数据区围栏中和次数（= 检测到注入特征文本的信号量）。
	HookFenceNeutralized func(n int)
	// HookPluginSkipped 插件跳过原因分布（circuit/budget/canceled）。
	HookPluginSkipped func(reason string)
	// HookCommentRoute 评论意图路由分布（route: review|ask, via: keyword|llm|fallback）。
	HookCommentRoute func(route, via string)
	// HookTaskDeduped 任务创建去重计数（reason: event_id|last_head_sha|in_flight）。
	HookTaskDeduped func(reason string)
	// HookBestOfN best-of-N 采样结果分布（k/n：full=全部成功、partial=部分成功）。
	HookBestOfN func(plugin string, result string)
)

func obsPluginDuration(name string, d time.Duration) {
	if HookPluginDuration != nil {
		HookPluginDuration(name, d)
	}
}

func obsLLMTokens(stage, model string, tokens int) {
	if HookLLMTokens != nil {
		HookLLMTokens(stage, model, tokens)
	}
}

func obsFindings(severity, name string, n int) {
	if HookFindings != nil && n > 0 {
		HookFindings(severity, name, n)
	}
}

// ReportLLMTokens 供 llm 包记账（argus_llm_tokens_total，只观测不判定）。
func ReportLLMTokens(stage, model string, tokens int) {
	if tokens > 0 {
		obsLLMTokens(stage, model, tokens)
	}
}

// ReportPluginFailure 插件失败计数（argus_plugin_failures_total，含 poster 等非插件路径）。
func ReportPluginFailure(name string) {
	if HookPluginFailure != nil {
		HookPluginFailure(name)
	}
}

// ReportTaskTotal 供 runner 记任务终态。
func ReportTaskTotal(platform, kind, status string) {
	if HookTaskTotal != nil {
		HookTaskTotal(platform, kind, status)
	}
}

// ReportStageDuration 供 runner 记阶段耗时。
func ReportStageDuration(stage string, d time.Duration) {
	if HookStageDuration != nil {
		HookStageDuration(stage, d)
	}
}

// ReportReflectDrop R3.5 证据门 strict 丢弃（reason=missing_evidence|missing_counterexample|evidence_path_out_of_scope）。
func ReportReflectDrop(reason string) {
	if HookReflectDrop != nil {
		HookReflectDrop(reason)
	}
}

// ReportReflectDegrade R3.5 证据门 soft 降级。
func ReportReflectDegrade(reason string) {
	if HookReflectDegrade != nil {
		HookReflectDegrade(reason)
	}
}

// ReportLayerExec 分层活检：层实际执行时记一次。
// 词表钉死：diffprep | R1 | R2 | R3 | R35 | R4 | R5 | merger | poster。
func ReportLayerExec(layer string) {
	if HookLayerExec != nil && layer != "" {
		HookLayerExec(layer)
	}
}

// ReportVerdict R5 裁决分布（argus_verdicts_total）。
func ReportVerdict(platform, kind, verdict, rule string) {
	if HookVerdict != nil {
		HookVerdict(platform, kind, verdict, rule)
	}
}

// ReportFenceNeutralized 围栏中和计数（注入特征信号，argus_fence_neutralized_total）。
func ReportFenceNeutralized(n int) {
	if HookFenceNeutralized != nil && n > 0 {
		HookFenceNeutralized(n)
	}
}

// ReportPluginSkipped 插件跳过原因（reason 词表：circuit | budget | canceled）。
func ReportPluginSkipped(reason string) {
	if HookPluginSkipped != nil && reason != "" {
		HookPluginSkipped(reason)
	}
}

// ReportCommentRoute 评论意图路由分布。
func ReportCommentRoute(route, via string) {
	if HookCommentRoute != nil {
		HookCommentRoute(route, via)
	}
}

// ReportTaskDeduped 任务创建去重（reason 词表：event_id | last_head_sha | in_flight）。
func ReportTaskDeduped(reason string) {
	if HookTaskDeduped != nil {
		HookTaskDeduped(reason)
	}
}

// ReportBestOfN best-of-N 采样结果分布（result 词表：full | partial）。
func ReportBestOfN(name, result string) {
	if HookBestOfN != nil {
		HookBestOfN(name, result)
	}
}

// Layer 管线阶段词表（单一出处，v3.46.0 契约显式化）。所有跨包消费阶段名之处
// 一律引用此处常量，禁止裸字符串——新增阶段只需在此追加并扩展 StageTimeline：
//   - 指标 label：ReportStageDuration/ReportLayerExec（与告警规则 label 对齐，改值断 Grafana）；
//   - SSE 进度事件：Workflow.onStage；
//   - LLM 阶段路由键：llm.StageRouter 前缀匹配（LayerR1 命中 LayerR1A），模型覆盖/包装同键；
//   - MCP TaskStages 阶段时间线：service 按 StageTimeline 顺序出表。
const (
	LayerDiffprep = "diffprep"
	LayerR1       = "R1"
	LayerR1A      = "R1a" // R1 的分块提炼子阶段（StageRouter 由 LayerR1 前缀命中）
	LayerR2       = "R2"
	LayerR3       = "R3"
	LayerR35      = "R35"
	LayerR4       = "R4"
	LayerR5       = "R5"
	LayerMerger   = "merger"
	LayerPoster   = "poster"
)

// StageTimeline 阶段时间线的规范顺序（只读词表，消费方勿改写元素；新增阶段在此追加）。
var StageTimeline = []string{LayerR1, LayerR2, LayerR3, LayerR35, LayerR4, LayerR5}

// ToolTraceLogger agentrun 工具轨迹审计日志（tool_result 只落长度不落内容，防
// 敏感代码入库；v3.64.0 自 builtin/stages 两处克隆收口）。prefix 决定事件名
// （<prefix>.tool_call / .tool_result），kv 为附加固定键值（如 plugin 名）。
// log 为 nil 时返回 nil 闭包（agentrun 侧 nil 安全）。
func ToolTraceLogger(log logx.Logger, prefix string, kv ...any) func(agentrun.Event) {
	if log == nil {
		return nil
	}
	return func(e agentrun.Event) {
		switch e.Type {
		case agentrun.EventToolCall:
			log.Info(prefix+".tool_call", append(kv, "tool", e.Tool)...)
		case agentrun.EventToolResult:
			log.Info(prefix+".tool_result", append(kv, "tool", e.Tool, "bytes", len(e.Text))...)
		}
	}
}
