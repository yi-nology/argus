// Package runner 任务执行编排（§10.4 worker 的执行体）：
// 拉起任务 → 组装上下文（PR/diff）→ 调 workflow（R1→R5 或 R4+R5）→ poster 回帖。
// 错误处置遵循 §10.2 矩阵：上下文/LLM 失败 → task=failed（不在 PR 评论，避免平台故障刷屏用户）。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"

	"github.com/oagentkit/obs/audit"
	"github.com/oekit/concurrency/ratelimit"
	"github.com/oekit/logx"

	"github.com/cloudwego/eino/callbacks"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/port"
	"github.com/oagentkit/rag"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/dispatch"
	"argus/internal/domain"
	"argus/internal/impactview"
	"argus/internal/learning"
	"argus/internal/memorykernel"
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
	Dispatcher *dispatch.Dispatcher
	Merger     *dispatch.Merger
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
	// oagentkit/obs/audit nil 接收者安全，未注入 = 只少审计不报错）。
	Audit *audit.Logger

	// LLMLimiter 全局 LLM 请求限速器（nil = 不限速）。
	LLMLimiter ratelimit.Limiter

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
	// FallbackModel ReAct 主路径 failover 备模型（oagentkit llm.FailoverModel；
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

	// MemKernel 评审记忆内核（v3.151.0 引入；v5.0.0 起记忆唯一路径）：
	// 非 nil 时 R3 记忆注入走内核八段召回 + MemoryBand 防伪包裹。
	MemKernel *memorykernel.Kernel

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
			// v3.180.0 省 token 盲区修复：CachedTokens 是命中前缀缓存的 prompt token
			// （便宜得多），旧记账按全价入账 → 成本高估。摊薄 InputPerM 折扣缓存命中
			// 部分（cacheAdjustedInput），token 计数保持真实。llm v0.17.0 起用
			// RecordPricedCached 让 cached 落进 CostRecord.CachedTokens（CacheHitRatio
			// 可派生）——缓存折扣本就该调用方在 Price 层表达（上游 Price 文档明示），
			// 故 cacheAdjustedInput 保留而非上游内置。
			inputPerM := cacheAdjustedInput(rate[0]*1000, rec.PromptTokens, rec.CachedTokens, r.Cfg.CacheReadRatio())
			price := port.Price{InputPerM: inputPerM, OutputPerM: rate[1] * 1000}
			taskCost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			r.Cost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			// 缓存命中率观测（前缀缓存命中率=cached/prompt）：argus 侧 Prometheus 时序
			// 监控面（Grafana），与上游 CostTracker 的 pull 式 Summary 互补不冗余。
			plugin.ReportLLMCache(rec.Model, rec.CachedTokens, rec.PromptTokens)
		}))
	}
	return callbacks.InitCallbacks(ctx, nil, handlers...)
}

// wireWorkflow 组装任务级工作流并接线任务侧注入：评审记忆索引（dream 导出）/
// 外部工具调用上限/仓库级评审规范（.argus/REVIEW.md）/阶段进度事件。
func (r *Runner) wireWorkflow(ctx context.Context, task *storage.Task, budget *plugin.TokenBudget) *requirement.Workflow {
	wf := r.buildWorkflow(budget)
	// 本仓评审记忆索引（v3.35.0）：dream 周期导出，此处装载注入 R3。
	// v5.0.0 文件面退役：内核是唯一路径；未装配（ARGUS_MEMORY_KERNEL 关）=
	// 空串（R3 提示词无该段），功能未启用零开销。
	// 内核召回走八段 + MemoryBand 防伪（sessionID=任务 ID 防回声）。
	if r.MemKernel != nil {
		ref := memorykernel.Ref{Platform: task.Platform, Owner: task.Owner, Repo: task.Repo}
		wf.MemoryIndex = r.MemKernel.RecallIndex(ctx, memorykernel.NamespaceOf(ref),
			memorykernel.OrgNamespace, task.ID, "", r.Cfg.MemoryIndexBudget)
	}
	// 外部工具调用上限可配（v3.41.0）
	wf.ExternalToolCallLimit = r.Cfg.ExternalToolCallLimit
	// 仓库级评审规范（v3.38.0）：.argus/REVIEW.md @ head，R3/R4 注入
	wf.RepoGuide = r.RepoGuideFor(ctx, task.Platform, task.Owner, task.Repo, task.HeadSHA)
	// 阶段进度事件（v3.40.0）：R1/R2/R3/R35 进入时发 task.stage，SSE 消费者可展示
	wf.OnStage = func(stage string) { r.publishStage(task, stage) }
	// 详情存档目录（v5.0.0 spill 收编）：超限工具全文落盘任务目录，get_detail 跨
	// checkpoint 复活/重启取回不失效；顺带 best-effort 清理 7 天前过期任务目录。
	wf.SpillDir = filepath.Join(r.Cfg.WorkRoot, "spill", task.ID)
	return wf
}

// PruneSpillRoot spill 存档过期清理（best-effort，v5.1.0 自每任务 wireWorkflow
// 挪 bootstrap 周期 ticker——每任务全目录扫描在任务风暴下是 O(N²) 面；周期化后
// 扫描频率与任务量解耦）。并发双删无害（后者报错忽略）。
func PruneSpillRoot(root string) {
	dir := filepath.Join(root, "spill")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	// 按仓旋钮单次加载（v3.187.0）：repo_settings 覆盖 → 全局默认，预算闸/
	// 任务预算/diff 上限三处消费全程共用（runner_knobs.go）。
	knobs := r.loadTaskKnobs(ctx, task)
	budget := dispatch.NewTokenBudget(knobs.taskTokenBudget)
	taskCost := llm.NewCostTracker()
	ctx = r.execContext(ctx, budget, taskCost)
	// 任务级 MCP 调用元数据：随 ctx 流入该任务全部工具调用的 _meta，server 侧
	// 可关联调用来源（哪次评审在调我）；Q&A 等非任务路径无注入。
	ctx = toolpool.WithCallMeta(ctx, map[string]any{
		"task_id": taskID,
		"repo":    task.Owner + "/" + task.Repo,
		"mr":      task.Number,
	})
	// v3.185.4：任务体统一 panic 出口——上游 queue/worker 的 panic 隔离只记
	// 日志不留终态（心跳过期复位重跑），对确定性 panic 是「复位→再炸」死循环
	// （live 实测：影响面注入 nil map panic 后任务卡 running）；argus 的
	// failed-retry 退避是重试正道，panic 必须经 handleRunFailure 落 failed 终态。
	if err = guardPanic(r.Log, task.ID, func() error {
		start := time.Now()
		wf := r.wireWorkflow(ctx, task, budget)
		r.publish(progress.EventTaskRunning, task, "", "")

		// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
		r.postStartMarker(ctx, task)

		if r.repoBudgetGate(ctx, task, knobs.monthlyBudget) {
			return nil
		}

		if rerr := r.run(ctx, task, wf, budget, start, taskCost, knobs); rerr != nil {
			r.handleRunFailure(ctx, task, budget, rerr)
		}
		return nil
	}); err != nil {
		// 守卫唯一上抛 = 任务体 panic：此时 run 已中断，handleRunFailure 未及
		// 执行——在守卫外补一次失败终态（其自身若再炸仅日志，终态缺失由心跳
		// 复位兜底，但确定性 panic 已从「无限复位重炸」变为 failed-retry 退避）
		defer func() {
			if rec := recover(); rec != nil {
				r.Log.Error("argus.runner.finalize_panic", "task", task.ID,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			}
		}()
		r.handleRunFailure(ctx, task, budget, err)
		return err
	}
	return nil
}

// guardPanic 任务体 panic → error 的统一安全出口（v3.185.4）。上游
// queue/worker 的 runTask 对 panic 只记一行日志（无栈、无终态），依赖心跳
// 过期复位——确定性 panic 下等于无限「复位→再炸」。本守卫把 panic 转成
// error 交回调用方走 handleRunFailure 的 failed-retry 退避正道，并补全栈
// 日志（此前排查影响面注入 panic 被迫临时 defer 才拿到栈）。
func guardPanic(log logx.Logger, taskID string, body func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("任务执行 panic: %v", rec)
			log.Error("argus.runner.task_panic", "task", taskID,
				"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
		}
	}()
	return body()
}

// run 单任务管线执行体。v3.58.0 拆分为四个命名子步骤（顺序即语义）：
// prepareContext（PR 上下文/仓库配置/diff/stale 判定）→ buildPluginTask
// （任务组装 + checkpoint + 各注入）→ recordRegret（学习环记账）→
// finalizeAndAnnounce（终态落库/sink/marker/播报）。此前 202 行单体是
// 勘察在案的可读性债（finish/r3Tools 同款清偿）。
func (r *Runner) run(ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *plugin.TokenBudget, start time.Time, taskCost *llm.CostTracker, knobs taskKnobs) error {

	// CI 失败归因（v3.71.0）：不依赖 MR 语义（push 的 commit 无 CR 可拉），
	// 在 prepareContext 之前分派——输入是失败 job 日志而非 diff。
	if task.Kind == domain.TaskKindCIAnalysis {
		return r.runCIAnalysis(ctx, task, start)
	}

	// 本地 diff 预审（v3.123.0）：同 ci_analysis 不依赖 MR 语义，prepareContext
	// 之前分派——输入是提交方送来的 diff 文本（artifacts），无平台拉取。
	if task.Kind == domain.TaskKindDiffPreview {
		return r.runDiffPreview(ctx, task, start)
	}

	pr, rc, diff, stale, err := r.prepareContext(ctx, task, knobs)
	if err != nil {
		return err
	}
	if stale {
		return nil // stale 终态已落地
	}

	// 写作面分支（v3.71.0）：describe/changelog 不进 R 系列审查管线与插件派发，
	// 单步 LLM 写作 → 平台写回，独立收口。
	if task.Kind == domain.TaskKindDescribe || task.Kind == domain.TaskKindChangelog {
		return r.runWriting(ctx, task, pr, diff, start)
	}

	ptask := r.buildPluginTask(ctx, task, pr, rc, diff, wf)

	var out *requirement.Output
	switch task.Kind {
	case domain.TaskKindRequirementCompliance:
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
	// 影响面段（v3.183.0）：injectImpactView 产出的快照渲染为评论折叠段
	//（快照已在 ptask.Artifacts，零额外计算；坏 JSON 静默省段——不阻断回帖）。
	if raw := ptask.Artifacts[plugin.ArtifactImpactView]; raw != "" {
		var snap impactview.Snapshot
		if err := json.Unmarshal([]byte(raw), &snap); err == nil {
			rep.ImpactSection = impactview.RenderReportSection(&snap)
		}
	}
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
