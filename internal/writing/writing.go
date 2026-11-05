// Package writing 写作面（v3.71.0，对标 PR-Agent /describe 与 /update_changelog）：
// describe 任务生成 PR 标题+描述、changelog 任务生成变更日志条目。
// 单步 LLM 写作，无 R 系列裁判与插件派发——审查面判"代码对不对"，
// 写作面管"该怎么说"；输入即 diff 与 PR 元数据，输出即平台可写回的文本。
package writing

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
)

// diffMaxChars 写作输入的 diff 截断上限。describe/changelog 消费的是"改了什么"
// 的语义而非逐行细节，60KB 头部足够覆盖绝大多数 MR 的变更意图。
const diffMaxChars = 60_000

// Service 写作面服务。LLM 为 nil 时调用返回错误（装配缺失语义，与 runner 各
// 模型槽位同风格）。
type Service struct {
	LLM llm.Generator
}

// Input 写作输入。
type Input struct {
	Platform      string
	Owner         string
	Repo          string
	Number        string
	Title         string
	Body          string
	Author        string
	SourceBranch  string
	TargetBranch  string
	ChangeSummary string // diffprep 产出的结构化变更摘要（可为空）
	DiffText      string
}

// Description describe 任务的产物：标题与正文。
type Description struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// describePrompt describe 的写作指令。输出 JSON（title/body），约束对齐
// PR-Agent /describe 的产出语义：变更动机 → 变更内容 → 影响面，不虚构。
const describePrompt = `你是资深软件工程师，为一次代码变更撰写 Pull Request 描述。

## 变更信息
- 仓库：%s/%s（%s #%s）
- 分支：%s → %s
- 作者：%s
- 当前标题（可能是不规范的临时名）：%s
- 当前描述（可能为空或草稿）：
%s

## 变更摘要
%s

## Diff（可能截断）
` + "```diff\n%s\n```" + `

## 要求
1. title：一行、动词开头、概括本次变更的核心目的（≤50 字，不用行尾标点）。
2. body：Markdown，依次含「## 变更动机」「## 变更内容」「## 影响面」三节；
   只依据上面给出的信息，禁止虚构未出现的文件/接口/行为；diff 里看不出动机时
   基于"改了什么"合理推断并克制表述。
3. 保持原文语言风格（中文 diff/标题用中文，英文用英文）。

只输出 JSON：{"title": "...", "body": "..."}`

// changelogPrompt changelog 条目写作指令：面向最终用户的变更记录，
// 一条一个要点，类别前缀（Features/Fixes/Docs/Chore…）由模型按变更性质归。
const changelogPrompt = `你是发布管理员，为一次代码变更撰写 CHANGELOG 条目。

## 变更信息
- 仓库：%s/%s（%s #%s），作者 %s
- 标题：%s
- 描述：
%s

## 变更摘要
%s

## Diff（可能截断）
` + "```diff\n%s\n```" + `

## 要求
输出 Markdown 无序列表（每行以 "- " 开头，1~5 条），每条 = 面向使用者的变更
效果（不是实现细节），中文，一条不超过 40 字。可加粗类别前缀，如
"- **Features:** 支持xxx"。只输出列表本身，不加标题或代码围栏。`

// Describe 生成 PR 标题与描述。
func (s *Service) Describe(ctx context.Context, in Input) (Description, error) {
	var out Description
	if s.LLM == nil {
		return out, fmt.Errorf("写作服务未接 LLM 生成器")
	}
	prompt := fmt.Sprintf(describePrompt, in.Owner, in.Repo, in.Platform, in.Number,
		in.SourceBranch, in.TargetBranch, in.Author, in.Title, orNone(in.Body),
		orNone(in.ChangeSummary), truncateDiff(in.DiffText))
	msgs := []*schema.Message{schema.UserMessage(prompt)}
	if err := s.LLM.GenerateJSON(ctx, "describe", msgs, &out); err != nil {
		return out, fmt.Errorf("describe 生成失败: %w", err)
	}
	out.Title = strings.TrimSpace(out.Title)
	out.Body = strings.TrimSpace(out.Body)
	if out.Title == "" || out.Body == "" {
		return out, fmt.Errorf("describe 产物不完整（title/body 存空）")
	}
	return out, nil
}

// Changelog 生成 changelog 条目（Markdown 列表）。
func (s *Service) Changelog(ctx context.Context, in Input) (string, error) {
	if s.LLM == nil {
		return "", fmt.Errorf("写作服务未接 LLM 生成器")
	}
	prompt := fmt.Sprintf(changelogPrompt, in.Owner, in.Repo, in.Platform, in.Number,
		in.Author, in.Title, orNone(in.Body), orNone(in.ChangeSummary), truncateDiff(in.DiffText))
	msgs := []*schema.Message{schema.UserMessage(prompt)}
	msg, err := s.LLM.Generate(ctx, "update_changelog", msgs)
	if err != nil {
		return "", fmt.Errorf("changelog 生成失败: %w", err)
	}
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return "", fmt.Errorf("changelog 产物为空")
	}
	return text, nil
}

// ReviewDiff 本地 diff 审查（v3.123.0 自 service 下沉收口）：单轮 LLM 审查
// 任意 diff 文本，返回 markdown 意见列表。MCP review_diff（同步）与
// diff_preview 任务（异步，runner 经同方法执行）共用本执行体——prompt 单源。
// 不建任务、不落库、不回帖是调用方的语义；本方法只管"diff → 意见文本"。
func (s *Service) ReviewDiff(ctx context.Context, diff string) (string, error) {
	if s.LLM == nil {
		return "", fmt.Errorf("模型未配置（ARGUS_LLM_*），本地 diff 审查不可用")
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("diff 为空")
	}
	// prompt 行格式是 v3.124.0 CLI 退出码契约的解析依据（cmd/argus-copilot
	// findingLineRe 同源手写）：`- file:line [severity] 说明`，severity 钉死
	// domain 词表三档——此前未钉词表，模型自由发挥会让机器可读面失配。
	prompt := "你是资深代码审查专家。审查以下 diff（提交前的本地预审），只报告有实际影响的" +
		"问题（安全/逻辑/边界），宁缺毋滥；每条意见一行，格式固定为" +
		"「- 文件路径:行号 [high|medium|low] 说明」（severity 只能取 high/medium/low 三档）。" +
		"无问题时只输出「✅ 未发现问题」。\n\n<diff>\n" + diff + "\n</diff>"
	msg, err := s.LLM.Generate(ctx, "diff_preview", []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		return "", fmt.Errorf("diff 审查失败: %w", err)
	}
	return strings.TrimSpace(msg.Content), nil
}

// describeBeginPrefix/describeEnd argus 管理段边界（HTML 注释对，平台渲染不可见）。
// begin 是前缀：Render 写入的完整注释为 "<!-- argus:describe begin task_id=... -->"，
// Merge 按前缀定位（读写两端契约单一出处，v3.75.3 修复此前精确串永不命中导致
// 管理段反复追加的漂移）；end 必须精确匹配，用于保住管理段之后的作者内容。
const (
	describeBeginPrefix = "<!-- argus:describe begin"
	describeEnd         = "<!-- argus:describe end -->"
)

// RenderDescription describe 产物 → 写回 PR 的正文段（管理段 marker 包裹）。
// task_id 放进注释内部（HTML 注释在平台页不可见；拼在 --> 外会漏成可见文本）。
func RenderDescription(d Description, taskID string) string {
	var b strings.Builder
	b.WriteString(describeBeginPrefix + " task_id=" + taskID + " -->\n\n")
	b.WriteString(d.Body + "\n\n" + describeEnd)
	return b.String()
}

// MergeDescription 把 argus 管理段合并进现有 PR 描述：已有管理段则只替换
// [begin, end] 区间（幂等更新），管理段前后的作者手写内容原样保留——绝不覆盖。
// 找不到 end marker（历史脏数据/人为截断）时保守跳过替换、只追加？不——按
// begin 截到文末（旧行为），旧段必然是 argus 写的，丢弃安全。
func MergeDescription(existing, section string) string {
	existing = strings.TrimSpace(existing)
	start := strings.Index(existing, describeBeginPrefix)
	if start < 0 {
		if existing == "" {
			return section
		}
		return existing + "\n\n---\n\n" + section
	}
	head := strings.TrimSpace(existing[:start])
	tail := ""
	if end := strings.Index(existing[start:], describeEnd); end >= 0 {
		tail = strings.TrimSpace(existing[start+end+len(describeEnd):])
	}
	// 拼装顺序：作者前段 → 管理段 → 作者尾段
	var parts []string
	sectionAndTail := section
	if tail != "" {
		sectionAndTail = section + "\n\n" + tail
	}
	if head != "" {
		parts = append(parts, head)
	}
	parts = append(parts, sectionAndTail)
	return strings.Join(parts, "\n\n---\n\n")
}

// truncateDiff 写作输入的 diff 截断（rune 安全：字节切分会把多字节 UTF-8
// 腰斩出非法序列，直进 LLM prompt 可能 400）。
func truncateDiff(diff string) string {
	if len(diff) > diffMaxChars {
		out, _ := textutil.TruncRunes(diff, diffMaxChars/3) // rune≈3 字节，换算后与旧字节预算同量级
		return out + "\n…（diff 截断，超出部分不影响写作语义）"
	}
	return diff
}

// orNone 空串占位。
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// CIInput CI 失败归因的输入。
type CIInput struct {
	Platform string
	Owner    string
	Repo     string
	Number   string // 关联 PR（可为空：无 MR 的 push pipeline）
	Branch   string
	Commit   string
	Jobs     []CIJob
}

// CIJob 单个失败 job 的元数据与日志尾部。
type CIJob struct {
	Name      string
	Stage     string
	WebURL    string
	Trace     string
	Truncated bool
}

// ciAnalysisPrompt CI 失败归因写作指令：结构化产出（根因/归责/修复建议），
// 面向贴回 PR 让作者直接行动。
const ciAnalysisPrompt = `你是 CI 故障诊断工程师。一次流水线失败了，请基于失败日志给出归因分析。

## 失败信息
- 仓库：%s/%s（%s），分支 %s，提交 %s
%s

## 要求
输出 Markdown，依次含：
1. 「## 结论」：一句话定性——是代码问题、环境/依赖问题、还是基础设施抖动；
2. 「## 根因」：引用日志中的关键报错行（代码块），指出失败的具体原因；
3. 「## 修复建议」：给出作者可执行的下一步（改哪里/重跑/联系谁）。

不确定时明确说不确定，禁止编造日志里没有的报错。中文。

只输出 JSON：{"conclusion": "...", "root_cause": "...", "fix": "..."}`

// CIAnalysis CI 失败归因产物。
type CIAnalysis struct {
	Conclusion string `json:"conclusion"`
	RootCause  string `json:"root_cause"`
	Fix        string `json:"fix"`
}

// AnalyzeCIFailure 归因失败 job 日志 → 结构化结论。
func (s *Service) AnalyzeCIFailure(ctx context.Context, in CIInput) (CIAnalysis, error) {
	var out CIAnalysis
	if s.LLM == nil {
		return out, fmt.Errorf("写作服务未接 LLM 生成器")
	}
	if len(in.Jobs) == 0 {
		return out, fmt.Errorf("无失败 job 日志可归因")
	}
	var jobs strings.Builder
	for _, j := range in.Jobs {
		trunc := ""
		if j.Truncated {
			trunc = "（已截断，仅尾部）"
		}
		jobs.WriteString(fmt.Sprintf("- 失败 job：%s（stage=%s）%s\n<details><summary>日志尾部</summary>\n\n```\n%s\n```\n</details>\n\n",
			j.Name, j.Stage, trunc, j.Trace))
	}
	pr := ""
	if in.Number != "" {
		pr = "关联 PR：#" + in.Number
	}
	prompt := fmt.Sprintf(ciAnalysisPrompt, in.Owner, in.Repo, in.Platform, in.Branch, shortSHA(in.Commit), pr+"\n\n"+jobs.String())
	msgs := []*schema.Message{schema.UserMessage(prompt)}
	if err := s.LLM.GenerateJSON(ctx, "ci_analysis", msgs, &out); err != nil {
		return out, fmt.Errorf("ci_analysis 生成失败: %w", err)
	}
	if strings.TrimSpace(out.Conclusion) == "" {
		return out, fmt.Errorf("ci_analysis 产物不完整（conclusion 为空）")
	}
	return out, nil
}

// RenderCIAnalysis 归因产物 → PR 评论正文。
func RenderCIAnalysis(a CIAnalysis, taskID string) string {
	var b strings.Builder
	b.WriteString("## 🔴 CI 失败归因（by argus）\n\n")
	b.WriteString("**结论**：" + a.Conclusion + "\n\n")
	if a.RootCause != "" {
		b.WriteString("**根因**：" + a.RootCause + "\n\n")
	}
	if a.Fix != "" {
		b.WriteString("**修复建议**：" + a.Fix + "\n\n")
	}
	b.WriteString("<!-- argus:ci_analysis task_id=" + taskID + " -->")
	return b.String()
}

// shortSHA 提交短 SHA 展示。
func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
