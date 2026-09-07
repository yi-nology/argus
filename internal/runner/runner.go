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

	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/yi-nology/git-platform-sdk/provider"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/notify"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poster"
	"argus/internal/progress"
	"argus/internal/reportview"
	"argus/internal/storage"
	"argus/internal/toolpool"
	"argus/internal/workflow/requirement"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/obsx"
	"git.example.com/enjoydream/agentkit/skill"
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
	Bus        *progress.Bus    // 任务进度事件（v3.1 SSE；nil 安全）
	Notify     *notify.Notifier // 终态 IM 推送（v3.1 octopus；nil 安全）
	Poster     *poster.Poster
	Log        logx.Logger
	Version    string

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
	R3Model model.BaseChatModel
	// ReflectRounds R3.5 报告反思轮数（v3.4：ARGUS_REFLECTION_ROUNDS；0 = 关闭）。
	ReflectRounds int
	// ReflectModel R3.5 反思专用模型（ARGUS_MODEL_R35；nil = 回退 R3Model → 主模型）。
	ReflectModel model.BaseChatModel

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
func (r *Runner) skillText(_ context.Context) string {
	if r.SkillProvider == nil || len(r.SkillRefs) == 0 {
		return ""
	}
	r.skillMu.Lock()
	defer r.skillMu.Unlock()
	if r.skillLoaded && time.Now().Before(r.skillRefresh) {
		return r.skillCache
	}
	// 用 Background ctx：skill 是本地文件读取，不需要任务级取消。
	bgCtx := context.Background()
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
		r.skillCache = skill.ListPrompt(filtered)
		return r.skillCache
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
	r.skillCache = strings.Join(parts, "\n")
	r.skillLoaded = true
	r.skillRefresh = time.Now().Add(10 * time.Minute)
	return r.skillCache
}

// WorkflowFor 导出每任务 Workflow 构造（ReviewTools 钩子复用 R3 的工具基础设施；
// 构造成本可忽略——字段全部共享引用）。
func (r *Runner) WorkflowFor(budget *plugin.TokenBudget) *requirement.Workflow {
	return r.buildWorkflow(budget)
}

// WorkflowFactory 每任务构造 Workflow（供 requirement-compliance 插件与 runner 共用）。
// budget 决定该任务 LLM 客户端的预算累计器；nil = 不限（协议重入路径）。
func (r *Runner) WorkflowFactory() adapter_builtin.WorkflowFactory {
	return func(budget *plugin.TokenBudget) adapter_builtin.WorkflowRunner {
		return r.buildWorkflow(budget)
	}
}

// buildWorkflow 每任务构造 Workflow 实例：LLM 客户端持有任务预算累计器，
// 其余字段全部共享引用，构造成本可忽略。
func (r *Runner) buildWorkflow(budget *plugin.TokenBudget) *requirement.Workflow {
	// 决策使用模式：use_skill 工具进 R3 工具表（与静态注入互斥，ARGUS_SKILL_MODE）
	var skillTool tool.BaseTool
	if r.Cfg.SkillMode == "tool" && r.SkillProvider != nil && len(r.SkillRefs) > 0 {
		if st, e := skill.AsSkillTool(r.SkillProvider, r.SkillRefs); e == nil {
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
	return &requirement.Workflow{
		LLM:              gen,
		R3ChatModel:      r.R3Model,
		SkillTool:        skillTool,
		SDK:              r.SDK,
		Dispatcher:       r.Dispatcher,
		Merger:           r.Merger,
		Tools:            r.ToolPool,
		ToolSpecs:        r.ToolSpecs,
		Knowledge:        r.Knowledge,
		SkillText:        r.skillText,
		Log:              r.Log,
		ReqBodyMaxChars:  r.Cfg.ReqBodyMaxChars(),
		ReqIssueMaxChars: r.Cfg.ReqIssueMaxChars(),
		GetFileMaxChars:  r.Cfg.GetFileMaxRunes(),
		GetIssueMaxChars: r.Cfg.GetIssueMaxRunes(),
		GetDiffMaxChars:  r.Cfg.GetDiffMaxRunes(),
		ReflectRounds:    r.ReflectRounds,
		ReflectModel:     r.ReflectModel,
	}
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	// 任务级成本记账（P0）：obsx OnUsage 回收全部 eino 模型调用的真实 usage——
	// 覆盖 Generate 直连路径与 ReAct agent 的 RawModel 旁路（llm.Client.OnUsage
	// 只覆盖前者）。PreviewLen 刻意留 0（不落消息内容），勿"调观测"时误设。
	taskCost := llm.NewCostTracker()
	obsOpt := obsx.Options{SlowThreshold: 60 * time.Second}
	if r.Cost != nil {
		obsOpt.OnUsage = func(component, mdl, stage string, prompt, completion int) {
			taskCost.Record(mdl, stage, prompt, completion, r.costRate(mdl))
			r.Cost.Record(mdl, stage, prompt, completion, r.costRate(mdl))
		}
	}
	ctx = obsx.InitLLMObservability(ctx, r.Log, obsOpt)
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	start := time.Now()
	budget := plugin.NewTokenBudget(r.Cfg.EffectiveTaskTokenBudget())
	wf := r.buildWorkflow(budget)
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
			_ = r.Store.FinishTask(ctx, task.ID, storage.StatusFailed, "", "", "", reason, 0, 0)
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
	// §10.2：上下文/LLM 阶段失败 → task=failed + 指标 + 日志，不在 PR 评论
	r.Log.Error("argus.runner.task_failed", "task", taskID, "error", err.Error())
	_ = r.Store.FinishTask(ctx, task.ID, storage.StatusFailed, "", "", "", err.Error(), budget.Used(), 0)
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, err.Error())
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
	return nil // 已终结为 failed，不再向 worker 上抛
}

func (r *Runner) run(ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *plugin.TokenBudget, start time.Time, taskCost *llm.CostTracker) error {

	pr, err := r.buildPRContext(ctx, task)
	if err != nil {
		return err
	}
	diff, err := r.buildDiffIncremental(ctx, pr, task)
	if err != nil {
		return err
	}
	// 增量审查：diff 与上次完全一致 → 标记 stale，跳过 LLM
	if diff.NoNewChanges {
		r.Log.Info("argus.runner.no_new_changes", "task", task.ID,
			"pr", fmt.Sprintf("%s/%s/%s#%s", task.Platform, task.Owner, task.Repo, task.Number))
		_ = r.Store.FinishTask(ctx, task.ID, storage.StatusStale, "", "", "",
			"增量审查：diff 与上次审查完全一致，无新变更", 0, 0)
		r.announceTerminal(ctx, task, storage.StatusStale, "", nil, "")
		plugin.ReportTaskTotal(pr.Platform, task.Kind, storage.StatusStale)
		return nil
	}
	if task.HeadSHA == "" && pr.HeadSHA != "" {
		_ = r.Store.SetTaskHeadSHA(ctx, task.ID, pr.HeadSHA)
	}

	ptask := plugin.Task{
		ID:        task.ID,
		Kind:      task.Kind,
		PR:        *pr,
		Diff:      diff,
		Artifacts: decodeArtifacts(task.ArtifactsJSON),
	}

	var out *requirement.Output
	switch task.Kind {
	case requirement.KindRequirementCompliance:
		out, err = wf.Run(ctx, ptask, budget)
	default: // code_review / visual_check：R4+R5 only
		out, err = wf.RunReviewOnly(ctx, ptask, budget)
	}
	if err != nil {
		return err
	}

	// 组装报告并回帖（staleness 门控在 poster 内）
	rep := r.buildReport(task, pr, out, taskCost)
	rep.VerdictText = reportview.VerdictText(out.Rule, countMissingPartial(out.Report))
	rep.Rule = out.Rule

	res, err := r.Poster.Post(ctx, task, *pr, diff, out.Verdict, rep)
	if err != nil {
		// 回帖最终失败 → task=failed + {plugin=poster} 计数（§10.2）
		plugin.ReportPluginFailure("poster")
		return fmt.Errorf("回帖失败: %w", err)
	}

	resultJSON, _ := json.Marshal(toResultPayload(out, diff.Text))
	status := storage.StatusDone
	if res.Stale {
		status = storage.StatusStale // findings/指纹已入库，不回帖不覆盖（§10.2）
	}
	if err := r.Store.FinishTask(ctx, task.ID, status, out.Verdict, reportview.Render(rep),
		string(resultJSON), "", out.Tokens, res.SummaryCommentID); err != nil {
		return err
	}
	// 调试产物留存（ARGUS_DEBUG=1）：R1/R2/R3 原始输出随任务可查（GET /tasks/:id 的 debug 字段）
	if r.Cfg.Debug && len(out.Debug) > 0 {
		if b, e := json.Marshal(out.Debug); e == nil {
			_ = r.Store.SetTaskDebug(ctx, task.ID, string(b))
		}
	}
	r.announceTerminal(ctx, task, status, out.Verdict, out, "")
	plugin.ReportTaskTotal(pr.Platform, task.Kind, status)
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

// buildDiff 拉取并清洗 diff（R2/R4 共用的 diffprep 层）。
func (r *Runner) buildDiff(ctx context.Context, pr *plugin.PRContext) (*plugin.DiffContext, error) {
	md, err := r.SDK.GetCRDiff(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 diff 失败: %w", err)
	}
	return argusctx.Prepare(md, r.Cfg.MaxDiffCharsResolved()), nil
}

// buildDiffIncremental 拉取 diff 并尝试增量清洗（diff-of-diffs）。
// 仅在 synchronize 事件（PR 更新）且存在前置审查记录时激活增量路径；
// 其余场景退化为全量 Prepare。
func (r *Runner) buildDiffIncremental(ctx context.Context, pr *plugin.PRContext, task *storage.Task) (*plugin.DiffContext, error) {
	md, err := r.SDK.GetCRDiff(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 diff 失败: %w", err)
	}
	totalLimit := r.Cfg.MaxDiffCharsResolved()

	// 非 synchronize 事件 → 全量审查（首次审查 / reopen 等）
	if task.EventType != "cr.synchronize" {
		return argusctx.Prepare(md, totalLimit), nil
	}

	// 查询同 PR 最近一次 done 任务的 diff 文本
	lastDiff, err := r.Store.GetLastReviewedDiff(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		r.Log.Warn("argus.runner.get_last_diff_failed", "task", task.ID, "error", err.Error())
		return argusctx.Prepare(md, totalLimit), nil // 降级全量
	}

	if lastDiff == "" {
		return argusctx.Prepare(md, totalLimit), nil // 无前置记录 → 全量
	}

	r.Log.Info("argus.runner.incremental_diff", "task", task.ID,
		"last_diff_chars", len(lastDiff))
	return argusctx.PrepareIncremental(md, lastDiff, totalLimit), nil
}

// costRate 模型单价（主/备模型按配置费率，其余按 0 计）。
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

// postStartMarker 审查开始时在 MR 上留一条"审查中"评论，便于用户感知进度。
// 失败不阻塞审查流程（仅告警）。
func (r *Runner) postStartMarker(ctx context.Context, task *storage.Task) {
	if r.SDK == nil {
		return
	}
	body := fmt.Sprintf(
		"🔍 Argus 正在审查中，请稍候…\n\n<!-- argus:start task_id=%s -->",
		task.ID,
	)
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
