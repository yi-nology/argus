// Package requirement L3 编排层：requirement-compliance workflow（R1→R5，design §4）。
// 原则：编排是确定性 Go 代码——路由/裁决/合并不走 LLM；自主性只给 R3 阶段内 agent（ReAct 调工具）。
package requirement

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/ekit/observability/logx"

	argusctx "argus/internal/context"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
	"argus/internal/plugin"
	"argus/internal/toolpool"
)

// TaskKind 词表（与 IDL TaskKind 对齐）。
const (
	KindRequirementCompliance = "requirement_compliance"
	KindCodeReview            = "code_review"
	KindVisualCheck           = "visual_check"
)

// Verdict 词表。
const (
	VerdictApprove        = "approve"
	VerdictComment        = "comment"
	VerdictRequestChanges = "request_changes"
)

// Output workflow 产出。
type Output struct {
	Verdict    string // approve | comment | request_changes
	Rule       string // R5.1..R5.5
	Spec       *plugin.RequirementSpec
	Report     *plugin.ComplianceReport
	Merged     *plugin.MergedFindings
	PluginsRun []string
	Summary    string // R2 变更摘要
	Elapsed    time.Duration
	Tokens     int
	Debug      map[string]string // 阶段产物（ARGUS_DEBUG=1 时由 runner 留存）
}

// Workflow R1→R5 编排器（首个 Workflow 接口实现，§9.4：接口随 v3.0 抽出）。
type Workflow struct {
	LLM        llm.Generator
	SDK        *argusctx.SDKCtx
	Dispatcher *plugin.Dispatcher
	Merger     *plugin.Merger
	Tools      toolpool.Pool // v3.0 mock 或 nil
	ToolSpecs  []toolpool.ToolSpec
	Knowledge  rag.KnowledgeService             // v3.1 知识检索（nil/Noop = 不挂 search_knowledge）
	SkillText  func(ctx context.Context) string // R3 方法论注入（runner 装配；nil = 无）
	// SkillTool 决策使用模式的 use_skill 工具（ARGUS_SKILL_MODE=tool 时装配；
	// 与 SkillText 互斥——静态注入 vs 渐进披露，双开会让正文重复）。
	SkillTool tool.BaseTool
	Log        logx.Logger

	// R1 需求文本截断上限（字符；<=0 用包内缺省）。截断必须留痕：
	// 在 spec.Ambiguous 追加提示 → R5.3 → 人工确认，不允许静默丢内容。
	ReqBodyMaxChars  int
	ReqIssueMaxChars int

	// R3 工具返回内容上限（rune；<=0 用包内缺省）。随模型窗口从 config 派生。
	GetFileMaxChars  int
	GetIssueMaxChars int
	GetDiffMaxChars  int
}

// skillsForKind 任务类型 → R4 分派的技能词表。
func skillsForKind(kind string) []string {
	switch kind {
	case KindVisualCheck:
		return []string{plugin.SkillVisualCheck}
	default:
		return []string{plugin.SkillCodeReview}
	}
}

// Run 完整链路 R1→R5（requirement_compliance 任务）。
func (w *Workflow) Run(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*Output, error) {
	start := time.Now()
	rc := w.loadRepoConfig(ctx, task)

	debug := map[string]string{}
	// R1 需求解析
	spec, err := w.ParseRequirement(ctx, task)
	if err != nil {
		return nil, err
	}
	if b, e := json.Marshal(spec); e == nil {
		debug["r1_spec"] = string(b)
	}
	// R2 变更理解
	summary, err := w.SummarizeChange(ctx, task)
	if err != nil {
		return nil, err
	}
	debug["r2_summary"] = summary
	// 注入 R2 产物到 Artifacts，R4 插件可复用（公共前缀省 token）
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts["change_summary"] = summary
	task.Requirement = spec

	// R3 完成度判定
	report, raw, err := w.CheckCompliance(ctx, task, spec, summary)
	if err != nil {
		return nil, err
	}
	if raw != "" {
		debug["r3_raw"] = raw
	}
	if report != nil {
		if b, e := json.Marshal(report); e == nil {
			debug["r3_checks"] = string(b)
		}
	}

	// R4 专家委派 + R5 汇总裁决
	out, err := w.finish(ctx, task, budget, rc, start, spec, report, summary)
	if out != nil {
		out.Debug = debug
	}
	return out, err
}

// RunReviewOnly 仅 R4+R5（code_review / visual_check 任务：无需求清单，R5.1/R5.3 需求条件跳过）。
func (w *Workflow) RunReviewOnly(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*Output, error) {
	start := time.Now()
	rc := w.loadRepoConfig(ctx, task)
	// R2 摘要供 R4 插件复用
	summary, err := w.SummarizeChange(ctx, task)
	if err != nil {
		return nil, err
	}
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts["change_summary"] = summary
	out, err := w.finish(ctx, task, budget, rc, start, nil, nil, summary)
	if out != nil {
		out.Debug = map[string]string{"r2_summary": summary}
	}
	return out, err
}

// finish = R4 专家委派 + R5 确定性真值表。
func (w *Workflow) finish(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget,
	rc *plugin.RepoConfig, start time.Time, spec *plugin.RequirementSpec, report *plugin.ComplianceReport,
	summary string) (*Output, error) {

	stageStart := time.Now()
	changedFiles := diffPaths(task.Diff)
	skills := skillsForKind(task.Kind)

	// R4：确定性路由 → 预算裁剪 → fan-out
	sel := w.Dispatcher.Select(&task.PR, changedFiles, skills, rc)
	var pruned []string
	sel, pruned = w.Dispatcher.PruneByBudget(sel, budget)
	outcomes := w.Dispatcher.Run(ctx, task, sel, budget)
	plugin.ReportStageDuration("R4", time.Since(stageStart))

	// Merger：归一化 + 指纹去重 + 抑制过滤（store 由 Merger 持有）
	merged, err := w.Merger.Merge(ctx, task.PR, task.ID, outcomes, time.Now())
	if err != nil {
		return nil, err
	}
	for _, p := range pruned {
		merged.Degraded = append(merged.Degraded, p+" 未参与（token 预算裁剪）")
	}

	// R5：确定性真值表（按序求值，首条命中即短路；模型自评 verdict 不参与）
	r5Start := time.Now()
	verdict, rule := Verdict(spec, report, merged.Findings)
	plugin.ReportStageDuration("R5", time.Since(r5Start))

	out := &Output{
		Verdict:    verdict,
		Rule:       rule,
		Spec:       spec,
		Report:     report,
		Merged:     merged,
		PluginsRun: plugin.OutcomeNames(outcomes),
		Summary:    summary,
		Elapsed:    time.Since(start),
	}
	out.Tokens = w.LLM.UsedTokens()
	return out, nil
}

// Verdict R5 裁决真值表（design §4，按序求值首条命中即短路）：
//
//	R5.1 missing+partial>0 → REQUEST_CHANGES（需求未完成，一票否决）
//	R5.2 存在 high finding → REQUEST_CHANGES
//	R5.3 unverifiable>0 或 Ambiguous>0 或 AC 总数 0 或 scope creep 非空 → COMMENT（人工确认）
//	R5.4 存在 medium/low finding → COMMENT
//	R5.5 其余 → APPROVE
//
// 注意：R5.3 的 Ambiguous/AC 总数条件取自 R1 的 spec（而非 R3 report）——R3 失败/缺席时
// 空清单与模糊需求仍必须落入人工确认路径，不能漏进 R5.5（二轮评审修掉的注入洞）。
func Verdict(spec *plugin.RequirementSpec, report *plugin.ComplianceReport, findings []plugin.Finding) (verdict, rule string) {
	var high, midLow, unverifiable int
	for _, f := range findings {
		switch f.Severity {
		case severity.High:
			high++
		case severity.Medium, severity.Low:
			midLow++
		}
	}
	missingPartial := false
	if report != nil {
		for _, c := range report.Checks {
			switch c.Status {
			case plugin.ACMissing, plugin.ACPartial:
				missingPartial = true
			case plugin.ACUnverifiable:
				unverifiable++
			}
		}
	}
	if missingPartial {
		return VerdictRequestChanges, "R5.1"
	}
	if high > 0 {
		return VerdictRequestChanges, "R5.2"
	}
	needHuman := unverifiable > 0 || (report != nil && len(report.ScopeCreep) > 0)
	if spec != nil { // requirement 模式：模糊需求 / 空清单强制人工确认
		needHuman = needHuman || len(spec.Ambiguous) > 0 || len(spec.ACs) == 0
	}
	if needHuman {
		return VerdictComment, "R5.3"
	}
	if midLow > 0 {
		return VerdictComment, "R5.4"
	}
	return VerdictApprove, "R5.5"
}

// loadRepoConfig 仓库主权配置（§3.4：只从默认分支读；失败按无配置 + 告警）。
func (w *Workflow) loadRepoConfig(ctx context.Context, task plugin.Task) *plugin.RepoConfig {
	pr := task.PR
	if pr.DefaultBranch == "" {
		repo, err := w.SDK.GetRepo(ctx, pr.Platform, pr.Owner, pr.Repo)
		if err != nil {
			w.Log.Warn("argus.workflow.repo_fetch_failed", "error", err.Error())
			return plugin.EmptyRepoConfig()
		}
		pr.DefaultBranch = repo.DefaultBranch
	}
	rc, err := w.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := plugin.ResolveRepoConfig(rc, w.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			w.Log.Warn("argus.workflow.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

func diffPaths(dc *plugin.DiffContext) []string {
	if dc == nil {
		return nil
	}
	out := make([]string, 0, len(dc.Files))
	for _, f := range dc.Files {
		out = append(out, f.Path)
	}
	return out
}
