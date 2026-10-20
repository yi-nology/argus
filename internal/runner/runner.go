// Package runner 任务执行编排（§10.4 worker 的执行体）：
// 拉起任务 → 组装上下文（PR/diff）→ 调 workflow（R1→R5 或 R4+R5）→ poster 回帖。
// 错误处置遵循 §10.2 矩阵：上下文/LLM 失败 → task=failed（不在 PR 评论，避免平台故障刷屏用户）。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"golang.org/x/time/rate"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/obs/audit"
	"github.com/oagentkit/textx/textutil"
	"github.com/oekit/logx"

	"github.com/cloudwego/eino/callbacks"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/port"
	"github.com/oagentkit/rag"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/learning"
	"argus/internal/memory"
	"argus/internal/notify"
	"argus/internal/plugin"
	"argus/internal/poster"
	"argus/internal/progress"
	"argus/internal/reportview"
	"argus/internal/sink"
	"argus/internal/storage"
	"argus/internal/toolpool"
	"argus/internal/workflow/requirement"

	"github.com/oagentkit/llm/obsx"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oagentkit/tools/workcopy"
)

// Runner 单任务执行器。
type Runner struct {
	Cfg       *config.Config
	Store     *storage.Store
	SDK       *argusctx.SDKCtx
	Model     model.BaseChatModel // eino 模型（每任务包一层 llm.Client 以隔离预算）
	ModelName string
	// LLM 装配期构造的生成器（*llm.Client 或 *llm.Resilient）。非 nil 时优先于 Model。
	LLM        llm.Generator
	Dispatcher *plugin.Dispatcher
	Merger     *plugin.Merger
	ToolPool   toolpool.Pool
	ToolSpecs  []toolpool.ToolSpec
	Knowledge  rag.KnowledgeService
	// WC 工作副本池（OCR delegate 前置使用；nil = 委托前置不生效）。
	WC      *workcopy.Pool
	Bus     *progress.Bus    // 任务进度事件（v3.1 SSE；nil 安全）
	Notify  *notify.Notifier // 终态 IM 推送（v3.1 octopus；nil 安全）
	Sink    *sink.Reporter   // 审查结果上报（ARGUS_SINK_URL 非空启用；nil 安全，失败只告警）
	Poster  *poster.Poster
	Log     logx.Logger
	Version string
	// Audit 审计日志（v3.86.0：verdict.posted / fix.push 等关键动作留痕；
	// agentkit/audit nil 接收者安全，未注入 = 只少审计不报错）。
	Audit *audit.Logger

	// LLMLimiter 全局 LLM 请求限速器（nil = 不限速）。
	LLMLimiter *rate.Limiter

	// Cost 全局成本追踪器（obsx OnUsage 回流；nil = 不记账）。
	Cost *llm.CostTracker
	// CostPer1K 模型 → 单价换算（主/备模型外的调用按 0 计）。
	CostPer1K func(model string) [2]float64

	// StageLLMs 阶段模型覆盖（P6：ARGUS_MODEL_R1/R2；经 StageRouter 进 workflow，
	// 未覆盖阶段走缺省链）。generator 为装配期原样实例，预算在包装时注入。
	StageLLMs map[string]llm.Generator
	// R3Model R3 ReAct 专用模型覆盖（P6：ARGUS_MODEL_R3；nil = 主模型）。
	// 已由 main 侧 Failover 装饰时直接使用；nil 时 buildWorkflow 用 LLM.RawModel 再包。
	R3Model model.BaseChatModel
	// FallbackModel ReAct 主路径 failover 备模型（agentkit llm.FailoverModel；
	// nil = 不启用）。Resilient.RawModel 只返回主模型，ReAct 旁路靠本字段补齐降级。
	FallbackModel model.BaseChatModel
	FallbackName  string
	// ReflectRounds R3.5 报告反思轮数（v3.4：ARGUS_REFLECTION_ROUNDS；0 = 关闭）。
	ReflectRounds int
	// ReflectModel R3.5 反思专用模型（ARGUS_MODEL_R35；nil = 回退 R3Model → 主模型）。
	ReflectModel model.BaseChatModel
	// ReflectEvidence 证据门模式（ARGUS_REFLECTION_EVIDENCE：off|soft|strict，默认 off）。
	ReflectEvidence string

	// G4 完备性裁判（v3.11.0）：annotate|demote（ARGUS_COMPLETENESS_POLICY）；
	// Retry=受限补审开关（ARGUS_COMPLETENESS_RETRY，默认关）。
	CompletenessPolicy string
	CompletenessRetry  bool
	// Learning 模式级后悔学习环（ARGUS_REGRET_LEARNING，默认关闭）。
	Learning learning.Config

	// R3 方法论注入（requirement-compliance 插件的 skillRefs，装配期传入）
	SkillProvider skill.Provider
	SkillRefs     []string

	skillMu      sync.Mutex
	skillCache   string
	skillLoaded  bool
	skillRefresh time.Time // 下次刷新时间（10 分钟周期，与 RAG rescan 对齐）
}

// execContext 注入任务级回调链并返回新 ctx：观测 tracing + 旁路用量记账
// （v0.10.12 记账单源化——obsx OnUsage 退役，trace 与 usage 两个 handler 一次
// InitCallbacks 注入；UsageHandler 只补 ReAct/RawModel 旁路，Generate 直连已由
// llm.Client.OnUsage 记账，handler 内防重跳过，两侧不双计）。旁路用量回灌任务
// 预算（v3.36.0）并记任务级/进程级成本。PreviewLen 刻意留 0（不落消息内容）。
func (r *Runner) execContext(ctx context.Context, budget *plugin.TokenBudget, taskCost *llm.CostTracker) context.Context {
	obsOpt := obsx.Options{SlowThreshold: 60 * time.Second}
	handlers := []callbacks.Handler{obsx.NewTracingHandler(r.Log, obsOpt)}
	if r.Cost != nil {
		handlers = append(handlers, llm.NewUsageHandler(func(rec llm.UsageRecord) {
			// v3.94.0：agentkit v0.12 起校准内化为 FitModel 实例槽（主路径自动
			// 校准），原 wrap.RecordUsage 回灌调用点删除。旁路无 stage 语义
			//（RunInfo 仅组件类型），stage 记空、模型按组件类型归账。
			budget.Add(rec.PromptTokens + rec.CompletionTokens)
			// v3.150.0：Record → RecordPriced（旧 Record 按 1K 计费已退役，
			// 新接口单位写在类型上=每 1M token；argus 配置侧仍是 per-1K，×1000 换算）。
			rate := r.costRate(rec.Model)
			price := port.Price{InputPerM: rate[0] * 1000, OutputPerM: rate[1] * 1000}
			taskCost.RecordPriced(rec.Model, "", rec.PromptTokens, rec.CompletionTokens, price)
			r.Cost.RecordPriced(rec.Model, "", rec.PromptTokens, rec.CompletionTokens, price)
		}))
	}
	return callbacks.InitCallbacks(ctx, nil, handlers...)
}

// wireWorkflow 组装任务级工作流并接线任务侧注入：评审记忆索引（dream 导出）/
// 外部工具调用上限/仓库级评审规范（.argus/REVIEW.md）/阶段进度事件。
func (r *Runner) wireWorkflow(ctx context.Context, task *storage.Task, budget *plugin.TokenBudget) *requirement.Workflow {
	wf := r.buildWorkflow(budget)
	// 本仓评审记忆索引（v3.35.0）：dream 周期导出事实文件，此处装载注入 R3；
	// 目录缺失/为空 = 空串（R3 提示词无该段），功能未启用零开销。
	wf.MemoryIndex = memory.LoadIndexWithOrg(r.Cfg.MemoryDir, memory.Ref{
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
	}, r.Cfg.MemoryIndexBudget)
	// 外部工具调用上限可配（v3.41.0）
	wf.ExternalToolCallLimit = r.Cfg.ExternalToolCallLimit
	// 仓库级评审规范（v3.38.0）：.argus/REVIEW.md @ head，R3/R4 注入
	wf.RepoGuide = r.RepoGuideFor(ctx, task.Platform, task.Owner, task.Repo, task.HeadSHA)
	// 阶段进度事件（v3.40.0）：R1/R2/R3/R35 进入时发 task.stage，SSE 消费者可展示
	wf.OnStage = func(stage string) { r.publishStage(task, stage) }
	return wf
}

// repoBudgetGate 仓库级月度预算门（v3.1）：超限在调 LLM 前短路——落 failed
// 终态（fenced）、start marker 与 IM 播报，返回 true 表示已拦截。预算路径不重试
// （刻意）→ 直接终态告知（v3.30.1）。
func (r *Runner) repoBudgetGate(ctx context.Context, task *storage.Task) bool {
	if r.Cfg.RepoMonthlyTokenBudget <= 0 {
		return false
	}
	now := time.Now()
	used, err := r.Store.RepoMonthlyTokens(ctx, task.Platform, task.Owner, task.Repo,
		time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
	if err != nil {
		r.Log.Warn("argus.runner.budget_check_failed", "task", task.ID, "error", err.Error())
		return false
	}
	if used < int64(r.Cfg.RepoMonthlyTokenBudget) {
		return false
	}
	r.Log.Warn("argus.runner.repo_budget_exceeded", "task", task.ID,
		"repo", task.Owner+"/"+task.Repo, "used", used, "budget", r.Cfg.RepoMonthlyTokenBudget)
	reason := fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", used, r.Cfg.RepoMonthlyTokenBudget)
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", reason, 0, 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"❌ Argus 审查失败：%s——下月预算重置后 push 新提交可重新触发审查。\n\n<!-- argus:start task_id=%s -->",
		reason, task.ID))
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, reason)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
	return true
}

// handleRunFailure 任务失败收尾：停机硬取消显式保持 running（依赖心跳过期重置
// 回 pending，此前靠"ctx 已死 → 写库必然失败"的副作用成立，语义脆弱已改）；
// 其余落 failed 终态（fenced）+ PR 侧失败告知（未超重试上限=中断将自动重试，
// 超限/禁用=最终失败，v3.30.1：失败必须显式告之，不许 MR 作者只见「审查中」
// 挂死）+ 终态播报。错误链可能携带带凭据 URL，三路消费统一先脱敏（logredact）。
func (r *Runner) handleRunFailure(ctx context.Context, task *storage.Task, budget *plugin.TokenBudget, runErr error) {
	if ctx.Err() != nil {
		r.Log.Warn("argus.runner.task_cancelled", "task", task.ID,
			"action", "保持 running，待心跳过期重置回 pending")
		return
	}
	errMsg := logredact.Redact(runErr.Error())
	r.Log.Error("argus.runner.task_failed", "task", task.ID, "error", errMsg)
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", errMsg, budget.Used(), 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
	} else if fenced {
		// stale 重置后新执行体已接管：本次写入归属失效，属预期而非错误
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	// PR 侧失败告知（best-effort，平台不可达时静默放弃）：failed-retry 扫
	// 5m×3^n 退避后重排。
	if cur, gerr := r.Store.GetTask(ctx, task.ID); gerr == nil {
		var body string
		switch {
		case r.Cfg.TaskMaxRetries > 0 && cur.Attempt < r.Cfg.TaskMaxRetries:
			body = fmt.Sprintf("⚠️ Argus 审查中断（第 %d 次尝试失败），数分钟后将自动重试。\n> %s\n\n<!-- argus:start task_id=%s -->",
				cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
		default:
			body = fmt.Sprintf("❌ Argus 审查失败（已尝试 %d 次）：\n> %s\n\npush 新提交可重新触发审查。\n\n<!-- argus:start task_id=%s -->",
				cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
		}
		r.setStartMarker(ctx, task, body)
	} else {
		r.Log.Warn("argus.runner.fail_notify_skip", "task", task.ID, "error", gerr.Error())
	}
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, errMsg)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	budget := plugin.NewTokenBudget(r.Cfg.EffectiveTaskTokenBudget())
	taskCost := llm.NewCostTracker()
	ctx = r.execContext(ctx, budget, taskCost)
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	// 任务级 MCP 调用元数据：随 ctx 流入该任务全部工具调用的 _meta，server 侧
	// 可关联调用来源（哪次评审在调我）；Q&A 等非任务路径无注入。
	ctx = toolpool.WithCallMeta(ctx, map[string]any{
		"task_id": taskID,
		"repo":    task.Owner + "/" + task.Repo,
		"mr":      task.Number,
	})
	start := time.Now()
	wf := r.wireWorkflow(ctx, task, budget)
	r.publish(progress.EventTaskRunning, task, "", "")

	// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
	r.postStartMarker(ctx, task)

	if r.repoBudgetGate(ctx, task) {
		return nil
	}

	if err = r.run(ctx, task, wf, budget, start, taskCost); err == nil {
		return nil
	}
	r.handleRunFailure(ctx, task, budget, err)
	return nil // 已终结为 failed，不再向 worker 上抛
}

// run 单任务管线执行体。v3.58.0 拆分为四个命名子步骤（顺序即语义）：
// prepareContext（PR 上下文/仓库配置/diff/stale 判定）→ buildPluginTask
// （任务组装 + checkpoint + 各注入）→ recordRegret（学习环记账）→
// finalizeAndAnnounce（终态落库/sink/marker/播报）。此前 202 行单体是
// 勘察在案的可读性债（finish/r3Tools 同款清偿）。
func (r *Runner) run(ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *plugin.TokenBudget, start time.Time, taskCost *llm.CostTracker) error {

	// CI 失败归因（v3.71.0）：不依赖 MR 语义（push 的 commit 无 CR 可拉），
	// 在 prepareContext 之前分派——输入是失败 job 日志而非 diff。
	if task.Kind == plugin.TaskKindCIAnalysis {
		return r.runCIAnalysis(ctx, task, start)
	}

	// 本地 diff 预审（v3.123.0）：同 ci_analysis 不依赖 MR 语义，prepareContext
	// 之前分派——输入是提交方送来的 diff 文本（artifacts），无平台拉取。
	if task.Kind == plugin.TaskKindDiffPreview {
		return r.runDiffPreview(ctx, task, start)
	}

	pr, rc, diff, stale, err := r.prepareContext(ctx, task)
	if err != nil {
		return err
	}
	if stale {
		return nil // stale 终态已落地
	}

	// 写作面分支（v3.71.0）：describe/changelog 不进 R 系列审查管线与插件派发，
	// 单步 LLM 写作 → 平台写回，独立收口。
	if task.Kind == plugin.TaskKindDescribe || task.Kind == plugin.TaskKindChangelog {
		return r.runWriting(ctx, task, pr, diff, start)
	}

	ptask := r.buildPluginTask(ctx, task, pr, rc, diff, wf)

	var out *requirement.Output
	switch task.Kind {
	case plugin.TaskKindRequirementCompliance:
		out, err = wf.Run(ctx, ptask, budget)
	default: // code_review / visual_check：R4+R5 only
		out, err = wf.RunReviewOnly(ctx, ptask, budget)
	}
	if err != nil {
		return err
	}

	r.recordRegret(ctx, out, pr)

	// 组装报告并回帖（staleness 门控在 poster 内）
	rep := r.buildReport(task, pr, out, taskCost)
	rep.VerdictText = reportview.VerdictText(out.Rule, countMissingPartial(out.Report))
	rep.Rule = out.Rule
	// 仓库级行内评论上限（v3.15.0 review.max_comments；0 = 不限，钳制在 poster）
	rep.MaxInlineComments = rc.Review.MaxComments

	// 修复验证环（v3.74.0）：配置了隔离包装命令且本次审查有工作副本可用时，
	// 对带 suggested_fix 的意见做沙箱应用+编译验证，通过者渲染「✅ 沙箱验证
	// 通过」。任何失败都静默降级为不标注——验证环绝不阻断审查主链路。
	r.verifyFixes(ctx, task, pr, out)

	res, err := r.Poster.Post(ctx, task, *pr, diff, out.Verdict, rep)
	// 活检：回帖层是否被真正调用（成功/失败/stale 都算执行过）
	plugin.ReportLayerExec(plugin.LayerPoster)
	if err != nil {
		// 回帖最终失败 → task=failed + {plugin=poster} 计数（§10.2）
		plugin.ReportPluginFailure("poster")
		return fmt.Errorf("回帖失败: %w", err)
	}

	return r.finalizeAndAnnounce(ctx, task, pr, out, res, rep, diff, start)
}

// prepareContext PR 上下文与前置判定：平台 API 组装 → 仓库主权配置单次加载 →
// 增量 diff → stale 判定。stale=true 表示 diff 与上次完全一致、stale 终态已
// 落地，调用方直接返回；err 非 nil 为真实失败。
func (r *Runner) prepareContext(ctx context.Context, task *storage.Task) (
	pr *plugin.PRContext, rc *plugin.RepoConfig, diff *plugin.DiffContext, stale bool, err error) {

	pr, err = r.buildPRContext(ctx, task)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// 仓库主权配置单次加载（v3.15.0）：diff 路径过滤、附加指令、workflow 严重度
	// 阈值与评论上限共用；加载失败降级为空配置（全默认行为）。
	rc = r.loadRepoConfig(ctx, pr)
	diffOpts := argusctx.PrepareOptions{
		Exclude: rc.Review.ExcludePaths,
		Include: rc.Review.IncludePaths,
	}
	diff, err = r.buildDiffIncremental(ctx, pr, task, diffOpts)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// 增量审查：diff 与上次完全一致 → 标记 stale，跳过 LLM
	if diff.NoNewChanges {
		r.Log.Info("argus.runner.no_new_changes", "task", task.ID,
			"pr", fmt.Sprintf("%s/%s/%s#%s", task.Platform, task.Owner, task.Repo, task.Number))
		staleReason := "增量审查：diff 与上次审查完全一致，无新变更"
		if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusStale, "", "", "", staleReason, 0, 0); ferr != nil {
			r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
		} else if fenced {
			r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
				"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
		}
		r.announceTerminal(ctx, task, storage.StatusStale, "", nil, "")
		plugin.ReportTaskTotal(pr.Platform, task.Kind, storage.StatusStale)
		return pr, rc, diff, true, nil
	}
	if task.HeadSHA == "" && pr.HeadSHA != "" {
		_ = r.Store.SetTaskHeadSHA(ctx, task.ID, pr.HeadSHA)
	}
	return pr, rc, diff, false, nil
}

// buildPluginTask 任务组装与注入：plugin.Task 骨架 → checkpoint 回流/落库钩子 →
// 仓库指引/附加指令/学习环提示/跨轮一致性 → OCR/SAST/codegraph 前置 →
// 仓库级行为配置进 workflow。
func (r *Runner) buildPluginTask(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	rc *plugin.RepoConfig, diff *plugin.DiffContext, wf *requirement.Workflow) plugin.Task {

	ptask := plugin.Task{
		ID:        task.ID,
		Kind:      task.Kind,
		PR:        *pr,
		Diff:      diff,
		Artifacts: storage.DecodeJSONMap(task.ArtifactsJSON),
	}
	if ptask.Artifacts == nil {
		ptask.Artifacts = map[string]string{}
	}
	// G2 断点续审（v3.12.0）：同任务重试时回流已持久化的 R1/R2 阶段产物，
	// 跳过已成功阶段的重复 LLM 调用（跨任务不生效——新任务无 checkpoint 键）。
	if task.StageArtifacts != "" {
		if m := storage.DecodeJSONMap(task.StageArtifacts); len(m) > 0 {
			for k, v := range m {
				ptask.Artifacts[k] = v
			}
			r.Log.Info("argus.runner.checkpoint_reuse", "task", task.ID, "stages", len(m))
		}
	}
	// 阶段产物落库钩子：R1/R2 每完成一步即持久化（失败只告警——checkpoint 缺失
	// 只影响重试成本，不影响正确性）
	wf.CheckpointSink = func(arts map[string]string) {
		if err := r.Store.MergeTaskStageArtifacts(ctx, task.ID, arts); err != nil {
			r.Log.Warn("argus.runner.checkpoint_save_failed", "task", task.ID, "error", err.Error())
		}
	}
	// 仓库级自定义审查指引（.argus/review-guidelines.md，对标 Copilot 的
	// custom review instructions）：只从默认分支读，缺失/超限静默降级。
	if g := r.fetchReviewGuidelines(ctx, pr); g != "" {
		ptask.Artifacts[plugin.ArtifactReviewGuidelines] = g
	}
	// 仓库附加审查指令（v3.15.0 .reviewagents.yaml review.extra_instructions）：
	// 与 guidelines.md 合并注入（无文件指引时单独成段），同一信任边界。
	if extra := rc.Review.ExtraInstructions; strings.TrimSpace(extra) != "" {
		merged := ptask.Artifacts[plugin.ArtifactReviewGuidelines]
		if merged != "" {
			merged += "\n"
		}
		ptask.Artifacts[plugin.ArtifactReviewGuidelines] = merged +
			"\n===【仓库附加审查指令（.reviewagents.yaml review.extra_instructions）】===\n" + extra
	}
	// 学习环：注入本仓库历史高误报模式提示词（无样本/关闭时无此键）。
	if h := r.reviewHints(ctx, pr); h != "" {
		ptask.Artifacts[plugin.ArtifactReviewHints] = h
	}
	// 跨轮一致性（v3.52.0）：同 PR 此前轮次的 open 意见注入 R4，供评审员保持
	// 结论一致（推翻须显式说明理由）——同 head 重审 verdict 翻转（生产实测
	// request_changes↔comment 漂移）的最小缓解。查询失败降级无此键。
	if prev := r.prevFindingsSection(ctx, pr); prev != "" {
		ptask.Artifacts[plugin.ArtifactPrevFindings] = prev
		r.Log.Info("argus.runner.prev_findings_injected", "task", task.ID,
			"pr", fmt.Sprintf("%s/%s/%s#%s", pr.Platform, pr.Owner, pr.Repo, pr.Number),
			"bytes", len(prev))
	}
	// OCR delegate 委托前置（design §2）：确定性文件分诊 + 仓库规则注入 R4。
	r.injectOCRDelegate(ctx, pr, diff, &ptask)
	// SAST 确定性前置（v3.15.0）：gitleaks/semgrep 工作副本扫描，产出直进 Merger。
	r.injectSAST(ctx, pr, diff, wf)
	// Go 符号索引（v3.15.0 P0-1）：get_symbol/get_references 工具 + 触达符号注入。
	r.injectCodeGraph(ctx, pr, diff, &ptask, wf)
	// 全文检索（v3.84.0）：search_code 受控 grep 工具的工作副本路径注入。
	r.injectCodeSearch(ctx, pr, diff, wf)
	// 跨仓契约快照（v3.90.0）：related 段声明的关联仓契约文件注入 R4。
	r.injectRelatedContracts(ctx, pr, rc, &ptask)

	// 仓库级行为配置进 workflow（v3.15.0）：min_severity 过滤在 finish 内执行；
	// RepoConfig 非 nil 时 workflow 不再重复加载（省一次 API 往返）。
	wf.RepoConfig = rc
	return ptask
}

// recordRegret 学习环记账：R3.5 反思剔除的条目分别落指纹级误报历史与模式级
// regret（失败只告警，不影响主流程）。
func (r *Runner) recordRegret(ctx context.Context, out *requirement.Output, pr *plugin.PRContext) {
	// regret 跨轮入库：R3.5 反思剔除的指纹记录误报历史（产出 vs 终审的差值即
	// regret），后续轮次由 Merger 读取标注可信度。失败只告警，不影响主流程。
	// RemovedPrints[i] 与 RemovedFindings[i] 按索引一一对应（reflection 组装保证）：
	// 行不存在时按原始身份内容建行，不再产生空内容幽灵行（v3.8.0）。
	if out.Reflect != nil && len(out.Reflect.RemovedPrints) > 0 {
		for i, fp := range out.Reflect.RemovedPrints {
			var finding plugin.Finding
			if i < len(out.Reflect.RemovedFindings) {
				finding = out.Reflect.RemovedFindings[i]
			}
			if err := r.Store.RecordPrintRegret(ctx,
				pr.Platform, pr.Owner, pr.Repo, pr.Number, fp, finding); err != nil {
				r.Log.Warn("argus.regret.record_failed", "error", err.Error())
			}
		}
	}
	// 模式级 regret：完整剔除条目按 plugin+comment 入 pattern_signals。
	// 记账键必须与 PatternPosted 一致（PatternKey 由 plugin+原始评语派生），而
	// RemovedFindings 携带的 Comment 可能已带展示标注（merger 标注先于 R3.5）——
	// 按索引配对的 RemovedPrints[i]（入库 FP）查 prints 行取回原文后再记账，
	// 查不到（行被清理等）才退回携带文本（v3.9.0 审查修复：惯犯条目模式归因丢失）。
	// By 取首段与 merger PatternPosted 同口径（v3.9.1：多插件合并条目的 regret
	// 此前落在 "a,b" 键的影子行，真实模式行只缺不涨）。
	if out.Reflect != nil && len(out.Reflect.RemovedFindings) > 0 && r.Learning.Enabled {
		for i, f := range out.Reflect.RemovedFindings {
			if i < len(out.Reflect.RemovedPrints) {
				if ps, err := r.Store.GetPrintState(ctx,
					pr.Platform, pr.Owner, pr.Repo, pr.Number, out.Reflect.RemovedPrints[i]); err != nil {
					r.Log.Warn("argus.regret.print_state_failed", "error", logredact.Redact(err.Error()))
				} else if ps != nil && ps.Comment != "" {
					f.Comment = ps.Comment
				}
			}
			if err := r.Store.PatternRegret(ctx,
				pr.Platform, pr.Owner, pr.Repo, learning.FirstBy(f.By), f, time.Now()); err != nil {
				r.Log.Warn("argus.regret.pattern_failed", "error", err.Error())
			}
		}
	}
}

// finalizeAndAnnounce 终态收口：结果落库（fenced）→ debug 留存 → sink 旁路
// 上报 → start marker 收口 → 终态播报与指标。finish 落库失败返回 error 由
// 主失败路径接手（task=failed），fenced 则静默返回（写入已被新执行体接管）。
func (r *Runner) finalizeAndAnnounce(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	out *requirement.Output, res *poster.Result, rep reportview.Report, diff *plugin.DiffContext, start time.Time) error {

	resultJSON, _ := json.Marshal(toResultPayload(out, diff.Text))
	status := storage.StatusDone
	if res.Stale {
		status = storage.StatusStale // findings/指纹已入库，不回帖不覆盖（§10.2）
	}
	if fenced, err := r.Store.FinishTaskFenced(ctx, task.ID, status, out.Verdict, reportview.Render(rep),
		string(resultJSON), "", out.Tokens, res.SummaryCommentID); err != nil {
		return err
	} else if fenced {
		// 此前无守卫：stale 重置后僵尸执行体会把旧报告覆盖到新执行体的结果上
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
		return nil
	}
	// 调试产物留存（ARGUS_DEBUG=1）：R1/R2/R3 原始输出随任务可查（GET /tasks/:id 的 debug 字段）
	if r.Cfg.Debug && len(out.Debug) > 0 {
		if b, e := json.Marshal(out.Debug); e == nil {
			_ = r.Store.SetTaskDebug(ctx, task.ID, string(b))
		}
	}
	// 结果上报（ARGUS_SINK_URL 非空启用）：push 旁路给 review-service 承接展示，
	// 异步且失败只告警——绝不阻断/拖慢审查终态路径。argus 独立运行（未配置）时
	// 整段跳过、零开销，不依赖 review-service 的任何存在。
	if r.Sink.Enabled() {
		merged := []plugin.Finding{}
		if out.Merged != nil {
			merged = out.Merged.Findings
		}
		r.Sink.Report(sink.BuildPayload(task.ID, pr, merged, out.Verdict,
			out.PluginsRun, time.Since(start).Milliseconds(), out.Tokens, time.Now()))
	}
	// start marker 收口（v3.30.1）：成功路径把「审查中」更新为完成态（此前
	// marker 发布后永不更新，PR 上一直挂着「审查中」）。stale=不回帖由新执行体
	// 接管，marker 保持审查中由其收口。
	if status == storage.StatusDone {
		r.setStartMarker(ctx, task, fmt.Sprintf(
			"✅ Argus 审查完成，结论：**%s**（%s）——报告见下方总结评论。\n\n<!-- argus:start task_id=%s -->",
			strings.ToUpper(out.Verdict), out.Rule, task.ID))
	}
	r.announceTerminal(ctx, task, status, out.Verdict, out, "")
	plugin.ReportTaskTotal(pr.Platform, task.Kind, status)
	// 按仓趋势埋点（v3.73.0）：findings 密度 + 耗时分布，metrics 面按仓聚合。
	if out.Merged != nil {
		bySev := map[string]int{}
		for _, f := range out.Merged.Findings {
			bySev[f.Severity]++
		}
		for sev, n := range bySev {
			plugin.ReportFindingsByRepo(pr.Platform, pr.Owner+"/"+pr.Repo, sev, n)
		}
	}
	plugin.ReportTaskDurationByRepo(pr.Platform, pr.Owner+"/"+pr.Repo, task.Kind, time.Since(start).Seconds())
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", status,
		"verdict", out.Verdict, "rule", out.Rule, "findings", findingsCount(out.Merged),
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}
