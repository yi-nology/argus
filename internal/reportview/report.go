// Package reportview 回帖报告模板渲染（design §11 实现契约）。
// 独立成包以便单测覆盖渲染；纯函数、无 IO。
package reportview

import (
	"fmt"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/safejson"

	"argus/internal/plugin"
)

// ReportHeaderPrefix 报告评论 HTML 注释头前缀（人不可见，机器定位用：§10.1 审计可追溯 +
// review_targets 记录损坏时的扫描兜底）。
const ReportHeaderPrefix = "<!-- argus:report "

// ReportHeaderHTML 生成某任务的报告头。
func ReportHeaderHTML(taskID, headSHA, version string) string {
	return fmt.Sprintf("<!-- argus:report task_id=%s head=%s ver=%s -->", taskID, headSHA, version)
}

// EscapeUntrusted 委托给 agentkit/safejson.EscapeUntrusted。
func EscapeUntrusted(s string) string {
	return safejson.EscapeUntrusted(s)
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
}

// Render 渲染整份 markdown 报告（含 HTML 注释头）。
func Render(r Report) string {
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 📋 Argus 需求复核报告")

	if r.ShowCompliance {
		src := r.ReqSource
		if src == "" {
			src = "未关联"
		}
		fmt.Fprintf(&b, "（需求源: %s）\n", EscapeUntrusted(src))

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
			fmt.Fprintf(&b, "\n\n### 验收清单完成度: %d/%d %s\n", done, total, icon)
			for _, ac := range r.ACs {
				detail := ac.Evid
				if detail == "" {
					detail = "—"
				}
				fmt.Fprintf(&b, "- %s **%s** %s — %s（%s）\n", ac.Icon(), ac.ID,
					EscapeUntrusted(ac.Text), ac.Status, EscapeUntrusted(detail))
			}
		}

		if len(r.ScopeCreep) > 0 {
			b.WriteString("\n### ⚠️ 需求外改动（scope creep）\n")
			for _, sc := range r.ScopeCreep {
				fmt.Fprintf(&b, "- %s: %s\n", EscapeUntrusted(sc.File), EscapeUntrusted(sc.Note))
			}
		}
	}

	if len(r.Findings) > 0 {
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
			// 多行 comment 以两空格缩进续行，保持 markdown 列表结构
			comment := strings.ReplaceAll(strings.TrimSpace(EscapeUntrusted(f.Comment)), "\n", "\n  ")
			if ann := plugin.SamplesAnnotation(f); ann != "" {
				comment += ann // best-of-N 复现计数（结构化字段，指纹不受影响）
			}
			fmt.Fprintf(&b, "- `%s:%d` **[%s]** %s — by %s（fp:%s）\n",
				EscapeUntrusted(f.File), f.Line, strings.ToUpper(f.Severity), comment,
				EscapeUntrusted(by), fp)
			if f.SuggestedFix != "" {
				fix := EscapeUntrusted(f.SuggestedFix)
				fmt.Fprintf(&b, "  \n  **建议修复：**\n  ```\n  %s\n  ```\n", strings.ReplaceAll(fix, "\n", "\n  "))
			}
		}
	}

	if len(r.Degraded) > 0 {
		b.WriteString("\n<details><summary>插件降级说明</summary>\n\n")
		for _, d := range r.Degraded {
			fmt.Fprintf(&b, "- %s\n", EscapeUntrusted(d))
		}
		b.WriteString("\n</details>\n")
	}

	if len(r.Completeness) > 0 {
		b.WriteString("\n### ⚠️ 完备性检查（覆盖可能不完整）\n")
		for _, c := range r.Completeness {
			fmt.Fprintf(&b, "- %s\n", EscapeUntrusted(c))
		}
	}

	b.WriteString("\n### 结论: ")
	b.WriteString(r.VerdictText)
	if r.Rule != "" {
		fmt.Fprintf(&b, "（%s）", r.Rule)
	}
	b.WriteString("\n")

	if r.Meta {
		meta := fmt.Sprintf("task `%s` · 插件: %s · 耗时 %ds · tokens %d",
			r.TaskID, strings.Join(r.Plugins, ","), int(r.Elapsed.Seconds()), r.Tokens)
		if r.CostUSD > 0 {
			meta += fmt.Sprintf(" · 成本 ≈ $%.4f", r.CostUSD)
		}
		fmt.Fprintf(&b, "\n> %s\n", meta)
		if r.CrossReview != "" {
			fmt.Fprintf(&b, "> 🤝 交叉审查：%s\n", EscapeUntrusted(r.CrossReview))
		}
		if r.Suppressed > 0 {
			fmt.Fprintf(&b, "> 另有 %d 条历史意见已被标记为误报，本轮自动抑制。\n", r.Suppressed)
		}
	}
	return b.String()
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
