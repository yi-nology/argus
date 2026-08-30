package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/git-platform-sdk/provider"

	"argus/internal/llm"
	"argus/internal/plugin"
)

// issueRefRe 从分支名/PR 描述中提取关联 issue（需求源三入口之一）。
var issueRefRe = regexp.MustCompile(`#(\d{1,8})`)

// maxIssueRefs 关联 issue 数量上限（防恶意刷引用）。
const maxIssueRefs = 5

// ParseRequirement R1：需求解析 → RequirementSpec。
// 需求源：PR 描述 / 关联 issue / 分支名 #N。LLM 失败按"需求源全缺"降级（§10.2）。
func (w *Workflow) ParseRequirement(ctx context.Context, task plugin.Task) (*plugin.RequirementSpec, error) {
	start := time.Now()
	defer func() { plugin.ReportStageDuration("R1", time.Since(start)) }()

	pr := task.PR
	var sb strings.Builder
	if strings.TrimSpace(pr.Body) != "" {
		sb.WriteString(DataFence("PR 描述", pr.Body))
		sb.WriteString("\n")
	}
	if pr.HeadBranch != "" {
		sb.WriteString(DataFence("分支名", pr.HeadBranch))
		sb.WriteString("\n")
	}

	// 关联 issue：分支名 + 描述中 #N（去重，封顶）
	refs := extractIssueRefs(pr.HeadBranch + " " + pr.Body)
	for _, num := range refs {
		issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, num)
		if err != nil {
			if provider.IsNotFound(err) {
				continue
			}
			w.Log.Warn("argus.r1.issue_fetch_failed", "issue", num, "error", err.Error())
			continue
		}
		fmt.Fprintf(&sb, "%s\n", DataFence("issue #"+num+" "+issue.Title, issue.Body))
	}

	if strings.TrimSpace(sb.String()) == "" {
		// 需求源全缺：task 照走，R1 产出空清单 + Ambiguous=[原文] → R5.3 → COMMENT（§10.2）
		return &plugin.RequirementSpec{
			Source:    "未关联",
			Ambiguous: []string{"未在 PR 描述、分支名中找到关联需求，请补充需求说明"},
		}, nil
	}

	var spec plugin.RequirementSpec
	msgs := []*schema.Message{
		schema.SystemMessage(r1System),
		schema.UserMessage(sb.String() + "\n请解析以上需求信息，输出验收清单 JSON。"),
	}
	if err := w.LLM.GenerateJSON(ctx, "R1", msgs, &spec); err != nil {
		w.Log.Warn("argus.r1.degrade", "error", err.Error())
		// 解析降级：全部当 ambiguous → 人工确认路径（结果不丢）
		return &plugin.RequirementSpec{
			Source:    "解析降级",
			Ambiguous: []string{"需求解析失败，需人工确认：" + truncSummary(sb.String(), 300)},
		}, nil
	}
	if spec.Source == "" {
		spec.Source = firstNonEmpty(refs2source(refs), "PR 描述")
	}
	if warnings := SanitizeSpec(&spec); len(warnings) > 0 {
		for _, wn := range warnings {
			w.Log.Warn("argus.r1.injection_suspect", "warning", wn)
		}
	}
	// 补充关联 issue 引用便于追溯
	spec.IssueRefs = refs
	// 防线 #3：空清单也要有出处
	if len(spec.ACs) == 0 && len(spec.Ambiguous) == 0 {
		spec.Ambiguous = append(spec.Ambiguous, "需求信息未能改写出可判定条目，需人工确认")
	}
	return &spec, nil
}

// SummarizeChange R2：变更理解 → ChangeSummary（R3/R4 复用的 diff 公共前缀，省 token）。
func (w *Workflow) SummarizeChange(ctx context.Context, task plugin.Task) (string, error) {
	start := time.Now()
	defer func() { plugin.ReportStageDuration("R2", time.Since(start)) }()

	if task.Diff == nil || strings.TrimSpace(task.Diff.Text) == "" {
		return "（无有效 diff 内容）", nil
	}
	msgs := []*schema.Message{
		schema.SystemMessage(r2System),
		schema.UserMessage(DataFence("git diff（已清洗）", task.Diff.Text)),
	}
	resp, err := w.LLM.Generate(ctx, "R2", msgs)
	if err != nil {
		return "", err
	}
	return truncSummary(resp.Content, 1200), nil
}

// CheckCompliance R3：完成度判定 → ComplianceReport（ADK ReAct agent + 工具）。
// JSON 解析失败重试 1 次后仍败 → 全部 AC 置 unverifiable（§10.2，结果不丢）。
func (w *Workflow) CheckCompliance(ctx context.Context, task plugin.Task, spec *plugin.RequirementSpec, changeSummary string) (*plugin.ComplianceReport, string, error) {
	start := time.Now()
	defer func() { plugin.ReportStageDuration("R3", time.Since(start)) }()

	if spec == nil || len(spec.ACs) == 0 {
		return nil, "", nil // 无清单：R3 跳过（R5.3 由 Ambiguous/空清单兜底）
	}

	// 工具表：get_file / get_issue / get_diff（+ toolpool 扩展）
	tools, err := w.r3Tools(ctx, task)
	if err != nil {
		return nil, "", fmt.Errorf("R3 工具构造失败: %w", err)
	}

	instruction := r3System
	if w.SkillText != nil {
		if s := w.SkillText(ctx); s != "" {
			// §9.1 方法论注入：拼进系统提示词尾部（含 checksum 可排查漂移）
			instruction += "\n\n===【审查方法论（skill 注入）】===\n" + s
		}
	}

	user := strings.Builder{}
	user.WriteString(DataFence("验收清单", renderACs(spec)))
	user.WriteString("\n")
	if changeSummary != "" {
		user.WriteString(DataFence("变更摘要（R2）", changeSummary))
		user.WriteString("\n")
	}
	if task.Diff != nil {
		user.WriteString(DataFence("git diff（已清洗）", task.Diff.Text))
		user.WriteString("\n")
	}
	user.WriteString("\n请逐条判定验收清单完成度并输出 JSON。")

	query := user.String()
	final, err := w.runAgent(ctx, instruction, query, tools)
	if err != nil {
		w.Log.Warn("argus.r3.agent_failed", "error", err.Error())
	} else {
		var report plugin.ComplianceReport
		if e := json.Unmarshal([]byte(llm.ExtractJSON(final)), &report); e == nil {
			down := EnforceEvidence(&report) // 防线 #2 代码级校验
			if down > 0 {
				w.Log.Warn("argus.r3.evidence_downgraded", "count", down)
			}
			return &report, final, nil
		}
		// 解析失败：带错误重试 1 次（把错误作为追加轮询消息）
		retryQ := query + "\n\n你上一轮输出无法解析为合法 JSON，请重新输出且只输出合法 JSON。上一轮输出：\n" + truncSummary(final, 800)
		if final2, e2 := w.runAgent(ctx, instruction, retryQ, tools); e2 == nil {
			var report2 plugin.ComplianceReport
			if e := json.Unmarshal([]byte(llm.ExtractJSON(final2)), &report2); e == nil {
				EnforceEvidence(&report2)
				return &report2, final2, nil
			}
		}
	}

	// 仍失败：全部 AC 置 unverifiable → R5.3 COMMENT（降级为人工确认，不丢结果）
	degraded := &plugin.ComplianceReport{
		Summary: "R3 判定失败（模型输出不可解析），已全部降级为需人工确认",
	}
	for _, ac := range spec.ACs {
		degraded.Checks = append(degraded.Checks, plugin.ACCheck{
			ID: ac.ID, Status: plugin.ACUnverifiable,
			Detail: "自动判定不可用，请人工确认",
		})
	}
	return degraded, "", nil
}

// runAgent 构造并运行 ADK ReAct agent，drain 事件流取最终 assistant 文本。
func (w *Workflow) runAgent(ctx context.Context, instruction, query string, tools []tool.BaseTool) (string, error) {
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "argus-compliance",
		Description:   "需求完成度判定专家",
		Instruction:   instruction,
		Model:         w.LLM.Model,
		MaxIterations: 12,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return "", fmt.Errorf("构造 agent 失败: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: false})
	iter := runner.Query(ctx, query)

	var finalText string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return "", fmt.Errorf("agent 事件错误: %w", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mv := event.Output.MessageOutput
		if mv.Role == schema.Assistant && mv.Message != nil && len(mv.Message.ToolCalls) == 0 && mv.Message.Content != "" {
			finalText = mv.Message.Content // ReAct 出口：assistant 无 tool_calls
		}
	}
	if finalText == "" {
		return "", fmt.Errorf("agent 未产出最终文本")
	}
	return finalText, nil
}

// r3Tools R3 的工具表（每个任务构造一次，闭包绑定任务上下文）。
func (w *Workflow) r3Tools(ctx context.Context, task plugin.Task) ([]tool.BaseTool, error) {
	pr := task.PR

	getFile, err := utils.InferTool("get_file",
		"读取仓库某文件在 PR head 提交时的完整内容，用于核实 diff 中看不到的实现。参数 path 为仓库相对路径。",
		func(ctx context.Context, in *getFileIn) (*getFileOut, error) {
			content, err := w.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo, in.Path, pr.HeadSHA)
			if err != nil {
				return &getFileOut{Error: err.Error()}, nil // 工具错误回注文本，不中断 ReAct
			}
			if len(content) > 20000 {
				content = content[:20000] + "\n…（截断）"
			}
			return &getFileOut{Content: content}, nil
		})
	if err != nil {
		return nil, err
	}

	getIssue, err := utils.InferTool("get_issue",
		"读取本仓库某个 issue 的标题与正文（补充需求上下文）。",
		func(ctx context.Context, in *getIssueIn) (*getIssueOut, error) {
			issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, in.Number)
			if err != nil {
				return &getIssueOut{Error: err.Error()}, nil
			}
			body := issue.Body
			if len(body) > 8000 {
				body = body[:8000] + "…（截断）"
			}
			return &getIssueOut{Title: issue.Title, Body: body}, nil
		})
	if err != nil {
		return nil, err
	}

	getDiff, err := utils.InferTool("get_diff",
		"重新获取本次 PR 的清洗后 diff 文本（当上下文中 diff 被截断时使用）。",
		func(ctx context.Context, _ *emptyIn) (*getDiffOut, error) {
			if task.Diff != nil {
				text := task.Diff.Text
				if len(text) > 30000 {
					text = text[:30000] + "…（截断）"
				}
				return &getDiffOut{Diff: text}, nil
			}
			return &getDiffOut{Diff: ""}, nil
		})
	if err != nil {
		return nil, err
	}

	tools := []tool.BaseTool{getFile, getIssue, getDiff}
	if w.Tools != nil {
		extra, err := w.Tools.Tools(ctx, w.ToolSpecs)
		if err != nil {
			w.Log.Warn("argus.r3.toolpool_error", "error", err.Error())
		} else {
			tools = append(tools, extra...)
		}
	}
	return tools, nil
}

// ---------- 小工具 ----------

type getFileIn struct {
	Path string `json:"path" jsonschema:"description=仓库相对路径"`
}
type getFileOut struct {
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}
type getIssueIn struct {
	Number string `json:"number" jsonschema:"description=issue 编号（纯数字）"`
}
type getIssueOut struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Error string `json:"error,omitempty"`
}
type emptyIn struct{}
type getDiffOut struct {
	Diff string `json:"diff"`
}

func extractIssueRefs(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range issueRefRe.FindAllStringSubmatch(s, -1) {
		if seen[m[1]] || m[1] == "0" {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
		if len(out) >= maxIssueRefs {
			break
		}
	}
	return out
}

func refs2source(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, "issue #"+r)
	}
	return strings.Join(parts, ",")
}

func renderACs(spec *plugin.RequirementSpec) string {
	var b strings.Builder
	if spec.Source != "" {
		fmt.Fprintf(&b, "需求来源：%s\n", spec.Source)
	}
	for _, ac := range spec.ACs {
		fmt.Fprintf(&b, "%s: %s\n", ac.ID, ac.Text)
	}
	if len(spec.Ambiguous) > 0 {
		b.WriteString("模糊点（改写不了，判定时如无法核实请给 unverifiable）：\n")
		for _, a := range spec.Ambiguous {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	return b.String()
}

func truncSummary(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
