// Package reportview 回帖报告模板渲染（design §11 实现契约）。
// 独立成包以便单测覆盖渲染；纯函数、无 IO。
package reportview

import (
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/guardx/fence"

	"argus/internal/dispatch"
	"argus/internal/plugin"
)

// ReportHeaderPrefix 报告评论 HTML 注释头前缀（人不可见，机器定位用：§10.1 审计可追溯 +
// review_targets 记录损坏时的扫描兜底）。
const ReportHeaderPrefix = "<!-- argus:report "

// ReportHeaderHTML 生成某任务的报告头（前缀单一出处，改头格式只动常量）。
func ReportHeaderHTML(taskID, headSHA, version string) string {
	return ReportHeaderPrefix + fmt.Sprintf("task_id=%s head=%s ver=%s -->", taskID, headSHA, version)
}

// ACCheckView 单条验收清单渲染条目。
type ACCheckView struct {
	ID     string
	Text   string
	Status string // implemented|partial|missing|unverifiable
	Evid   string // 证据 file:line 或差距说明
}

// Icon 状态图标。
func (v ACCheckView) Icon() string {
	switch v.Status {
	case plugin.ACImplemented:
		return "✅"
	case plugin.ACPartial:
		return "⚠️"
	case plugin.ACMissing:
		return "❌"
	default:
		return "❓" // unverifiable：需人工确认
	}
}

// ScopeView 需求外改动条目。
type ScopeView struct {
	File string
	Note string
}

// Report 报告输入（由 workflow/poster 组装，渲染函数不做业务判断）。
type Report struct {
	TaskID    string
	HeadSHA   string
	Version   string
	ReqSource string // "issue #12" | "PR 描述" | ""（空渲染"未关联需求"）

	// 需求复核段：ACs 为空且 ShowCompliance=true 时渲染"无验收清单"分支（§11 N=0 分支）；
	// false = code_review-only 任务，整段省略。
	ShowCompliance bool
	ACs            []ACCheckView
	ScopeCreep     []ScopeView

	Findings []plugin.Finding

	// ImpactSection 影响面段（v3.183.0）：runner 从影响面快照预渲染
	//（impactview.RenderReportSection——details 折叠 + mermaid/列表），空 = 整段
	// 省略。预渲染字符串而非结构体，reportview 保持纯模板零新依赖。
	ImpactSection string

	// Completeness 完备性失败项（G4 v3.11.0；nil = 全部通过或未启用）。
	// 独立段而非 Degraded：完备性是覆盖事实（审得全不全），与插件降级说明分开展示。
	Completeness []string

	// 结论段
	VerdictText string // 已映射好图标与文案（⛔/⚠️/💬/✅ …），渲染层不再判断
	Rule        string // 命中的 R5 规则（R5.1..R5.5，可空）

	// 元信息行（ARGUS_REPORT_META 控制，默认开）
	Meta        bool
	Plugins     []string
	Elapsed     time.Duration
	Tokens      int
	CostUSD     float64  // 任务级估算成本（ARGUS_LLM_COST_* 配置费率；0 = 不渲染）
	CrossReview string   // 交叉审查编队注记（空 = 无编队参与）
	Degraded    []string // 降级注明（插件未参与原因）
	Suppressed  int      // 被误报抑制的 finding 数（脚注）
	// MaxInlineComments 行内评论上限（v3.15.0 .reviewagents.yaml review.max_comments；
	// 0 = 不限）。钳制发生在 poster，超限注明进 Degraded。
	MaxInlineComments int
	// FixHint 在 finding 行附「@argus fix fp:」修复入口提示（v3.85.0；服务级
	// ARGUS_FIX_AGENT 启用时由 runner 置 true）。
	FixHint bool
}

// Render 渲染整份 markdown 报告（含 HTML 注释头）。
func Render(r Report) string {
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 📋 Argus 需求复核报告")

	if r.ShowCompliance {
		renderCompliance(&b, r)
	}

	if len(r.Findings) > 0 {
		renderFindings(&b, r)
	}

	if r.ImpactSection != "" {
		b.WriteString(r.ImpactSection)
	}

	if len(r.Degraded) > 0 {
		b.WriteString("\n<details><summary>插件降级说明</summary>\n\n")
		for _, d := range r.Degraded {
			fmt.Fprintf(&b, "- %s\n", fence.EscapeUntrusted(d))
		}
		b.WriteString("\n</details>\n")
	}

	if len(r.Completeness) > 0 {
		b.WriteString("\n### ⚠️ 完备性检查（覆盖可能不完整）\n")
		for _, c := range r.Completeness {
			fmt.Fprintf(&b, "- %s\n", fence.EscapeUntrusted(c))
		}
	}

	b.WriteString("\n### 结论: ")
	b.WriteString(r.VerdictText)
	if r.Rule != "" {
		fmt.Fprintf(&b, "（%s）", r.Rule)
	}
	b.WriteString("\n")

	if r.Meta {
		renderMeta(&b, r)
	}
	return b.String()
}

// renderCompliance 验收清单完成度节（含 scope creep）。
func renderCompliance(b *strings.Builder, r Report) {
	src := r.ReqSource
	if src == "" {
		src = "未关联"
	}
	fmt.Fprintf(b, "（需求源: %s）\n", fence.EscapeUntrusted(src))

	done, total := complianceProgress(r.ACs)
	switch {
	case total == 0:
		b.WriteString("\n\n### 验收清单完成度\n\n**无验收清单（需求模糊或未关联，需人工确认）**\n")
	default:
		icon := "✅"
		if done < total {
			icon = "⚠️"
			for _, ac := range r.ACs {
				if ac.Status == plugin.ACMissing || ac.Status == plugin.ACPartial {
					icon = "⛔"
					break
				}
			}
		}
		fmt.Fprintf(b, "\n\n### 验收清单完成度: %d/%d %s\n", done, total, icon)
		for _, ac := range r.ACs {
			detail := ac.Evid
			if detail == "" {
				detail = "—"
			}
			fmt.Fprintf(b, "- %s **%s** %s — %s（%s）\n", ac.Icon(), ac.ID,
				fence.EscapeUntrusted(ac.Text), ac.Status, fence.EscapeUntrusted(detail))
		}
	}

	if len(r.ScopeCreep) > 0 {
		b.WriteString("\n### ⚠️ 需求外改动（scope creep）\n")
		for _, sc := range r.ScopeCreep {
			fmt.Fprintf(b, "- %s: %s\n", fence.EscapeUntrusted(sc.File), fence.EscapeUntrusted(sc.Note))
		}
	}
}

// renderFindings 代码审查意见节（指纹短码 + 多行 comment 缩进续行 + 建议修复块）。
func renderFindings(b *strings.Builder, r Report) {
	b.WriteString("\n### 🔍 代码审查意见\n")
	for _, f := range r.Findings {
		by := f.By
		if by == "" {
			by = "argus"
		}
		// 指纹短码：/findings/suppress 可按 fp 直达（反馈闭环，§3.3）。
		// 必须用 merger 写定的入库指纹 f.FP（prints 表身份）；终稿文本已被
		// 复现/R3.5 标注改写，重算必漂移——漂移短码让"最需要抑制的条目"
		// 恰好无法抑制（v3.8.0 修复）。零值回退仅兜底未经 merger 的测试桩。
		fp := f.FP
		if fp == "" {
			fp = plugin.Fingerprint(f.File, f.Comment, f.Line)
		}
		fp = fp[:8]
		// 修复入口（v3.85.0）：服务启用 fixer 时每条意见附一键修复命令
		fpnote := fmt.Sprintf("（fp:%s）", fp)
		if r.FixHint {
			fpnote = fmt.Sprintf("（fp:%s，修复：`@argus fix fp:%s`）", fp, fp)
		}
		// 多行 comment 以两空格缩进续行，保持 markdown 列表结构
		comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
		if ann := dispatch.SamplesAnnotation(f); ann != "" {
			comment += ann // best-of-N 复现计数（结构化字段，指纹不受影响）
		}
		if ann := dispatch.ConfidenceAnnotation(f); ann != "" {
			comment += ann
		}
		if f.Verified {
			comment += "（✅ 沙箱验证通过）"
		}
		fmt.Fprintf(b, "- `%s:%d` **[%s]** %s — by %s%s\n",
			fence.EscapeUntrusted(f.File), f.Line, strings.ToUpper(f.Severity), comment,
			fence.EscapeUntrusted(by), fpnote)
		if f.SuggestedFix != "" {
			// v3.91.2 围栏安全渲染：裸内容 + 按内容中最长反引号串动态加长围栏。
			// 此前经 EscapeUntrusted 再进围栏——零宽实体（&#8203;）在围栏内
			// 原样可见、反引号被 ′ 替换污染复制内容；围栏内本无 markdown 语义，
			// 正文流转义在这里帮倒忙（与 poster 行内 suggestion 的裸内容纪律对齐）。
			n := codeFenceLen(f.SuggestedFix)
			fenceStr := strings.Repeat("`", n)
			fix := strings.ReplaceAll(f.SuggestedFix, "\n", "\n  ")
			fmt.Fprintf(b, "  \n  **建议修复：**\n  %s\n  %s\n  %s\n", fenceStr, fix, fenceStr)
		}
	}
}

// renderMeta 元信息尾注节（耗时/成本/交叉审查/抑制计数）。
func renderMeta(b *strings.Builder, r Report) {
	meta := fmt.Sprintf("task `%s` · 插件: %s · 耗时 %ds · tokens %d",
		r.TaskID, strings.Join(r.Plugins, ","), int(r.Elapsed.Seconds()), r.Tokens)
	if r.CostUSD > 0 {
		meta += fmt.Sprintf(" · 成本 ≈ $%.4f", r.CostUSD)
	}
	fmt.Fprintf(b, "\n> %s\n", meta)
	if r.CrossReview != "" {
		fmt.Fprintf(b, "> 🤝 交叉审查：%s\n", fence.EscapeUntrusted(r.CrossReview))
	}
	if r.Suppressed > 0 {
		fmt.Fprintf(b, "> 另有 %d 条历史意见已被标记为误报，本轮自动抑制。\n", r.Suppressed)
	}
}

// complianceProgress 统计达成数（implemented 计为达成；partial/missing/unverifiable 不计）。
func complianceProgress(acs []ACCheckView) (done, total int) {
	total = len(acs)
	for _, ac := range acs {
		if ac.Status == plugin.ACImplemented {
			done++
		}
	}
	return done, total
}

// VerdictText 把 R5 结论映射为模板结论文案（workflow 调用，保持单一出处）。
// 词表必须与 workflow 的 rule 赋值一一对应——R5.0（v3.8.0 修复）：此前无该分支，
// 守门降级 COMMENT 后 default 落"✅ 通过"，人读文案与真值表直接矛盾。
func VerdictText(rule string, missingPartial int) string {
	switch {
	case rule == "R5.0":
		return "⚠️ 未能完成审查（无插件实际产出），本结论不可作为合并依据"
	case rule == "R5.1":
		return fmt.Sprintf("⛔ 需求未完成（%d 项未达标）", missingPartial)
	case rule == "R5.2":
		return "⛔ 存在高危问题，请修复后再提交"
	case rule == "R5.3":
		return "⚠️ 需人工确认"
	case rule == "R5.4":
		return "💬 有审查意见，请参考"
	default:
		return "✅ 通过"
	}
}

// codeFenceLen 围栏安全长度（v3.91.2）：内容中最长反引号串 +1（下限 3）——
// CommonMark 下内容再含 ``` 也不会提前闭合围栏。
func codeFenceLen(s string) int {
	n, cur := 3, 0
	for _, r := range s {
		if r == '`' {
			cur++
			if cur >= n {
				n = cur + 1
			}
		} else {
			cur = 0
		}
	}
	return n
}
