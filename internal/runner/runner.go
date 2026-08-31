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

	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/yi-nology/git-platform-sdk/provider"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/knowledge/rag"
	"argus/internal/knowledge/skill"
	"argus/internal/llm"
	"argus/internal/notify"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poster"
	"argus/internal/progress"
	"argus/internal/reportview"
	"argus/internal/storage"
	"argus/internal/toolpool"
	"argus/internal/workflow/requirement"
)

// Runner 单任务执行器。
type Runner struct {
	Cfg        *config.Config
	Store      *storage.Store
	SDK        *argusctx.SDKCtx
	Model      model.BaseChatModel // eino 模型（每任务包一层 llm.Client 以隔离预算）
	ModelName  string
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

	// R3 方法论注入（requirement-compliance 插件的 skillRefs，装配期传入）
	SkillProvider skill.Provider
	SkillRefs     []string

	skillOnce  sync.Once
	skillCache string
}

// skillText 惰性解析 skillRef（§9.1：含 checksum，防内容漂移难排查），启动后缓存。
func (r *Runner) skillText(ctx context.Context) string {
	if r.SkillProvider == nil || len(r.SkillRefs) == 0 {
		return ""
	}
	r.skillOnce.Do(func() {
		var parts []string
		for _, ref := range r.SkillRefs {
			name, ver := ref, ""
			if i := strings.Index(ref, "@"); i >= 0 {
				name, ver = ref[:i], ref[i+1:]
			}
			s, err := r.SkillProvider.Resolve(ctx, skill.Ref{Name: name, Version: ver, Source: "file"})
			if err != nil {
				r.Log.Warn("argus.runner.skill_resolve_failed", "ref", ref, "error", err.Error())
				continue
			}
			parts = append(parts, fmt.Sprintf("--- skill:%s (checksum=%s) ---\n%s", s.Name, s.Checksum, s.Content))
		}
		r.skillCache = strings.Join(parts, "\n")
	})
	return r.skillCache
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
	c := llm.NewClient(r.Model, r.ModelName, budget)
	// 窗口与输出预留注入 → Generate 发送前输入自守恒（fitInput）
	c.ContextTokens = r.Cfg.LLM.ContextToken
	c.MaxOutputTokens = r.Cfg.MaxTokensResolved()
	return &requirement.Workflow{
		LLM:              c,
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
	}
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	start := time.Now()
	budget := plugin.NewTokenBudget(r.Cfg.EffectiveTaskTokenBudget())
	wf := r.buildWorkflow(budget)
	r.publish("task.running", task, "", "")
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
			_ = r.Store.FinishTask(ctx, task.ID, storage.StatusFailed, "", "", "",
				fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", used, r.Cfg.RepoMonthlyTokenBudget),
				0, 0)
			r.announceTerminal(task, storage.StatusFailed, "", nil,
				fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", used, r.Cfg.RepoMonthlyTokenBudget))
			plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
			return nil
		}
	}

	err = r.run(ctx, task, wf, budget, start)
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
	r.announceTerminal(task, storage.StatusFailed, "", nil, err.Error())
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
	return nil // 已终结为 failed，不再向 worker 上抛
}

func (r *Runner) run(ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *plugin.TokenBudget, start time.Time) error {

	pr, err := r.buildPRContext(ctx, task)
	if err != nil {
		return err
	}
	diff, err := r.buildDiff(ctx, pr)
	if err != nil {
		return err
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
	rep := r.buildReport(task, pr, out)
	rep.VerdictText = reportview.VerdictText(out.Rule, countMissingPartial(out.Report))
	rep.Rule = out.Rule

	res, err := r.Poster.Post(ctx, task, *pr, diff, out.Verdict, rep)
	if err != nil {
		// 回帖最终失败 → task=failed + {plugin=poster} 计数（§10.2）
		plugin.ReportPluginFailure("poster")
		return fmt.Errorf("回帖失败: %w", err)
	}

	resultJSON, _ := json.Marshal(toResultPayload(out))
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
	r.announceTerminal(task, status, out.Verdict, out, "")
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

// buildReport 报告输入组装（模板渲染交给 reportview）。
func (r *Runner) buildReport(task *storage.Task, pr *plugin.PRContext, out *requirement.Output) reportview.Report {
	rep := reportview.Report{
		TaskID:  task.ID,
		HeadSHA: pr.HeadSHA,
		Version: r.Version,
		Meta:    r.Cfg.ReportMeta,
		Plugins: out.PluginsRun,
		Elapsed: out.Elapsed,
		Tokens:  out.Tokens,
	}
	if out.Merged != nil {
		rep.Findings = out.Merged.Findings
		rep.Degraded = out.Merged.Degraded
		rep.Suppressed = out.Merged.Suppressed
	}
	if out.Spec != nil {
		rep.ShowCompliance = true
		rep.ReqSource = out.Spec.Source
		for _, ac := range out.Spec.ACs {
			view := reportview.ACCheckView{ID: ac.ID, Text: ac.Text, Status: plugin.ACUnverifiable, Evid: "未判定"}
			if out.Report != nil {
				for _, c := range out.Report.Checks {
					if c.ID == ac.ID {
						view.Status = c.Status
						view.Evid = firstNonEmpty(c.Evidence, c.Detail)
						break
					}
				}
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
}

func toResultPayload(out *requirement.Output) *ResultPayload {
	p := &ResultPayload{Verdict: out.Verdict, Rule: out.Rule, Plugins: out.PluginsRun, Summary: out.Summary}
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

// finish 通知：SSE 终态事件 + octopus IM 推送（各自 nil/未配置安全）。
func (r *Runner) announceTerminal(task *storage.Task, status, verdict string, out *requirement.Output, failReason string) {
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
	r.Notify.NotifyTerminal(context.Background(), ev)
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
