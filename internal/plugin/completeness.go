// completeness.go —— 完备性裁判契约（v3.11.0，G4）。
// 类型放 plugin（契约层）：WorkflowOutput 携带 Completeness 贯穿 runner/渲染层；
// 判定逻辑在 workflow/requirement（纯确定性，读运行时事实，不调模型）。
package plugin

// 完备性检查项词表（报告/指标对齐用）。
const (
	CheckDiffTruncated     = "diff_truncated"      // diff 被截断，仅覆盖保留部分
	CheckPluginsMissing    = "plugins_missing"     // 启用插件有未实际产出者（超时/熔断/预算/错误）
	CheckLockPluginMissing = "lock_plugin_missing" // lockRepoConfig 插件未产出（高危）
	CheckACUncovered       = "ac_uncovered"        // R1 验收条目未被 R3 判定覆盖
	CheckUnverifiableRatio = "unverifiable_ratio"  // R3 unverifiable 占比过半
	CheckReviewerDegraded  = "reviewer_degraded"   // reviewer 自报审查降级（ReAct 落单轮等）
)

// CompletenessPolicy 词表（ARGUS_COMPLETENESS_POLICY）。
const (
	// CompletenessAnnotate 默认：失败项只追加报告降级注记，不改 verdict。
	CompletenessAnnotate = "annotate"
	// CompletenessDemote 降级门：任一检查不过时 APPROVE→COMMENT（rule=R5.C）。
	CompletenessDemote = "demote"
)

// CompletenessItem 单条检查结果。
type CompletenessItem struct {
	Check  string `json:"check"` // 词表见上
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CompletenessReport 完备性裁判产出（WorkflowOutput.Completeness，报告渲染消费）。
type CompletenessReport struct {
	Items    []CompletenessItem `json:"items"`
	OK       bool               `json:"ok"`       // 全部通过
	Failures []string           `json:"failures"` // 失败项可读描述（附注/降级依据）
}

// DegradedNotes 失败项转报告降级注记行（annotate 动作）。
func (r *CompletenessReport) DegradedNotes() []string {
	if r == nil || r.OK {
		return nil
	}
	return r.Failures
}
