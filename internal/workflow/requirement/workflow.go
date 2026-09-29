// Package requirement L3 编排层：requirement-compliance workflow（R1→R5，design §4）。
// 原则：编排是确定性 Go 代码——路由/裁决/合并不走 LLM；自主性只给 R3 阶段内 agent（ReAct 调工具）。
package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"

	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/reportutil"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/codegraph"
	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/toolpool"

	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
)

// TaskKind 词表（与 IDL TaskKind 对齐）：canonical 定义自 v3.5.6 上提
// plugin.TaskKind*（service/poller 不必为拿常量依赖编排层），本包直接引用。

// Verdict 词表。
const (
	VerdictApprove        = "approve"
	VerdictComment        = "comment"
	VerdictRequestChanges = "request_changes"
)

// Output workflow 产出。canonical 定义自 v3.5.6 上提 plugin.WorkflowOutput
// （compliance 插件外壳与 runner 的边界契约）；别名保包内与既有调用方兼容。
type Output = plugin.WorkflowOutput

// Workflow R1→R5 编排器（首个 Workflow 接口实现，§9.4：接口随 v3.0 抽出）。
type Workflow struct {
	// 输入通道契约（v3.54.0 文档化，防三通道漂移；此前勘察在案「八字段外部预写」）：
	//   ① 装配期构造字段——基础设施引用（LLM/Dispatcher/Merger/SDK/Tools/Knowledge），
	//     buildWorkflow 一次构造；
	//   ② 任务期预写字段——MemoryIndex/RepoGuide/ExternalToolCallLimit/OnStage/
	//     CheckpointSink/SASTOutcomes/CodeGraph/RepoConfig：运行期数据而非装配常量，
	//     runner 每任务 Run 前注入（这正是不用 Options 构造参数表达的原因，v3.54.0 复核定案）；
	//   ③ 调用参数——Run/RunReviewOnly(ctx, task, budget) 携带任务本体与预算。
	// 新增输入先问归属：装配常量→①，任务数据→②，禁止经钩子旁路绕开②。
	LLM llm.Generator
	// R3ChatModel R3 ReAct 专用模型（P6 阶段覆盖；nil = LLM.RawModel()）。
	R3ChatModel model.BaseChatModel
	SDK         *argusctx.SDKCtx
	Dispatcher  *plugin.Dispatcher
	Merger      *plugin.Merger
	Tools       toolpool.Pool // v3.0 mock 或 nil
	ToolSpecs   []toolpool.ToolSpec
	Knowledge   rag.KnowledgeService             // v3.1 知识检索（nil/Noop = 不挂 search_knowledge）
	SkillText   func(ctx context.Context) string // R3 方法论注入（runner 装配；nil = 无）

	// MemoryIndex 本仓历史评审记忆索引（v3.35.0，dream 周期导出 + runner 每任务
	// 装载；空 = 无记忆或功能未启用）。感知面：细节全文走 search_knowledge 检索。
	MemoryIndex string

	// RepoGuide 仓库级评审规范（v3.38.0，runner 按 repo@sha 从平台 API 装载
	// .argus/REVIEW.md；空 = 未启用或仓库未提供）。半可信输入：已经提醒标签
	// 转义预处理，stages 注入侧带对冲句。
	RepoGuide string

	// 详情库（v3.39.0 大输出两级化）：超限工具全文暂存，模型经 get_detail 取回。
	// workflow 每任务构造 → 任务级生命周期；条数/单条上限防 OOM。
	detailMu    sync.Mutex
	detailStore map[string]string
	detailSeq   int

	// OnStage 阶段进度回调（v3.40.0，runner 接线 SSE 发布；nil 安全）。
	OnStage func(stage string)

	// ExternalToolCallLimit 外部 MCP 工具单任务调用上限（v3.41.0 可配；≤0 回退 5）。
	ExternalToolCallLimit int

	// SkillTool 决策使用模式的 use_skill 工具（ARGUS_SKILL_MODE=tool 时装配；
	// 与 SkillText 互斥——静态注入 vs 渐进披露，双开会让正文重复）。
	SkillTool tool.BaseTool
	Log       logx.Logger

	// R1 需求文本截断上限（字符；<=0 用包内缺省）。截断必须留痕：
	// 在 spec.Ambiguous 追加提示 → R5.3 → 人工确认，不允许静默丢内容。
	ReqBodyMaxChars  int
	ReqIssueMaxChars int

	// R3 工具返回内容上限（rune；<=0 用包内缺省）。随模型窗口从 config 派生。
	GetFileMaxChars  int
	GetIssueMaxChars int
	GetDiffMaxChars  int

	// R3.5 报告反思（v3.4，agentkit/reflection）：>0 启用，值为"批判→修订"轮数上限
	//（包内钳制 ≤ reflectMaxRounds）。ReflectModel 为反思专用模型（nil = R3ChatModel
	// → LLM.RawModel() 逐级回退）。ReflectEvidence 证据门：off|soft|strict（默认 off）。
	ReflectRounds   int
	ReflectModel    model.BaseChatModel
	ReflectEvidence string

	// G4 完备性裁判（v3.11.0）：CompletenessPolicy = annotate（默认，只注明）|
	// demote（任一检查不过时 APPROVE→COMMENT，rule=R5.C）；CompletenessRetry =
	// 受限补审（失败插件在剩余预算内重试一次，结果替换不叠加）。
	CompletenessPolicy string
	CompletenessRetry  bool

	// CheckpointSink 阶段产物落库钩子（v3.12.0 G2 断点续审；nil = 不落）：
	// R1/R2 成功完成后即时回调，runner 持久化到 tasks.stage_artifacts；
	// 同任务重试时经 task.Artifacts 的 checkpoint_* 键回流，跳过已成功阶段。
	CheckpointSink func(artifacts map[string]string)

	// RepoConfig 仓库主权配置（v3.15.0：runner 单次加载后注入，min_severity 等
	// review 段行为在 finish 内消费）。nil = 未注入（loadRepoConfig 自行加载，
	// 既有测试路径不受影响）。
	RepoConfig *plugin.RepoConfig
	// SASTOutcomes 确定性 SAST 前置产出（v3.15.0：runner 在工作副本上跑
	// gitleaks/semgrep 后注入；finish 内并入 fan-out outcomes 一起合并）。
	SASTOutcomes []plugin.Outcome
	// CodeGraph Go 符号索引（v3.15.0 P0-1：runner 工作副本上建好后注入；
	// 非 nil 时 r3Tools/R4 ReviewTools 挂 get_symbol/get_references 工具）。
	CodeGraph *codegraph.Index
}

// skillsForKind 任务类型 → R4 分派的技能词表。
func skillsForKind(kind string) []string {
	switch kind {
	case plugin.TaskKindVisualCheck:
		return []string{plugin.SkillVisualCheck}
	default:
		return []string{plugin.SkillCodeReview}
	}
}

// Run 完整链路 R1→R5（requirement_compliance 任务）。
func (w *Workflow) Run(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*Output, error) {
	start := time.Now()
	task.Budget = budget // R3 重试的预算门读取（深挖修复：R1-R3 此前完全不受预算约束）
	rc := w.loadRepoConfig(ctx, task)

	debug := map[string]string{}
	var err error
	// R1 需求解析（v3.12.0 G2：同任务重试优先复用已持久化阶段产物——checkpoint
	// 只在任务内有效，新任务 task.Artifacts 无 checkpoint 键，恒走全量解析）
	spec := w.checkpointSpec(task)
	if spec != nil {
		debug["r1_checkpoint"] = "reused"
	} else {
		spec, err = w.ParseRequirement(ctx, task)
		if err != nil {
			return nil, err
		}
		if b, e := json.Marshal(spec); e == nil {
			w.checkpointSave(task, map[string]string{plugin.ArtifactCkptR1Spec: string(b)})
		}
	}
	if b, e := json.Marshal(spec); e == nil {
		debug[plugin.DebugKeyR1Spec] = string(b)
	}
	// R2 变更理解。失败降级继续（深挖修复）：R2 只产摘要且 R3/R4 携带原始 diff
	// 不依赖它——此前是全链路唯一"一个摘要模型瞬断就 error 掉整个任务"的单点，
	// 与 R1/R3/R4 的失败降级纪律不一致。
	summary, reused := w.summaryWithCheckpoint(ctx, task)
	if reused {
		debug[plugin.DebugKeyR2Checkpoint] = "reused"
	}
	debug[plugin.DebugKeyR2Summary] = summary
	// 注入 R2 产物到 Artifacts，R4 插件可复用（公共前缀省 token）
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts[plugin.ArtifactChangeSummary] = summary
	task.Requirement = spec

	// R3 完成度判定
	report, raw, err := w.CheckCompliance(ctx, task, spec, summary)
	if err != nil {
		return nil, err
	}
	if raw != "" {
		debug[plugin.DebugKeyR3Raw] = raw
	}
	if report != nil {
		if b, e := json.Marshal(report); e == nil {
			debug[plugin.DebugKeyR3Checks] = string(b)
		}
	}

	// R4 专家委派 + R5 汇总裁决
	out, err := w.finish(ctx, task, budget, rc, start, spec, report, summary)
	if out != nil {
		out.Debug = debug
	}
	return out, err
}

// checkpointSpec 任务级 checkpoint 复用：R1 spec（坏 JSON 视为无 checkpoint）。
func (w *Workflow) checkpointSpec(task plugin.Task) *plugin.RequirementSpec {
	raw := task.Artifacts[plugin.ArtifactCkptR1Spec]
	if raw == "" {
		return nil
	}
	var spec plugin.RequirementSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		w.Log.Warn("argus.checkpoint.r1_corrupt", "error", err.Error(), "effect", "全量重跑 R1")
		return nil
	}
	return &spec
}

// checkpointSummary 任务级 checkpoint 复用：R2 摘要（reused=false = 无 checkpoint）。
func (w *Workflow) checkpointSummary(task plugin.Task) (summary string, reused bool) {
	if s := task.Artifacts[plugin.ArtifactCkptR2Summary]; s != "" {
		return s, true
	}
	return "", false
}

// checkpointSave 阶段产物落库（CheckpointSink nil = 不落；失败由 sink 实现方告警，
// 不影响主流程——checkpoint 缺失只影响重试成本）。
func (w *Workflow) checkpointSave(task plugin.Task, patch map[string]string) {
	if w.CheckpointSink == nil {
		return
	}
	w.CheckpointSink(patch)
}

// summaryWithCheckpoint R2 摘要统一入口（Run 与 RunReviewOnly 共用）：checkpoint
// 命中直接复用不调 LLM；未命中调 SummarizeChange，成功即 checkpoint 落库，失败
// 降级空摘要继续管线。v3.49.0 起 code_review/visual_check 任务重试同样免 R2
// 重复跑（此前 checkpoint 只覆盖 requirement 路径）。
func (w *Workflow) summaryWithCheckpoint(ctx context.Context, task plugin.Task) (summary string, reused bool) {
	if s, ok := w.checkpointSummary(task); ok {
		return s, true
	}
	summary, err := w.SummarizeChange(ctx, task)
	if err != nil {
		w.Log.Warn("argus.r2.degrade", "error", err.Error())
		return "", false
	}
	w.checkpointSave(task, map[string]string{plugin.ArtifactCkptR2Summary: summary})
	return summary, false
}

// RunReviewOnly 仅 R4+R5（code_review / visual_check 任务：无需求清单，R5.1/R5.3 需求条件跳过）。
func (w *Workflow) RunReviewOnly(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*Output, error) {
	start := time.Now()
	task.Budget = budget
	rc := w.loadRepoConfig(ctx, task)
	// R2 摘要供 R4 插件复用（失败降级，同 Run）；checkpoint 复用/落库与 Run 同款
	//（v3.49.0 补齐：此前 code_review/visual_check 任务重试时 R2 每次全量重跑）。
	summary, reused := w.summaryWithCheckpoint(ctx, task)
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts[plugin.ArtifactChangeSummary] = summary
	out, err := w.finish(ctx, task, budget, rc, start, nil, nil, summary)
	if out != nil {
		out.Debug = map[string]string{plugin.DebugKeyR2Summary: summary}
		if reused {
			out.Debug[plugin.DebugKeyR2Checkpoint] = "reused"
		}
	}
	return out, err
}

// finish = R4 专家委派 + R5 确定性真值表。
// finish = R4 专家委派 + R5 确定性裁决的编排壳。v3.55.0 拆分为四个命名子步骤，
// 顺序即语义（委派 → 合并/反思/标注 → 仓库主权过滤 → 裁决与门），步骤间只传
// 显式产物——此前 210 行单体函数是勘察在案的最大可读性债。
func (w *Workflow) finish(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget,
	rc *plugin.RepoConfig, start time.Time, spec *plugin.RequirementSpec, report *plugin.ComplianceReport,
	summary string) (*Output, error) {

	changedFiles := diffPaths(task.Diff)
	skills := skillsForKind(task.Kind)

	sel, outcomes, pruned, retryNotes := w.dispatchR4(ctx, task, budget, rc, changedFiles, skills)

	merged, reflectInfo, err := w.mergeAndReflect(ctx, task, outcomes, pruned, summary, changedFiles)
	if err != nil {
		return nil, err
	}
	w.applyRepoPolicies(merged, rc, task)

	verdict, rule, completeness := w.judgeAndGate(task, spec, report, merged, sel, outcomes, pruned, retryNotes)

	out := &Output{
		Verdict:      verdict,
		Rule:         rule,
		Spec:         spec,
		Report:       report,
		Merged:       merged,
		PluginsRun:   plugin.OutcomeNames(outcomes),
		Summary:      summary,
		Elapsed:      time.Since(start),
		Reflect:      reflectInfo,
		Completeness: completeness,
	}
	out.Tokens = w.LLM.UsedTokens()
	return out, nil
}

// dispatchR4 R4 专家委派：确定性路由 → 预算裁剪 → fan-out → 编队共识 → SAST
// 并入 → G4 受限补审。返回裁决所需的全量产物（sel 供完备性裁判、retryNotes
// 供降级注明）。
func (w *Workflow) dispatchR4(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget,
	rc *plugin.RepoConfig, changedFiles, skills []string) (sel []plugin.Selection,
	outcomes []plugin.Outcome, pruned, retryNotes []string) {

	stageStart := time.Now()
	w.onStage(plugin.LayerR4)

	// R4：确定性路由 → 预算裁剪 → fan-out
	sel = w.Dispatcher.Select(&task.PR, changedFiles, skills, rc)
	sel, pruned = w.Dispatcher.PruneByBudget(sel, budget)
	outcomes = w.Dispatcher.Run(ctx, task, sel, budget)
	// v3.4 编队共识模式：consensus 编队不进 fan-out（Select 已排除），在
	// fan-out 后单独召集——成员黑板互看、共识收敛，产出按成员归属进 Merger。
	for _, sq := range w.Dispatcher.ConsensusSquads(&task.PR, changedFiles) {
		outcomes = append(outcomes, w.Dispatcher.RunConsensus(ctx, task, sq, budget)...)
	}
	// SAST 确定性前置（v3.15.0）：产出并入合并（密钥泄漏等确定性结论可直接驱动
	// R5.2 一票否决）；零 LLM、不计任务 token 预算。
	if len(w.SASTOutcomes) > 0 {
		outcomes = append(w.SASTOutcomes, outcomes...)
	}
	// G4 受限补审（opt-in）：执行失败的插件在剩余预算内重试一次（v3.11.0）
	if w.CompletenessRetry {
		outcomes, retryNotes = w.retryFailedPlugins(ctx, task, budget, sel, outcomes)
	}
	plugin.ReportStageDuration(plugin.LayerR4, time.Since(stageStart))
	plugin.ReportLayerExec(plugin.LayerR4)
	return sel, outcomes, pruned, retryNotes
}

// mergeAndReflect 合并与修订：Merger 归一化/指纹去重/抑制过滤 → R2 缺失与预算
// 裁剪注明 → R3.5 报告反思 → 学习环低置信标注重放 → 交叉审查分歧标注。
func (w *Workflow) mergeAndReflect(ctx context.Context, task plugin.Task, outcomes []plugin.Outcome,
	pruned []string, summary string, changedFiles []string) (*plugin.MergedFindings, *plugin.ReflectSummary, error) {

	// Merger：归一化 + 指纹去重 + 抑制过滤（store 由 Merger 持有）
	merged, err := w.Merger.Merge(ctx, task.PR, task.ID, outcomes, time.Now())
	if err != nil {
		return nil, nil, err
	}
	if summary == "" {
		merged.Degraded = append(merged.Degraded,
			"R2 变更理解失败，变更摘要缺失（R3/R4 直接基于原始 diff 审查，报告不含变更动机摘要）")
	}
	for _, p := range pruned {
		merged.Degraded = append(merged.Degraded, p+" 未参与（token 预算裁剪）")
	}

	// R3.5 报告反思（v3.4）：合并去重后、分歧标注与 R5 裁决前——被剔除的意见连带
	// 分歧标注一起消失，R5 真值表也基于修订后 findings 求值。指纹库不受影响：
	// 反思是逐轮质量闸门（统计性判断），持久抑制只来自人工 suppress 反馈。
	var reflectInfo *plugin.ReflectSummary
	if w.ReflectRounds > 0 {
		// 活检：开关开着就必须走到 R35；模型未配/无 findings 也算「尝试执行」
		//——否则「配置了反思却从未调用」会与「正常空跑」不可分。
		plugin.ReportLayerExec(plugin.LayerR35)
		w.onStage(plugin.LayerR35)
		reflectInfo = w.reflectFindings(ctx, task, summary, merged)
		if reflectInfo != nil && (reflectInfo.Removed > 0 || reflectInfo.DegradedNoEvidence > 0) {
			note := fmt.Sprintf(
				"R3.5 报告反思：%d 轮（收敛=%v），剔除 %d 条误报嫌疑、修正 %d 条",
				reflectInfo.Rounds, reflectInfo.Converged, reflectInfo.Removed, reflectInfo.Adjusted)
			if reflectInfo.DroppedNoEvidence > 0 {
				note += fmt.Sprintf("；证据门丢弃 %d 条", reflectInfo.DroppedNoEvidence)
			}
			if reflectInfo.DegradedNoEvidence > 0 {
				note += fmt.Sprintf("；证据门降级 %d 条", reflectInfo.DegradedNoEvidence)
			}
			merged.Degraded = append(merged.Degraded, note)
		}
	}

	// 学习环低置信标注重放：R3.5 按 id 回写 comment 会覆盖 Merger 阶段的后缀
	// （docs/review-closed-loop-design.md §3.5）。幂等。必须先于交叉分歧标注——
	// 交叉标注文本先入 Comment 会让重放 matchLowPattern 的键失配（PatternKey 由
	// 评语派生），被交叉标注的条目永远补不上低置信标注（v3.9.1）。
	if w.Merger != nil {
		w.Merger.AnnotateLowConfidence(ctx, task.PR, merged)
	}

	// 交叉审查分歧标注（v3.3）：编队成员对同一区域意见不一致时标给人审
	if members := w.Dispatcher.ActiveSquadMembers(&task.PR, changedFiles); len(members) >= 2 {
		w.Merger.AnnotateCrossReview(merged, members)
	}
	return merged, reflectInfo, nil
}

// applyRepoPolicies 仓库主权过滤（确定性，裁决仍不走 LLM）：review.min_severity
// 低于阈值者不进报告、不参与 R5 裁决；路径排除的覆盖收缩必须可见。
func (w *Workflow) applyRepoPolicies(merged *plugin.MergedFindings, rc *plugin.RepoConfig, task plugin.Task) {
	// 仓库级严重度阈值（v3.15.0 review.min_severity）
	if rc != nil && rc.Review.MinSeverity != "" {
		kept, dropped := plugin.FilterByMinSeverity(merged.Findings, rc.Review.MinSeverity)
		if dropped > 0 {
			merged.Findings = kept
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("仓库配置 review.min_severity=%s：%d 条低于阈值的意见未纳入报告与裁决",
					rc.Review.MinSeverity, dropped))
		}
	}
	// 仓库级路径排除透出（v3.15.0）：被 exclude/include 排除的文件对模型不可见，
	// 覆盖范围收缩必须可见（与截断/增量注记同哲学）。
	if task.Diff != nil {
		excluded := 0
		for _, f := range task.Diff.Files {
			if f.RepoExcluded {
				excluded++
			}
		}
		if excluded > 0 {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("仓库配置 review 路径过滤：%d 个变更文件被排除在审查之外（.reviewagents.yaml）", excluded))
		}
	}
}

// judgeAndGate R5 确定性裁决与全部门：完备性裁判 → scope 闸门 → 真值表 →
// R5.0 空产出守门 → SAST 失败注明 → R5.C 完备性降级门 → 增量审查范围透出。
// merged 的 Degraded 按原顺序追加。
func (w *Workflow) judgeAndGate(task plugin.Task, spec *plugin.RequirementSpec,
	report *plugin.ComplianceReport, merged *plugin.MergedFindings, sel []plugin.Selection,
	outcomes []plugin.Outcome, pruned []string, retryNotes []string) (verdict, rule string, completeness *plugin.CompletenessReport) {

	// G4 完备性裁判（v3.11.0）：确定性检查「审得全不全」（插件缺失/截断/AC 漏判/
	// 降级审查）。失败项经 Output.Completeness 进报告独立「完备性检查」段
	//（不并入 Merged.Degraded，避免与插件降级说明重复）；demote 策略在 R5 后降级。
	completeness = JudgeCompleteness(sel, outcomes, pruned, task, spec, report)
	merged.Degraded = append(merged.Degraded, retryNotes...)

	// R5：确定性真值表（按序求值，首条命中即短路；模型自评 verdict 不参与）。
	// v3.8.0：有 diff 文件集时启用 scope 闸门——reviewer 经 get_file 看到未变更
	// 文件后对本次变更之外的文件报 high，此前可直接驱动 R5.2 REQUEST_CHANGES
	//（一票否决了与本次变更无关的意见）；现在越界 high 只计入 COMMENT 级并注明。
	r5Start := time.Now()
	var scope map[string]bool
	if task.Diff != nil && len(task.Diff.Files) > 0 {
		scope = make(map[string]bool, len(task.Diff.Files))
		for _, f := range task.Diff.Files {
			// 仓库级排除文件（v3.15.0 RepoExcluded）不进审查视野（diff 文本不含），
			// 与「get_file 读到的未变更文件」同属越界——其上的意见不驱动 R5.2
			//（v9 夹具 47 钉住：排除文件上的 high 此前会一票否决，排除形同虚设）。
			if f.RepoExcluded {
				continue
			}
			scope[f.Path] = true
		}
	}
	verdict, rule, outOfScope := VerdictScoped(spec, report, merged.Findings, scope)
	if outOfScope > 0 {
		merged.Degraded = append(merged.Degraded,
			fmt.Sprintf("%d 条意见位于本次变更范围之外（get_file 读到的未变更文件），未计入高危裁决", outOfScope))
	}
	plugin.ReportStageDuration(plugin.LayerR5, time.Since(r5Start))
	plugin.ReportLayerExec(plugin.LayerR5)

	// R5.0 守门（深挖 P0）：零插件实际产出时禁入 R5.5 APPROVE。skills 词表不匹配/
	// 插件全被禁用/全数熔断/跳过时，空 findings 是"没人审"而不是"没问题"——
	// 静默 APPROVE 会把未审查的变更放进主干。降级为 COMMENT + Degraded 注明。
	// SAST 前置产出不计入 ran（它不是审查 agent，不能把"只有 gitleaks 跑了"
	// 洗成"有人审过"）。
	sastNames := make(map[string]bool, len(w.SASTOutcomes))
	for _, o := range w.SASTOutcomes {
		sastNames[o.Name] = true
	}
	ran := 0
	for _, o := range outcomes {
		if sastNames[o.Name] {
			continue
		}
		if o.SkippedReason == "" && o.Err == nil {
			ran++
		}
	}
	if ran == 0 {
		merged.Degraded = append(merged.Degraded,
			"无任何审查插件实际产出（全被禁用/熔断/跳过，原因见上方 Degraded）——本结论在未审查状态下给出，仅作参考")
		if verdict == VerdictApprove {
			verdict, rule = VerdictComment, "R5.0"
		}
	}
	// SAST 失败降级注明（产出空 ≠ 扫描成功；覆盖事实必须可见）。
	for _, o := range w.SASTOutcomes {
		if o.Err != nil {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("SAST %s 扫描失败：%s（审查继续）", o.Name, logredact.Redact(o.Err.Error())))
		}
	}
	// G4 降级门（opt-in demote）：完备性检查不过时不允许白给 APPROVE（v3.11.0）。
	// 这是 R5.0 精神向「部分覆盖」的延伸：R5.0 管全缺，R5.C 管部分缺失/截断/漏判。
	if !completeness.OK && w.CompletenessPolicy == plugin.CompletenessDemote && verdict == VerdictApprove {
		verdict, rule = VerdictComment, "R5.C"
	}
	plugin.ReportVerdict(task.PR.Platform, task.Kind, verdict, rule)
	// 增量审查范围透出（v3.8.0）：AlreadyReviewed 文件未重审此前零痕迹——
	// 与大 PR 截断同构的"覆盖范围收缩"，结论可信度的一部分必须可见。
	if task.Diff != nil {
		reviewed := 0
		for _, f := range task.Diff.Files {
			if f.AlreadyReviewed {
				reviewed++
			}
		}
		if reviewed > 0 {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("增量审查：%d 个文件与上轮完全一致未重审，本次仅覆盖其余变更", reviewed))
		}
	}
	// 大 PR 截断透出（深挖 P0 → v3.11.0 由完备性裁判 CheckDiffTruncated 承接，
	// 注记统一从 completeness.DegradedNotes() 产出，此处不再重复）。
	return verdict, rule, completeness
}

// retryFailedPlugins G4 受限补审：对执行失败（Err 非空且非任务取消跳过）的插件
// 重试一次。约束：剩余预算 >0 才补审；重试经 Dispatcher.Run 照常过预算门/熔断门
// （已熔断插件会被跳过，保留原错误）；重试成功**替换**原 outcome——不叠加 findings，
// 不产生双重指纹。consensus 产物不重试（黑板轮转语义由 RunConsensus 自管）。
func (w *Workflow) retryFailedPlugins(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget,
	sel []plugin.Selection, outcomes []plugin.Outcome) ([]plugin.Outcome, []string) {

	selByName := make(map[string]plugin.Selection, len(sel))
	for _, s := range sel {
		selByName[s.Reg.Manifest.Name] = s
	}
	var retrySel []plugin.Selection
	for _, o := range outcomes {
		if o.Err != nil && o.SkippedReason == "" {
			if s, ok := selByName[o.Name]; ok {
				retrySel = append(retrySel, s)
			}
		}
	}
	if len(retrySel) == 0 {
		return outcomes, nil
	}
	if budget != nil && budget.Remaining() <= 0 {
		return outcomes, []string{"受限补审跳过：任务 token 预算已耗尽"}
	}
	names := make([]string, 0, len(retrySel))
	for _, s := range retrySel {
		names = append(names, s.Reg.Manifest.Name)
	}
	w.Log.Info("argus.completeness.retry", "plugins", strings.Join(names, ","))
	retried := w.Dispatcher.Run(ctx, task, retrySel, budget)
	retryByName := make(map[string]plugin.Outcome, len(retried))
	for _, o := range retried {
		retryByName[o.Name] = o
	}
	var notes []string
	for i := range outcomes {
		o := &outcomes[i]
		if o.Err == nil || o.SkippedReason != "" {
			continue
		}
		r, ok := retryByName[o.Name]
		if !ok {
			continue
		}
		switch {
		case r.Err == nil && r.SkippedReason == "":
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试成功（原执行失败）", o.Name))
			o.Result, o.Err, o.Duration, o.Manifest, o.SkippedReason = r.Result, nil, r.Duration, r.Manifest, ""
		case r.SkippedReason != "":
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试被跳过（%s）", o.Name, r.SkippedReason))
		default:
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试仍失败", o.Name))
		}
	}
	return outcomes, notes
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
	verdict, rule, _ = VerdictScoped(spec, report, findings, nil)
	return verdict, rule
}

// VerdictScoped Verdict 的 scope 感知变体（v3.8.0）：inScope 非 nil 时，file 不在
// 本次变更文件集内的 high 意见不触发 R5.2（reviewer 经 get_file 能看到未变更文件，
// 对变更外文件的一票否决越权），降计 COMMENT 级；返回值为越界条数（0 = 无或未启用）。
// inScope 为 nil 时与 Verdict 完全等价（真值表原语义不变，既有测试不受影响）。
func VerdictScoped(spec *plugin.RequirementSpec, report *plugin.ComplianceReport, findings []plugin.Finding,
	inScope map[string]bool) (verdict, rule string, outOfScope int) {
	var high, midLow, unverifiable int
	for _, f := range findings {
		switch f.Severity {
		case reportutil.High:
			// scope 闸门：越界 high 不进 R5.2 计数，但保留为 COMMENT 级意见
			if inScope != nil && f.File != "" && !inScope[f.File] {
				outOfScope++
				midLow++
				continue
			}
			high++
		case reportutil.Medium, reportutil.Low:
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
		return VerdictRequestChanges, "R5.1", outOfScope
	}
	if high > 0 {
		return VerdictRequestChanges, "R5.2", outOfScope
	}
	needHuman := unverifiable > 0 || (report != nil && len(report.ScopeCreep) > 0)
	if spec != nil { // requirement 模式：模糊需求 / 空清单强制人工确认
		needHuman = needHuman || len(spec.Ambiguous) > 0 || len(spec.ACs) == 0
	}
	if needHuman {
		return VerdictComment, "R5.3", outOfScope
	}
	if midLow > 0 {
		return VerdictComment, "R5.4", outOfScope
	}
	return VerdictApprove, "R5.5", outOfScope
}

// loadRepoConfig 仓库主权配置（§3.4：只从默认分支读；失败按无配置 + 告警）。
// runner 注入的 RepoConfig（v3.15.0）优先：diff 过滤与 workflow 共用一次加载结果。
func (w *Workflow) loadRepoConfig(ctx context.Context, task plugin.Task) *plugin.RepoConfig {
	if w.RepoConfig != nil {
		return w.RepoConfig
	}
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
