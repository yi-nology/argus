package plugin

import "time"

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
)

func obsTaskTotal(platform, kind, status string) {
	if HookTaskTotal != nil {
		HookTaskTotal(platform, kind, status)
	}
}

func obsStageDuration(stage string, d time.Duration) {
	if HookStageDuration != nil {
		HookStageDuration(stage, d)
	}
}

func obsPluginDuration(name string, d time.Duration) {
	if HookPluginDuration != nil {
		HookPluginDuration(name, d)
	}
}

func obsPluginFailure(name string) {
	if HookPluginFailure != nil {
		HookPluginFailure(name)
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

// ReportPluginFailure 供 poster 等非插件路径记失败（argus_plugin_failures_total）。
func ReportPluginFailure(name string) { obsPluginFailure(name) }

// ReportTaskTotal 供 runner 记任务终态。
func ReportTaskTotal(platform, kind, status string) { obsTaskTotal(platform, kind, status) }

// ReportStageDuration 供 runner 记阶段耗时。
func ReportStageDuration(stage string, d time.Duration) { obsStageDuration(stage, d) }
