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
	"github.com/cloudwego/eino/components/tool"
	"golang.org/x/time/rate"

	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/textutil"
	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/yi-nology/go-git-platform/provider"

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
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
	"github.com/cloudwego/eino/callbacks"

	"git.example.com/enjoydream/agentkit/obsx"
	"git.example.com/enjoydream/agentkit/skill"
	"git.example.com/enjoydream/agentkit/workcopy"
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

// skillText 惰性解析 skillRef（§9.1：含 checksum，防内容漂移难排查），10 分钟周期刷新。
// 改 skill 文件后无需重启，下一个任务自动加载新内容。
// 锁只护缓存字段；SkillProvider I/O 在锁外执行，避免多任务 R3 装配互相阻塞。
func (r *Runner) skillText(_ context.Context) string {
	if r.SkillProvider == nil || len(r.SkillRefs) == 0 {
		return ""
	}
	r.skillMu.Lock()
	if r.skillLoaded && time.Now().Before(r.skillRefresh) {
		cached := r.skillCache
		r.skillMu.Unlock()
		return cached
	}
	r.skillMu.Unlock()

	// 用 Background ctx：skill 是本地文件读取，不需要任务级取消。
	bgCtx := context.Background()
	text := r.loadSkillText(bgCtx)

	r.skillMu.Lock()
	r.skillCache = text
	r.skillLoaded = true
	r.skillRefresh = time.Now().Add(10 * time.Minute)
	r.skillMu.Unlock()
	return text
}

// loadSkillText 实际解析 skill 内容（无锁；结果由 skillText 写回缓存）。
func (r *Runner) loadSkillText(bgCtx context.Context) string {
	var parts []string
	toolMode := r.Cfg.SkillMode == "tool"
	if toolMode {
		// 渐进披露：清单用 agentkit skill.ListPrompt 渲染——加粗主词是规范引用名
		//（use_skill 加载依据），frontmatter 展示名降为括注；模型用展示名回填时
		// AsSkillTool 会别名归一化（v0.7.0 起闭环）
		lister, ok := r.SkillProvider.(skill.Lister)
		if !ok {
			r.Log.Warn("argus.runner.skill_list_failed", "error", "provider 不支持 ListSkills")
			return ""
		}
		metas, err := lister.ListSkills(bgCtx)
		if err != nil {
			r.Log.Warn("argus.runner.skill_list_failed", "error", err.Error())
			return ""
		}
		allowed := map[string]bool{}
		for _, ref := range r.SkillRefs {
			if i := strings.Index(ref, "@"); i >= 0 {
				ref = ref[:i]
			}
			allowed[ref] = true
		}
		var filtered []skill.Meta
		for _, m := range metas {
			if len(allowed) == 0 || allowed[m.Name] {
				filtered = append(filtered, m)
			}
		}
		// 两级预算降级（v0.10.18，对标 ZCode skills 注入预算）：清单超 20K 字节
		// 先降纯名单、仍超再截断注明；预算内渲染与 ListPrompt 完全一致。
		return skill.RenderList(filtered, skill.DefaultListBudget)
	}
	for _, ref := range r.SkillRefs {
		name, ver := ref, ""
		if i := strings.Index(ref, "@"); i >= 0 {
			name, ver = ref[:i], ref[i+1:]
		}
		s, err := r.SkillProvider.Resolve(bgCtx, skill.Ref{Name: name, Version: ver, Source: "file"})
		if err != nil {
			r.Log.Warn("argus.runner.skill_resolve_failed", "ref", ref, "error", err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("--- skill:%s (checksum=%s) ---\n%s", s.Name, s.Checksum, s.Content))
	}
	return strings.Join(parts, "\n")
}

// WorkflowFor 导出每任务 Workflow 构造（ReviewTools 钩子复用 R3 的工具基础设施；
// 构造成本可忽略——字段全部共享引用）。
func (r *Runner) WorkflowFor(budget *plugin.TokenBudget) *requirement.Workflow {
	return r.buildWorkflow(budget)
}

// WorkflowFactory 每任务构造 Workflow（供 requirement-compliance 插件与 runner 共用）。
// budget 决定该任务 LLM 客户端的预算累计器；nil = 不限（协议重入路径）。
// 契约类型用 plugin.WorkflowFactory（v3.5.6 上提）——编排层不再 import 具体适配器。
func (r *Runner) WorkflowFactory() plugin.WorkflowFactory {
	return func(budget *plugin.TokenBudget) plugin.WorkflowRunner {
		return r.buildWorkflow(budget)
	}
}

// buildWorkflow 每任务构造 Workflow 实例：LLM 客户端持有任务预算累计器，
// 其余字段全部共享引用，构造成本可忽略。
func (r *Runner) buildWorkflow(budget *plugin.TokenBudget) *requirement.Workflow {
	// 决策使用模式：use_skill 工具进 R3 工具表（与静态注入互斥，ARGUS_SKILL_MODE）
	var skillTool tool.BaseTool
	if r.Cfg.SkillMode == "tool" && r.SkillProvider != nil && len(r.SkillRefs) > 0 {
		// 显式内容上限（v3.39.0）：与清单 RenderList 预算构成两级纪律
		if st, e := skill.AsSkillTool(r.SkillProvider, r.SkillRefs,
			skill.WithContentCap(r.Cfg.SkillContentCap)); e == nil {
			skillTool = st
		} else {
			r.Log.Warn("argus.runner.skill_tool_failed", "error", e.Error(), "fallback", "静态注入")
		}
	}
	// 优先使用装配期构造的 Generator（*llm.Client 或 *llm.Resilient 降级链）；
	// 通过 BudgetInjector 注入任务级预算（Resilient 副本共享熔断状态，Client 浅拷贝）。
	var gen llm.Generator
	if r.LLM != nil {
		gen = r.LLM
		if budget != nil {
			if bi, ok := gen.(llm.BudgetInjector); ok {
				gen = bi.WithBudget(budget)
			}
		}
		// P6 阶段路由：R1/R2 覆盖链各自注入任务预算后注册（精确/前缀匹配，
		// "R1" 命中 "R1a"）；未覆盖阶段走缺省链
		if len(r.StageLLMs) > 0 {
			sr := llm.NewStageRouter(gen)
			for stage, sg := range r.StageLLMs {
				if bi, ok := sg.(llm.BudgetInjector); ok && budget != nil {
					sg = bi.WithBudget(budget)
				}
				sr.Use(stage, sg)
			}
			gen = sr
		}
	} else {
		c := llm.NewClient(r.Model, r.ModelName, budget)
		// 窗口与输出预留注入 → Generate 发送前输入自守恒（fitInput）
		c.ContextTokens = r.Cfg.LLM.ContextToken
		c.MaxOutputTokens = r.Cfg.MaxTokensResolved()
		c.Limiter = r.LLMLimiter
		// agentkit llm 用 OnUsage 回调替代直接调用 plugin.ReportLLMTokens
		c.OnUsage = func(stage string, prompt, completion int) {
			plugin.ReportLLMTokens(stage, r.ModelName, prompt+completion)
		}
		gen = c
	}
	// R3 ReAct 模型解析：阶段覆盖 > RawModel；配置了备模型则包 FailoverModel
	//（ADK 直调 BaseChatModel，不走 Resilient 链）。
	r3Chat := r.R3Model
	if r3Chat == nil && r.LLM != nil {
		r3Chat = r.LLM.RawModel()
	}
	if r3Chat != nil && r.FallbackModel != nil {
		// v0.10.11：链节结构化（ChainLink{Model,Name}），消平行切片错位
		fm := llm.NewFailoverModel(
			llm.ChainLink{Model: r3Chat, Name: r.ModelName},
			llm.ChainLink{Model: r.FallbackModel, Name: r.FallbackName},
		)
		if r.Log != nil {
			fm.OnFailover = func(from, to, reason string) {
				r.Log.Warn("argus.llm.failover", "from", from, "to", to,
					"stage", "R3(react)", "reason", reason)
			}
		}
		r3Chat = fm
	}
	return &requirement.Workflow{
		LLM:                gen,
		R3ChatModel:        r3Chat,
		SkillTool:          skillTool,
		SDK:                r.SDK,
		Dispatcher:         r.Dispatcher,
		Merger:             r.Merger,
		Tools:              r.ToolPool,
		ToolSpecs:          r.ToolSpecs,
		Knowledge:          r.Knowledge,
		SkillText:          r.skillText,
		Log:                r.Log,
		ReqBodyMaxChars:    r.Cfg.ReqBodyMaxChars(),
		ReqIssueMaxChars:   r.Cfg.ReqIssueMaxChars(),
		GetFileMaxChars:    r.Cfg.GetFileMaxRunes(),
		GetIssueMaxChars:   r.Cfg.GetIssueMaxRunes(),
		GetDiffMaxChars:    r.Cfg.GetDiffMaxRunes(),
		ReflectRounds:      r.ReflectRounds,
		ReflectModel:       r.ReflectModel,
		ReflectEvidence:    r.ReflectEvidence,
		CompletenessPolicy: r.CompletenessPolicy,
		CompletenessRetry:  r.CompletenessRetry,
	}
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	// 任务级成本记账（P0）：回收全部 eino 模型调用的真实 usage。v0.10.12 记账
	// 单源化——obsx OnUsage 退役，trace 与 usage 两个 handler 一次 InitCallbacks
	// 并注入；UsageHandler 只补 ReAct/RawModel 旁路（Generate 直连已由
	// llm.Client.OnUsage 记账，handler 内防重跳过，两侧不双计）。旁路无 stage
	// 语义（RunInfo 仅组件类型），stage 记空、模型按组件类型归账。
	// PreviewLen 刻意留 0（不落消息内容），勿"调观测"时误设。
	budget := plugin.NewTokenBudget(r.Cfg.EffectiveTaskTokenBudget())
	taskCost := llm.NewCostTracker()
	obsOpt := obsx.Options{SlowThreshold: 60 * time.Second}
	handlers := []callbacks.Handler{obsx.NewTracingHandler(r.Log, obsOpt)}
	if r.Cost != nil {
		handlers = append(handlers, llm.NewUsageHandler(func(rec llm.UsageRecord) {
			argusctx.RecordUsage(rec.Model, rec.PromptTokens) // ctxfit 真实 usage 校准回灌
			// 旁路用量回灌任务预算（v3.36.0）：ReAct/RawModel 直调不经 Client
			// 预算注入，硬截断此前对其失效。callbacks 侧只收旁路调用（Client
			// 记账调用被防重跳过），恰好无双计。
			budget.Add(rec.PromptTokens + rec.CompletionTokens)
			taskCost.Record(rec.Model, "", rec.PromptTokens, rec.CompletionTokens, r.costRate(rec.Model))
			r.Cost.Record(rec.Model, "", rec.PromptTokens, rec.CompletionTokens, r.costRate(rec.Model))
		}))
	}
	ctx = callbacks.InitCallbacks(ctx, nil, handlers...)
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
	r.publish("task.running", task, "", "")

	// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
	r.postStartMarker(ctx, task)

	// 仓库级月度预算门控（v3.1）：超限在调 LLM 前短路，任务直接 failed。
	if r.Cfg.RepoMonthlyTokenBudget > 0 {
		now := time.Now()
		used, err := r.Store.RepoMonthlyTokens(ctx, task.Platform, task.Owner, task.Repo,
			time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
		if err != nil {
			r.Log.Warn("argus.runner.budget_check_failed", "task", taskID, "error", err.Error())
		} else if used >= int64(r.Cfg.RepoMonthlyTokenBudget) {
			r.Log.Warn("argus.runner.repo_budget_exceeded", "task", taskID,
				"repo", task.Owner+"/"+task.Repo, "used", used, "budget", r.Cfg.RepoMonthlyTokenBudget)
			reason := fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", used, r.Cfg.RepoMonthlyTokenBudget)
			if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", reason, 0, 0); ferr != nil {
				r.Log.Warn("argus.runner.finish_failed", "task", taskID, "error", ferr.Error())
			} else if fenced {
				r.Log.Warn("argus.runner.finish_fenced", "task", taskID,
					"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
			}
			// 预算路径不重试（刻意）→ 直接终态告知（v3.30.1）
			r.setStartMarker(ctx, task, fmt.Sprintf(
				"❌ Argus 审查失败：%s——下月预算重置后 push 新提交可重新触发审查。\n\n<!-- argus:start task_id=%s -->",
				reason, task.ID))
			r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, reason)
			plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
			return nil
		}
	}

	err = r.run(ctx, task, wf, budget, start, taskCost)
	if err == nil {
		return nil
	}
	// 停机硬取消：显式保持 running（不写终态），依赖"心跳过期重置"重跑。
	// 此前语义靠"ctx 已死 → FinishTask 写库必然失败"的副作用成立，组件意图矛盾且脆弱。
	if ctx.Err() != nil {
		r.Log.Warn("argus.runner.task_cancelled", "task", taskID,
			"action", "保持 running，待心跳过期重置回 pending")
		return nil
	}
	// §10.2：上下文/LLM 阶段失败 → task=failed + 指标 + 日志 + PR 侧告知（v3.30.1：
	// 此前「不在 PR 评论」导致 MR 作者只见「审查中」挂死——失败必须显式告之）。
	// 错误链可能携带平台 SDK/工作副本侧的带凭据 URL（https://user:token@host/...），
	// 日志、入库、IM 推送三路消费统一先脱敏（agentkit logredact）。
	errMsg := logredact.Redact(err.Error())
	r.Log.Error("argus.runner.task_failed", "task", taskID, "error", errMsg)
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", errMsg, budget.Used(), 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", taskID, "error", ferr.Error())
	} else if fenced {
		// stale 重置后新执行体已接管：本次写入归属失效，属预期而非错误
		r.Log.Warn("argus.runner.finish_fenced", "task", taskID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	// PR 侧失败告知（best-effort，平台不可达时静默放弃）：未超重试上限=「中断，
	// 将自动重试」（failed-retry 扫 5m×3^n 退避后重排）；超限/禁用=「最终失败，
	// push 新提交可重新触发」。
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
		r.Log.Warn("argus.runner.fail_notify_skip", "task", taskID, "error", gerr.Error())
	}
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, errMsg)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
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
		Artifacts: decodeArtifacts(task.ArtifactsJSON),
	}
	if ptask.Artifacts == nil {
		ptask.Artifacts = map[string]string{}
	}
	// G2 断点续审（v3.12.0）：同任务重试时回流已持久化的 R1/R2 阶段产物，
	// 跳过已成功阶段的重复 LLM 调用（跨任务不生效——新任务无 checkpoint 键）。
	if task.StageArtifacts != "" {
		if m := decodeArtifacts(task.StageArtifacts); len(m) > 0 {
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

// buildPRContext 从平台 API 组装 PR 上下文（CLI 任务此刻才拿到 head SHA）。
func (r *Runner) buildPRContext(ctx context.Context, task *storage.Task) (*plugin.PRContext, error) {
	cr, err := r.SDK.GetCR(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 PR 详情失败: %w", err)
	}
	repo, err := r.SDK.GetRepo(ctx, task.Platform, task.Owner, task.Repo)
	if err != nil {
		return nil, fmt.Errorf("获取仓库信息失败: %w", err)
	}
	headSHA := cr.HeadSHA
	if headSHA == "" {
		headSHA = task.HeadSHA
	}
	return &plugin.PRContext{
		Platform:      task.Platform,
		Owner:         task.Owner,
		Repo:          task.Repo,
		Number:        task.Number,
		Title:         cr.Title,
		Body:          cr.Description,
		Author:        authorName(cr.Author),
		Labels:        cr.Labels,
		HeadSHA:       headSHA,
		BaseBranch:    cr.TargetBranch,
		HeadBranch:    cr.SourceBranch,
		DefaultBranch: repo.DefaultBranch,
		IsDraft:       cr.Draft,
		EventType:     task.EventType,
	}, nil
}

// buildDiffIncremental 拉取 diff 并尝试增量清洗（diff-of-diffs）。
// 仅在 synchronize 事件（PR 更新）且存在前置审查记录时激活增量路径；
// 其余场景退化为全量 Prepare。opts 为仓库级路径过滤（v3.15.0 review 段）。
func (r *Runner) buildDiffIncremental(ctx context.Context, pr *plugin.PRContext, task *storage.Task,
	opts ...argusctx.PrepareOptions) (*plugin.DiffContext, error) {
	md, err := r.SDK.GetCRDiff(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 diff 失败: %w", err)
	}
	// 任一路径只要 Prepare 成功即视为 diffprep 执行（活检）
	defer plugin.ReportLayerExec(plugin.LayerDiffprep)
	totalLimit := r.Cfg.MaxDiffCharsResolved()

	// 非 synchronize 事件 → 全量审查（首次审查 / reopen 等）
	if task.EventType != "cr.synchronize" {
		return argusctx.Prepare(md, totalLimit, opts...), nil
	}

	// 查询同 PR 最近一次 done 任务的 diff 文本
	lastDiff, err := r.Store.GetLastReviewedDiff(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		r.Log.Warn("argus.runner.get_last_diff_failed", "task", task.ID, "error", err.Error())
		return argusctx.Prepare(md, totalLimit, opts...), nil // 降级全量
	}

	if lastDiff == "" {
		return argusctx.Prepare(md, totalLimit, opts...), nil // 无前置记录 → 全量
	}

	r.Log.Info("argus.runner.incremental_diff", "task", task.ID,
		"last_diff_chars", len(lastDiff))
	return argusctx.PrepareIncremental(md, lastDiff, totalLimit, opts...), nil
}

// loadRepoConfig 仓库主权配置（runner 侧单次加载，diff 过滤与 workflow 共用；
// v3.15.0 前该加载只在 workflow.loadRepoConfig，为传 diffprep 选项在此提前）。
// 失败按无配置 + 告警（与 workflow 侧同纪律）。
func (r *Runner) loadRepoConfig(ctx context.Context, pr *plugin.PRContext) *plugin.RepoConfig {
	if pr.DefaultBranch == "" {
		repo, err := r.SDK.GetRepo(ctx, pr.Platform, pr.Owner, pr.Repo)
		if err != nil {
			r.Log.Warn("argus.runner.repo_fetch_failed", "error", err.Error())
			return plugin.EmptyRepoConfig()
		}
		pr.DefaultBranch = repo.DefaultBranch
	}
	rc, err := r.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		r.Log.Warn("argus.runner.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := plugin.ResolveRepoConfig(rc, r.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			r.Log.Warn("argus.runner.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

// repoGuideMaxBytes 仓库评审规范单文件上限（ZCode AGENTS.md 同款 100KB）。
const repoGuideMaxBytes = 100_000

// repoGuideAgentMaxBytes 业界标准 agent 规范文件的单文件上限（AGENTS.md 等多为
// 项目级通用指引而非审查专属，注入预算给小一些，防多源叠加挤占 R3/R4 窗口）。
const repoGuideAgentMaxBytes = 32_000

// repoGuideAgentFiles 业界标准 agent 规范文件（2026 事实标准，兼容读第一个存在的）：
// AGENTS.md（跨工具通用）/ CLAUDE.md（Claude Code）/ .cursorrules（Cursor）。
// 全部命中时仍只取第一个——同一仓库多份 agent 规范的内容通常高度重叠，
// 全拼只会稀释注入预算。
var repoGuideAgentFiles = []string{"AGENTS.md", "CLAUDE.md", ".cursorrules"}

// repoGuideCache 评审规范缓存（repo@sha → 内容；同 sha 多任务/重试复用，
// 负缓存空串防同 sha 反复打平台 API）。
var (
	repoGuideMu    sync.Mutex
	repoGuideCache = map[string]string{}
)

// RepoGuideFor 读取被评审仓库的评审规范 @ head（v3.38.0，v3.71.0 多源）：
// .argus/REVIEW.md（argus 专属）+ 业界标准 agent 规范（AGENTS.md/CLAUDE.md/
// .cursorrules，第一个存在的）。两源独立截断后拼接，REVIEW.md 在前。
// 经平台 API 取 blob——tree 作用域天然无本地 symlink 逃逸面；头截断 + 提醒
// 标签转义（半可信输入）；缺失/失败返回空段（常态，debug 留痕）。
func (r *Runner) RepoGuideFor(ctx context.Context, platform, owner, repo, headSHA string) string {
	if r.SDK == nil || r.Cfg.RepoGuideOff || headSHA == "" {
		return ""
	}
	key := platform + "/" + owner + "/" + repo + "@" + headSHA
	repoGuideMu.Lock()
	cached, ok := repoGuideCache[key]
	repoGuideMu.Unlock()
	if ok {
		return cached
	}
	var sections []string
	content := r.repoGuideFile(ctx, platform, owner, repo, headSHA, ".argus/REVIEW.md", repoGuideMaxBytes)
	if content != "" {
		sections = append(sections, content)
	} else {
		r.Log.Debug("argus.runner.repo_guide_absent", "repo", owner+"/"+repo)
	}
	for _, path := range repoGuideAgentFiles {
		agent := r.repoGuideFile(ctx, platform, owner, repo, headSHA, path, repoGuideAgentMaxBytes)
		if agent != "" {
			r.Log.Info("argus.runner.agent_guide_loaded", "repo", owner+"/"+repo, "file", path)
			sections = append(sections, "## Agent 通用规范（来自 "+path+"）\n\n"+agent)
			break
		}
	}
	merged := strings.Join(sections, "\n\n")
	repoGuideMu.Lock()
	if len(repoGuideCache) >= 64 {
		repoGuideCache = map[string]string{} // 粗暴整体换防膨胀（键含 sha，天然有限）
	}
	repoGuideCache[key] = merged
	repoGuideMu.Unlock()
	return merged
}

// repoGuideFile 单文件拉取 + 截断 + 提醒标签转义；缺失/失败返回空串。
func (r *Runner) repoGuideFile(ctx context.Context, platform, owner, repo, headSHA, path string, maxBytes int) string {
	raw, err := r.SDK.GetFileContent(ctx, platform, owner, repo, path, headSHA)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	if len(raw) > maxBytes {
		// rune 安全截断：字节切分会腰斩多字节 UTF-8 出非法序列进 R3/R4 prompt。
		var trunc string
		trunc, _ = textutil.TruncRunes(raw, maxBytes/3) // rune≈3 字节，与旧字节预算同量级
		raw = trunc + "\n…（截断：" + path + " 超上限）"
	}
	r.Log.Info("argus.runner.repo_guide_loaded", "repo", owner+"/"+repo, "file", path, "bytes", len(raw))
	return plugin.EscapeReminderTags(raw)
}

// costRate 模型单价（v3.36.0 起统一按配置费率近似，未知模型不再按 0 计）。
func (r *Runner) costRate(mdl string) [2]float64 {
	if r.CostPer1K != nil {
		return r.CostPer1K(mdl)
	}
	return [2]float64{}
}

// buildReport 报告输入组装（模板渲染交给 reportview）。taskCost 非 nil 时附成本摘要。
func (r *Runner) buildReport(task *storage.Task, pr *plugin.PRContext, out *requirement.Output,
	taskCost *llm.CostTracker) reportview.Report {
	rep := reportview.Report{
		TaskID:  task.ID,
		HeadSHA: pr.HeadSHA,
		Version: r.Version,
		Meta:    r.Cfg.ReportMeta,
		Plugins: out.PluginsRun,
		Elapsed: out.Elapsed,
		Tokens:  out.Tokens,
	}
	if taskCost != nil {
		var usd float64
		for _, ms := range taskCost.Summary() {
			usd += ms.TotalCostUSD
		}
		rep.CostUSD = usd
	}
	if out.Merged != nil {
		rep.Findings = out.Merged.Findings
		rep.Degraded = out.Merged.Degraded
		rep.Suppressed = out.Merged.Suppressed
		rep.CrossReview = out.Merged.CrossReview
	}
	// G4 完备性段（DegradedNotes nil 安全：未启用/全通过 → nil）
	rep.Completeness = out.Completeness.DegradedNotes()
	if out.Spec != nil {
		rep.ShowCompliance = true
		rep.ReqSource = out.Spec.Source
		// 预建 check map，O(1) 查找替代 O(N*M) 嵌套循环
		checkMap := map[string]*plugin.ACCheck{}
		if out.Report != nil {
			for i := range out.Report.Checks {
				checkMap[out.Report.Checks[i].ID] = &out.Report.Checks[i]
			}
		}
		for _, ac := range out.Spec.ACs {
			view := reportview.ACCheckView{ID: ac.ID, Text: ac.Text, Status: plugin.ACUnverifiable, Evid: "未判定"}
			if c, ok := checkMap[ac.ID]; ok {
				view.Status = c.Status
				view.Evid = firstNonEmpty(c.Evidence, c.Detail)
			}
			rep.ACs = append(rep.ACs, view)
		}
		if out.Report != nil {
			for _, sc := range out.Report.ScopeCreep {
				rep.ScopeCreep = append(rep.ScopeCreep, reportview.ScopeView{File: sc.File, Note: sc.Note})
			}
		}
	}
	return rep
}

// ResultPayload GetTask 返回的结构化结果（result_json 列）。
type ResultPayload struct {
	Verdict    string                   `json:"verdict"`
	Rule       string                   `json:"rule"`
	Findings   []plugin.Finding         `json:"findings"`
	Report     *plugin.ComplianceReport `json:"compliance_report,omitempty"`
	ScopeCreep []plugin.ScopeCreepItem  `json:"scope_creep,omitempty"`
	Summary    string                   `json:"change_summary,omitempty"`
	Plugins    []string                 `json:"plugins"`
	Degraded   []string                 `json:"degraded,omitempty"`
	// Completeness 完备性裁判结构化产出（v3.13.0 G7 数据出口；nil = 未启用/全通过）。
	// GET /tasks/:id 与 MCP get_report 的消费方据此渲染「审得全不全」。
	Completeness *plugin.CompletenessReport `json:"completeness,omitempty"`
	// DiffText 本次审查的 diff 原文（增量审查时用于 diff-of-diffs 比对）。
	DiffText string `json:"diff_text,omitempty"`
}

func toResultPayload(out *requirement.Output, diffText string) *ResultPayload {
	p := &ResultPayload{Verdict: out.Verdict, Rule: out.Rule, Plugins: out.PluginsRun, Summary: out.Summary, DiffText: diffText}
	if out.Merged != nil {
		p.Findings = out.Merged.Findings
		p.Degraded = out.Merged.Degraded
	}
	p.Report = out.Report
	if out.Report != nil {
		p.ScopeCreep = out.Report.ScopeCreep
	}
	// G7：完备性报告持久化（nil 安全）——结构化产出进 result_json，任何任务
	// 详情消费方（报告页/副驾/IDE 插件）无需解析 markdown 即可拿到
	p.Completeness = out.Completeness
	return p
}

func decodeArtifacts(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	m := map[string]string{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func authorName(u *provider.CRUser) string {
	if u == nil {
		return ""
	}
	if u.Username != "" {
		return u.Username
	}
	return u.Name
}

func countMissingPartial(report *plugin.ComplianceReport) int {
	if report == nil {
		return 0
	}
	n := 0
	for _, c := range report.Checks {
		if c.Status == plugin.ACMissing || c.Status == plugin.ACPartial {
			n++
		}
	}
	return n
}

// publish 广播进度事件（nil Bus 安全）。
func (r *Runner) publish(typ string, task *storage.Task, status, verdict string) {
	if r.Bus == nil || task == nil {
		return
	}
	r.Bus.Publish(progress.Event{
		Type: typ, TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		Status: status, Verdict: verdict,
	})
}

// publishStage 阶段级进度事件（task.stage；v3.40.0 SSE 阶段可视化数据源）。
func (r *Runner) publishStage(task *storage.Task, stage string) {
	if r.Bus == nil || task == nil {
		return
	}
	r.Bus.Publish(progress.Event{
		Type: "task.stage", TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		Stage: stage,
	})
}

// postStartMarker 审查开始时在 MR 上留一条"审查中"评论，便于用户感知进度。
// 失败不阻塞审查流程（仅告警）。
// 幂等复用（v3.8.0）：先找最近的 start-marker 评论，存在则原地更新其 task_id
// 而不是新建——此前每次重跑（发布打断→重置→重跑）都留一条孤儿"审查中"。
func (r *Runner) postStartMarker(ctx context.Context, task *storage.Task) {
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"🔍 Argus 正在审查中，请稍候…\n\n<!-- argus:start task_id=%s -->",
		task.ID,
	))
}

// setStartMarker 复用/新建 start marker（生命周期：审查中→完成/重试中/最终失败，
// 三态更新——此前 marker 发布后永不更新，失败后 MR 作者只见「审查中」挂死）。
// 复用最近一条 start marker（含历史任务的）：ListIssueComments 平台侧通常
// 按时间正序，取末尾几条扫描；找不到锚点才新建。
func (r *Runner) setStartMarker(ctx context.Context, task *storage.Task, body string) {
	if r.SDK == nil {
		return
	}
	if comments, err := r.SDK.ListIssueComments(ctx, task.Platform, task.Owner, task.Repo, task.Number); err == nil {
		for i := len(comments) - 1; i >= 0 && i >= len(comments)-50; i-- {
			c := comments[i]
			if c == nil || !strings.Contains(c.Body, "<!-- argus:start task_id=") {
				continue
			}
			if _, uerr := r.SDK.UpdateIssueComment(ctx, task.Platform, task.Owner, task.Repo, task.Number, c.ID, body); uerr != nil {
				r.Log.Warn("argus.runner.start_marker_update_failed", "task", task.ID, "error", uerr.Error())
			}
			return
		}
	}
	if _, err := r.SDK.CreateIssueComment(ctx, task.Platform, task.Owner, task.Repo, task.Number, body); err != nil {
		r.Log.Warn("argus.runner.start_marker_failed", "task", task.ID, "error", err.Error())
	}
}

// finish 通知：SSE 终态事件 + octopus IM 推送（各自 nil/未配置安全）。
func (r *Runner) announceTerminal(ctx context.Context, task *storage.Task, status, verdict string, out *requirement.Output, failReason string) {
	r.publish("task.finished", task, status, verdict)
	if r.Notify == nil {
		return
	}
	ev := notify.TaskEvent{
		TaskID: task.ID, Platform: task.Platform, Owner: task.Owner,
		Repo: task.Repo, Number: task.Number, Status: status, Verdict: verdict,
		FailReason: failReason,
	}
	if out != nil {
		ev.Rule = out.Rule
		ev.Tokens = out.Tokens
		ev.Elapsed = out.Elapsed
		if out.Merged != nil {
			ev.Findings = len(out.Merged.Findings)
		}
	}
	r.Notify.NotifyTerminal(ctx, ev)
}

func findingsCount(m *plugin.MergedFindings) int {
	if m == nil {
		return 0
	}
	return len(m.Findings)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// reviewHints 渲染本仓库历史高误报模式提示词（学习环关闭或无样本时返回空）。
// 失败只降级为空，不阻塞审查。
func (r *Runner) reviewHints(ctx context.Context, pr *plugin.PRContext) string {
	if pr == nil || !r.Learning.Enabled || r.Store == nil {
		return ""
	}
	list, err := r.Store.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
		r.Learning.MinSamples, r.Learning.InjectFloor, r.Learning.InjectTopN)
	if err != nil || len(list) == 0 {
		if err != nil {
			r.Log.Warn("argus.regret.hints_failed", "error", err.Error())
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("本仓库历史上以下审查模式误报率偏高（开发者 suppress / 终审剔除）。")
	b.WriteString("命中类似表述时：给出具体行与影响，否则不要输出。\n")
	for _, p := range list {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		fmt.Fprintf(&b, "- [%s] %s（置信度 %.2f，samples=%d）\n", p.Plugin, sample, p.Confidence, p.Samples)
	}
	return b.String()
}

// prevFindingsSection 渲染同 PR 此前轮次的 open 意见清单（跨轮一致性参照，
// v3.52.0）：file:line [SEVERITY] 评语（单条截 160 rune），上限 15 条防提示词
// 膨胀。查询失败返回空（best-effort，不影响主流程）。
func (r *Runner) prevFindingsSection(ctx context.Context, pr *plugin.PRContext) string {
	if r.Store == nil {
		return ""
	}
	prints, err := r.Store.ListFindingPrints(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		r.Log.Warn("argus.runner.prev_findings_failed", "error", err.Error())
		return ""
	}
	lines := make([]string, 0, len(prints))
	for _, p := range prints {
		if p.Status != storage.PrintOpen || p.Comment == "" || p.File == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s:%d [%s] %s",
			p.File, p.Line, strings.ToUpper(p.Severity), textutil.TruncEllipsis(p.Comment, 160)))
		if len(lines) >= 15 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
