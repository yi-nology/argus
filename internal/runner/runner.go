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
	"argus/internal/knowledge/skill"
	"argus/internal/llm"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poster"
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
	return &requirement.Workflow{
		LLM:        llm.NewClient(r.Model, r.ModelName, budget),
		SDK:        r.SDK,
		Dispatcher: r.Dispatcher,
		Merger:     r.Merger,
		Tools:      r.ToolPool,
		ToolSpecs:  r.ToolSpecs,
		SkillText:  r.skillText,
		Log:        r.Log,
	}
}

// skillText R3 方法论注入：聚合全部 builtin 插件 skillRef 解析结果（Runner 装配期填充）。
// 由 main 在构造 Runner 时赋值（见 internal/bootstrap 装配代码）。
var _ = func() {}

// Execute 执行一个已 claim（running）的任务。始终终结任务状态（done/failed/stale）。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	start := time.Now()
	budget := plugin.NewTokenBudget(r.Cfg.TaskTokenBudget)
	wf := r.buildWorkflow(budget)

	err = r.run(ctx, task, wf, budget, start)
	if err == nil {
		return nil
	}
	// §10.2：上下文/LLM 阶段失败 → task=failed + 指标 + 日志，不在 PR 评论
	r.Log.Error("argus.runner.task_failed", "task", taskID, "error", err.Error())
	_ = r.Store.FinishTask(ctx, task.ID, storage.StatusFailed, "", "", "", err.Error(), budget.Used(), 0)
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

	res, err := r.Poster.Post(ctx, task, *pr, out.Verdict, rep)
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

func hasHigh(m *plugin.MergedFindings) bool {
	if m == nil {
		return false
	}
	for _, f := range m.Findings {
		if f.Severity == plugin.SeverityHigh {
			return true
		}
	}
	return false
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
