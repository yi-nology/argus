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

	"github.com/oekit/logx"

	argusctx "argus/internal/context"
	"argus/internal/dispatch"
	"argus/internal/domain"
	"argus/internal/plugin"
	"argus/internal/toolpool"

	"github.com/oagentkit/textx/codeintel"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/rag"
)

// TaskKind 词表（与 IDL TaskKind 对齐）：canonical 定义自 v3.5.6 上提
// domain.TaskKind*（service/poller 不必为拿常量依赖编排层），本包直接引用。

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
	//     CheckpointSink/SASTOutcomes/CodeGraph/CodeSearchDir/RepoConfig：运行期数据
	//     而非装配常量，
	//     runner 每任务 Run 前注入（这正是不用 Options 构造参数表达的原因，v3.54.0 复核定案）；
	//   ③ 调用参数——Run/RunReviewOnly(ctx, task, budget) 携带任务本体与预算。
	// 新增输入先问归属：装配常量→①，任务数据→②，禁止经钩子旁路绕开②。
	LLM llm.Generator
	// R3ChatModel R3 ReAct 专用模型（P6 阶段覆盖；nil = LLM.RawModel()）。
	R3ChatModel model.BaseChatModel
	SDK         *argusctx.SDKCtx
	Dispatcher  *dispatch.Dispatcher
	Merger      *dispatch.Merger
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

	// 详情存档目录（v5.0.0 收编 agent-harness/spill 语义，取代 v3.39.0 进程内 map）：
	// 超限工具全文落盘 <WorkRoot>/spill/<taskID>/，get_detail 取回——checkpoint 复活/
	// 进程重启后 ref 不再失效（旧 map 版重启即失）。runner wireWorkflow 每任务注入；
	// 空 = 两级化退纯截断（无取回指引）。
	SpillDir string

	// spillSeq 存档序号分配（spillMu 护并发工具调用的 ref 唯一性）。
	spillMu  sync.Mutex
	spillSeq int

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

	// R3.5 报告反思（v3.4，oagentkit/agent/reflection）：>0 启用，值为"批判→修订"轮数上限
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
	SASTOutcomes []dispatch.Outcome
	// CodeGraph Go 符号索引（v3.15.0 P0-1：runner 工作副本上建好后注入；
	// 非 nil 时 r3Tools/R4 ReviewTools 挂 get_symbol/get_references 工具）。
	CodeGraph *codeintel.Index
	// CodeSearchDir 工作副本根路径（v3.84.0：runner 就绪后注入；非空时 r3Tools/R4
	// ReviewTools 挂 search_code 全文检索工具，覆盖符号索引到不了的词法线索）。
	// 池语义：注入只记路径不持引用，目录由 workcopy TTL 兜底存活；工具侧读不到
	// 时按工具错误文本降级，不中断 ReAct。
	CodeSearchDir string

	// ckptMu R4 逐插件 checkpoint 落库序列化（v3.154.0）：
	// MergeTaskStageArtifacts 是读-改-写合并，fan-out 并发落键不加锁会丢更新。
	// 任务级实例锁，与 R1/R2 的顺序落库互不竞争。
	ckptMu sync.Mutex
}

// skillsForKind 任务类型 → R4 分派的技能词表。
func skillsForKind(kind string) []string {
	switch kind {
	case domain.TaskKindVisualCheck:
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
			w.checkpointSave(map[string]string{plugin.ArtifactCkptR1Spec: string(b)})
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
func (w *Workflow) checkpointSave(patch map[string]string) {
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
	w.checkpointSave(map[string]string{plugin.ArtifactCkptR2Summary: summary})
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
		PluginsRun:   dispatch.OutcomeNames(outcomes),
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
	rc *plugin.RepoConfig, changedFiles, skills []string) (sel []dispatch.Selection,
	outcomes []dispatch.Outcome, pruned, retryNotes []string) {

	stageStart := time.Now()
	w.onStage(plugin.LayerR4)

	// R4：确定性路由 → 同任务重试回流（v3.154.0）→ 预算裁剪 → fan-out
	sel = w.Dispatcher.Select(&task.PR, changedFiles, skills, rc)
	// 逐插件 checkpoint 回流：命中 ckpt_r4/<plugin> 的插件直接复用产物——
	// 进程崩溃/发布打断后的同任务重试不再整批重跑（R4 是 LLM 主体成本）。
	// 复用产物并入 outcomes（视为已执行），执行集只含未命中插件。
	execSel, restoredSel, restored := restoreR4Outcomes(task.Artifacts, sel)
	if len(restored) > 0 {
		w.Log.Info("argus.r4.ckpt_reuse", "restored", len(restored), "total", len(sel))
	}
	execSel, pruned = w.Dispatcher.PruneByBudget(execSel, budget)
	outcomes = append(outcomes, restored...)
	outcomes = append(outcomes, w.Dispatcher.RunWithHook(ctx, task, execSel, budget, w.r4CkptHook)...)
	// sel 返回口径 = 覆盖全集（复用 + 本轮执行保留），G4 完备性裁判据此判覆盖
	sel = append(restoredSel, execSel...)
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
func (w *Workflow) mergeAndReflect(ctx context.Context, task plugin.Task, outcomes []dispatch.Outcome,
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
		// 预算是被调的，不是天条：把旋钮指给读者（v5.5.3）
		merged.Degraded = append(merged.Degraded,
			p+" 未参与（token 预算裁剪；可调：repo_settings.task_token_budget / ARGUS_TASK_TOKEN_BUDGET）")
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

// retryFailedPlugins G4 受限补审：对执行失败（Err 非空且非任务取消跳过）的插件
// 重试一次。约束：剩余预算 >0 才补审；重试经 Dispatcher.Run 照常过预算门/熔断门
// （已熔断插件会被跳过，保留原错误）；重试成功**替换**原 outcome——不叠加 findings，
// 不产生双重指纹。consensus 产物不重试（黑板轮转语义由 RunConsensus 自管）。
func (w *Workflow) retryFailedPlugins(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget,
	sel []dispatch.Selection, outcomes []dispatch.Outcome) ([]dispatch.Outcome, []string) {

	selByName := make(map[string]dispatch.Selection, len(sel))
	for _, s := range sel {
		selByName[s.Reg.Manifest.Name] = s
	}
	var retrySel []dispatch.Selection
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
	retryByName := make(map[string]dispatch.Outcome, len(retried))
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
