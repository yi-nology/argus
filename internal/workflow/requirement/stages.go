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

// 缺省需求文本截断上限（字符）——Workflow 未装配 config 派生值时的兜底。
const (
	defaultReqBodyMaxChars  = 24_000
	defaultReqIssueMaxChars = 8_000
)

// maxACs R1 产出的验收条目上限：超限条目转 Ambiguous（R5.3 人工确认），
// 防超长需求改写出上百条 AC 拖垮 R3、也让"清单过大"显式落人工拆分。
const maxACs = 30

// maxACTextRunes 单条 AC 文本上限（字符），防 LLM 把整段原文塞进单条 AC。
const maxACTextRunes = 500

// maxDigestChunks 两级提炼时单段最多提炼的分块数（每块 bodyMax 字符）——
// 超出部分截断留痕，防恶意超长文本制造海量 LLM 调用。
const maxDigestChunks = 8

// reqSegment R1 需求源分段（PR 描述 / 各关联 issue）。
type reqSegment struct {
	label     string
	content   string
	rawChars  int
	truncated bool
}

// ParseRequirement R1：需求解析 → RequirementSpec。
// 需求源：PR 描述 / 关联 issue / 分支名 #N。LLM 失败按"需求源全缺"降级（§10.2）。
// 长度策略：单段硬上限（bodyMax×8）→ 总量 ≤ bodyMax 原文直进单次解析；
// 超过则两级提炼（R1a 按 bodyMax 分块逐块提炼要点 → 合并后进 R1 解析）。
// 任何截断/提炼路径都留痕 Ambiguous → R5.3 人工确认，绝不静默丢内容。
func (w *Workflow) ParseRequirement(ctx context.Context, task plugin.Task) (*plugin.RequirementSpec, error) {
	start := time.Now()
	defer func() { plugin.ReportStageDuration("R1", time.Since(start)) }()

	bodyMax, issueMax := w.ReqBodyMaxChars, w.ReqIssueMaxChars
	if bodyMax <= 0 {
		bodyMax = defaultReqBodyMaxChars
	}
	if issueMax <= 0 {
		issueMax = defaultReqIssueMaxChars
	}
	digestMax := bodyMax * 8

	pr := task.PR
	var truncNotes []string
	segments := make([]reqSegment, 0, 6)
	if s := strings.TrimSpace(pr.Body); s != "" {
		seg := reqSegment{label: "PR 描述", content: s, rawChars: len([]rune(s))}
		if t, truncated := truncRunes(s, digestMax); truncated {
			seg.content, seg.truncated = t, true
			truncNotes = append(truncNotes, fmt.Sprintf("PR 描述超长已截断（原 %d 字符，保留前 %d），请人工核对完整性", seg.rawChars, digestMax))
		}
		segments = append(segments, seg)
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
		raw := issue.Title + "\n" + issue.Body // 标题同样攻击者可控，一起进数据区
		seg := reqSegment{label: "issue #" + num, content: raw, rawChars: len([]rune(raw))}
		if t, truncated := truncRunes(raw, issueMax*8); truncated {
			seg.content, seg.truncated = t, true
			truncNotes = append(truncNotes, fmt.Sprintf("issue #%s 正文超长已截断（原 %d 字符，保留前 %d），请人工核对完整性", num, seg.rawChars, issueMax*8))
		}
		segments = append(segments, seg)
	}

	total := 0
	for _, seg := range segments {
		total += len([]rune(seg.content))
	}
	if total == 0 && strings.TrimSpace(pr.HeadBranch) == "" {
		// 需求源全缺：task 照走，R1 产出空清单 + Ambiguous=[原文] → R5.3 → COMMENT（§10.2）
		return &plugin.RequirementSpec{
			Source:    "未关联",
			Ambiguous: []string{"未在 PR 描述、分支名中找到关联需求，请补充需求说明"},
		}, nil
	}

	// 组装 R1 输入：总量 ≤ bodyMax 原文直进；超过则两级提炼
	var sb strings.Builder
	if pr.HeadBranch != "" {
		sb.WriteString(DataFence("分支名", pr.HeadBranch))
		sb.WriteString("\n")
	}
	digested := false
	if total > bodyMax {
		digested = true
		for _, seg := range segments {
			digest, notes := w.digestSegment(ctx, seg, bodyMax)
			truncNotes = append(truncNotes, notes...)
			fmt.Fprintf(&sb, "%s\n", DataFence(seg.label+"（分段提炼要点）", digest))
		}
	} else {
		for _, seg := range segments {
			fmt.Fprintf(&sb, "%s\n", DataFence(seg.label, seg.content))
		}
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
	// 截断/两级提炼留痕：Ambiguous 非空 → R5.3 COMMENT（人工确认），绝不静默
	for _, n := range truncNotes {
		spec.Ambiguous = append(spec.Ambiguous, "[超长截断] "+n)
		w.Log.Warn("argus.r1.requirement_truncated", "note", n)
	}
	if digested {
		spec.Ambiguous = append(spec.Ambiguous, "[两级提炼] 需求原文超长，已按分段提炼要点后解析，建议人工核对需求完整性")
		w.Log.Warn("argus.r1.two_stage_digest", "segments", len(segments), "total_chars", total)
	}
	// 防线 #3：空清单也要有出处
	if len(spec.ACs) == 0 && len(spec.Ambiguous) == 0 {
		spec.Ambiguous = append(spec.Ambiguous, "需求信息未能改写出可判定条目，需人工确认")
	}
	// AC 条数上限：超限条目不丢弃，转 Ambiguous 落人工确认
	if len(spec.ACs) > maxACs {
		excess := spec.ACs[maxACs:]
		ids := make([]string, 0, len(excess))
		for _, ac := range excess {
			ids = append(ids, ac.ID)
		}
		w.Log.Warn("argus.r1.ac_cap", "total", len(spec.ACs), "cap", maxACs)
		spec.Ambiguous = append(spec.Ambiguous, fmt.Sprintf(
			"[条目超限] 验收清单解析出 %d 条，超出 %d 条上限，多余条目（%s）转人工确认，建议拆分需求",
			len(spec.ACs)+len(excess), maxACs, strings.Join(ids, ",")))
		spec.ACs = spec.ACs[:maxACs]
	}
	// 单条 AC 文本截断（LLM 偶发把整段原文塞进单条 AC）
	for i := range spec.ACs {
		if t, truncated := truncRunes(spec.ACs[i].Text, maxACTextRunes); truncated {
			spec.ACs[i].Text = t + "…（截断）"
		}
	}
	return &spec, nil
}

// digestSegment R1a：单段需求按 chunkSize 分块，逐块提炼要点后拼接（单块输出 ≤4000 字）。
// 任一块提炼失败则整段回退为截断原文，并通过 notes 留痕（→ Ambiguous → R5.3）。
func (w *Workflow) digestSegment(ctx context.Context, seg reqSegment, chunkSize int) (string, []string) {
	var notes []string
	chunks := chunkRunes(seg.content, chunkSize)
	if len(chunks) > maxDigestChunks {
		notes = append(notes, fmt.Sprintf("%s 超长（原 %d 字符），仅提炼前 %d 块（每块 %d 字符），请人工核对完整性",
			seg.label, seg.rawChars, maxDigestChunks, chunkSize))
		chunks = chunks[:maxDigestChunks]
	}
	parts := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		msgs := []*schema.Message{
			schema.SystemMessage(r1DigestSystem),
			schema.UserMessage(DataFence(seg.label+fmt.Sprintf(" 需求片段 %d/%d（数据）", i+1, len(chunks)), ch) +
				"\n请提炼以上需求片段的全部要点。"),
		}
		resp, err := w.LLM.Generate(ctx, "R1a", msgs)
		if err != nil {
			w.Log.Warn("argus.r1.digest_failed", "segment", seg.label, "chunk", i+1, "error", err.Error())
			notes = append(notes, fmt.Sprintf("%s 片段提炼失败（块 %d/%d），以截断原文参与解析", seg.label, i+1, len(chunks)))
			return seg.content, notes
		}
		parts = append(parts, truncSummary(resp.Content, 4000))
	}
	return strings.Join(parts, "\n"), notes
}

// chunkRunes 按 rune 等分为 ≤n 字符的块（最后一块可不足）。
func chunkRunes(s string, n int) []string {
	if n <= 0 {
		return []string{s}
	}
	r := []rune(s)
	var out []string
	for len(r) > 0 {
		k := n
		if len(r) < k {
			k = len(r)
		}
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
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
			dropped, fixed := alignChecksToSpec(spec, &report) // 双向对齐（防发明条目放大 R5.1，也防漏报缩水成 APPROVE）
			if dropped > 0 {
				w.Log.Warn("argus.r3.checks_dropped", "count", dropped)
			}
			if fixed > 0 {
				w.Log.Warn("argus.r3.checks_aligned", "count", fixed)
			}
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
				alignChecksToSpec(spec, &report2)
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
	fileMax, issueMax, diffMax := w.GetFileMaxChars, w.GetIssueMaxChars, w.GetDiffMaxChars
	if fileMax <= 0 {
		fileMax = 20_000
	}
	if issueMax <= 0 {
		issueMax = 8_000
	}
	if diffMax <= 0 {
		diffMax = 30_000
	}

	getFile, err := utils.InferTool("get_file",
		"读取仓库某文件在 PR head 提交时的完整内容，用于核实 diff 中看不到的实现。参数 path 为仓库相对路径。",
		func(ctx context.Context, in *getFileIn) (*getFileOut, error) {
			content, err := w.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo, in.Path, pr.HeadSHA)
			if err != nil {
				return &getFileOut{Error: err.Error()}, nil // 工具错误回注文本，不中断 ReAct
			}
			if rc := len([]rune(content)); rc > fileMax {
				content, _ = truncRunes(content, fileMax)
				content += "\n…（截断）"
			}
			// 文件内容是 PR 作者完全可控的注入面：回注 agent 前必须包数据区（防线 #1）
			return &getFileOut{Content: DataFence("get_file 文件内容（数据）", content)}, nil
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
			if rc := len([]rune(body)); rc > issueMax {
				body, _ = truncRunes(body, issueMax)
				body += "…（截断）"
			}
			// 标题+正文一起进数据区（两者都攻击者可控）
			return &getIssueOut{Body: DataFence("get_issue 标题与正文（数据）", issue.Title+"\n"+body)}, nil
		})
	if err != nil {
		return nil, err
	}

	getDiff, err := utils.InferTool("get_diff",
		"重新获取本次 PR 的清洗后 diff 文本（当上下文中 diff 被截断时使用）。",
		func(ctx context.Context, _ *emptyIn) (*getDiffOut, error) {
			if task.Diff != nil {
				text := task.Diff.Text
				if rc := len([]rune(text)); rc > diffMax {
					text, _ = truncRunes(text, diffMax)
					text += "…（截断）"
				}
				return &getDiffOut{Diff: DataFence("get_diff（数据）", text)}, nil
			}
			return &getDiffOut{Diff: ""}, nil
		})
	if err != nil {
		return nil, err
	}

	tools := []tool.BaseTool{getFile, getIssue, getDiff}
	if w.Knowledge != nil {
		if kt := w.Knowledge.AsTool(); kt != nil {
			tools = append(tools, kt)
		}
	}
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
	if out, truncated := truncRunes(s, n); truncated {
		return out + "…"
	}
	return s
}

// truncRunes 按 rune 截断，避免多字节字符被腰斩产生非法 UTF-8 进 LLM 上下文。
// 第二个返回值 = 是否真发生了截断（rune 口径）——多字节内容"字节超限 rune 未超"时
// 不该打截断标记。
func truncRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
