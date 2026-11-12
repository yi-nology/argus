// runner_report.go —— 报告产物与公告面：buildReport/ResultPayload/终态回帖/进度发布/start marker/审查提示注入。

package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// costRate 模型单价（v3.36.0 起统一按配置费率近似，未知模型不再按 0 计）。
func (r *Runner) costRate(mdl string) [2]float64 {
	if r.CostPer1K != nil {
		return r.CostPer1K(mdl)
	}
	return [2]float64{}
}

// cacheAdjustedInput 按缓存命中折扣摊薄输入单价（v3.180.0 成本口径修正）。
// cached ⊆ prompt 是命中前缀缓存的 prompt token（便宜得多，cacheRatio=0.1）；
// 旧记账把整块 prompt 按全价入账 → 成本高估。此处把 InputPerM 摊薄为
// (uncached + cached×ratio)/prompt，**token 计数保持真实**（PromptTokens 不被
// 「等效计费量」污染）。cached<=0 或 prompt<=0 时原价返回；cached 超 prompt 钳回。
func cacheAdjustedInput(inputPerM float64, prompt, cached int, cacheRatio float64) float64 {
	if cached <= 0 || prompt <= 0 {
		return inputPerM
	}
	if cached > prompt {
		cached = prompt
	}
	blended := (float64(prompt-cached) + float64(cached)*cacheRatio) / float64(prompt)
	return inputPerM * blended
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
		FixHint: r.Cfg.FixAgent != "", // v3.85.0：fixer 启用时意见行附一键修复入口
	}
	// v6.10.0 报告契约模板：渲染分发键与综合段产物（finish 从 Artifacts 回声到
	// Output，这里直读；Analyst 为 nil（非 analyst/降级）时渲染层自然退化为
	// standard 骨架）。
	rep.Template = out.ReportTemplate
	rep.Analyst = out.Analyst
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
				view.Evid = textutil.FirstNonEmpty(c.Evidence, c.Detail)
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
	// Analyst 分析师综合段产物（v6.10.0；nil = 非 analyst/综合段失败降级）。
	// 结构化消费面（报告页/IDE）据此渲染声明对照/结构维度，无需解析 markdown。
	Analyst *plugin.AnalystSections `json:"analyst,omitempty"`
	// ReportTemplate 本任务裁决后的报告模板键（v6.10.0）。
	ReportTemplate string `json:"report_template,omitempty"`
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
	p.Analyst = out.Analyst
	p.ReportTemplate = out.ReportTemplate
	return p
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

// publish 广播进度事件（nil Bus 安全）+ 落 outbox（P1-05，#10 跨实例分发）。
func (r *Runner) publish(typ string, task *storage.Task, status, verdict string) {
	if r.Bus == nil || task == nil {
		return
	}
	ev := progress.Event{
		Type: typ, TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		Status: status, Verdict: verdict,
	}
	r.publishOutbox(ev)
}

// publishStage 阶段级进度事件（task.stage；v3.40.0 SSE 阶段可视化数据源）。
func (r *Runner) publishStage(task *storage.Task, stage string) {
	if r.Bus == nil || task == nil {
		return
	}
	r.publishOutbox(progress.Event{
		Type: progress.EventTaskStage, TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		Stage: stage,
	})
}

// publishOutbox 进程内 Bus + outbox 双写（与 service.publishProgress 同语义：
// 本实例实时、跨实例 fanout 回灌；追加失败计数+告警不阻断）。
func (r *Runner) publishOutbox(ev progress.Event) {
	ev.ID = progress.NewEventID()
	r.Bus.Publish(ev)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped("outbox_failed")
		r.Log.Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
}

// stageCNNames 执行期进度条的阶段中文名（v6.1.5）。键一律引用 plugin.Layer*
// 常量（词表单一出处 plugin/hooks.go StageTimeline），覆盖审查管线全部阶段
// R1–R5；子阶段（R1a）经 stageSlot 前缀回退归到主阶段槽位。
var stageCNNames = map[string]string{
	plugin.LayerR1:  "需求解析",
	plugin.LayerR2:  "变更理解",
	plugin.LayerR3:  "完成度判定",
	plugin.LayerR35: "报告反思",
	plugin.LayerR4:  "专家插件",
	plugin.LayerR5:  "终审裁决",
}

// stageSlot stage 在 StageTimeline 中的槽位：精确匹配优先；未命中回退前缀匹配
// （与 llm.StageRouter 同语义——LayerR1 前缀命中 LayerR1A 子阶段），最长前缀胜出。
// 词表外（含空串）返回 -1。
func stageSlot(stage string) int {
	for i, s := range plugin.StageTimeline {
		if s == stage {
			return i
		}
	}
	best := -1
	for i, s := range plugin.StageTimeline {
		if strings.HasPrefix(stage, s) && (best < 0 || len(s) > len(plugin.StageTimeline[best])) {
			best = i
		}
	}
	return best
}

// stageProgressBar 执行期阶段进度条（v6.1.5）：已过 ✅、当前 ⏳、未到显式
// 「未开始」，顺序即 plugin.StageTimeline 规范序。current 不在词表时全部按
// 「未开始」展示（保守：宁可不声称进度，也不错报完成）。纯函数，独立可测。
func stageProgressBar(current string) string {
	cur := stageSlot(current)
	parts := make([]string, 0, len(plugin.StageTimeline))
	for i, s := range plugin.StageTimeline {
		switch {
		case i == cur:
			parts = append(parts, fmt.Sprintf("%s %s ⏳", s, stageCNNames[s]))
		case cur >= 0 && i < cur:
			parts = append(parts, fmt.Sprintf("%s %s ✅", s, stageCNNames[s]))
		default:
			parts = append(parts, fmt.Sprintf("%s %s 未开始", s, stageCNNames[s]))
		}
	}
	return "进度：" + strings.Join(parts, " · ")
}

// startMarkerProgressBody 执行期 start marker 文案（v6.1.5）：「审查中」+ 阶段
// 进度条 + 幂等锚点。锚点格式与 postStartMarker 初版及三态收口一致，setStartMarker
// 扫锚点原地更新的定位逻辑复用同一条锚点线。
func startMarkerProgressBody(taskID, stage string) string {
	return fmt.Sprintf("🔍 Argus 审查中…\n\n%s\n\n<!-- argus:start task_id=%s -->",
		stageProgressBar(stage), taskID)
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
	r.publish(progress.EventTaskFinished, task, status, verdict)
	// 审计留痕（v3.86.0）：verdict 终态是审查链路的关键可审计动作
	r.Audit.Log("verdict.posted", "task", task.ID, "platform", task.Platform,
		"owner", task.Owner, "repo", task.Repo, "number", task.Number,
		"status", status, "verdict", verdict, "rule", ruleOf(out))
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

// ruleOf 审计字段用：终态命中的 R5 规则（out 为 nil 时空串）。
func ruleOf(out *requirement.Output) string {
	if out == nil {
		return ""
	}
	return out.Rule
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
	out := b.String()
	// 注入留痕（可观测性补齐）：提示词面注入此前零日志——线上排查「为什么这轮
	// 意见变少」时无法区分提示词收敛与模型随机性。
	r.Log.Info("argus.regret.hints_injected", "pr", fmt.Sprintf("%s/%s/%s#%s",
		pr.Platform, pr.Owner, pr.Repo, pr.Number), "patterns", len(list), "bytes", len(out))
	return out
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
