// Package reportview 回帖报告模板渲染（design §11 实现契约）。
// 独立成包以便单测覆盖渲染；纯函数、无 IO。
package reportview

import (
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/guardx/fence"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
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
	// Template 报告契约模板键（v6.10.0：standard|analyst|minimal，词单源
	// reporttmpl——runner 经 reporttmpl.Resolve 裁决后写入）。空/未知值回退
	// standard 渲染（防组装失误炸报告面）。
	Template string

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

	// Analyst 分析师综合段产物（v6.10.0；仅 Template=analyst 时由 runner 从
	// out.Analyst 注入）。各段 nil/空 = 渲染省略（N=0 分支纪律）——综合段失败
	// 降级时 Analyst 为 nil，退化为 standard 骨架。
	Analyst *plugin.AnalystSections

	// ScaleFiles/ScaleAdditions/ScaleDeletions 变更规模统计（v6.11.0；runner
	// 从 DiffContext 汇总，analyst 报告头「N 文件 +A/−B」形态）。ScaleFiles=0
	// 时规模行整行省略（无 diff 的写作/问答任务不渲染空统计）。
	ScaleFiles     int
	ScaleAdditions int
	ScaleDeletions int
	// CIStatus head SHA 合并 CI 状态（v6.11.0；统一词表 pending|running|success|
	// failure|error|canceled，空 = 未取到——meta 行省略该段，不显式报"无"）。
	CIStatus string
}

// Render 渲染整份 markdown 报告（含 HTML 注释头）。按 Template 分发：
// standard = 现状清单式报告（缺省，零漂移）；analyst = 分析师深度报告（例文
// 骨架：声明对照表→风险链→坏味→结构维度→测试缺口/待确认）；minimal = 结论+
// 高危极简。未知/空模板回退 standard（组装失误不炸报告面）。
func Render(r Report) string {
	switch r.Template {
	case reporttmpl.Analyst:
		return renderAnalyst(r)
	case reporttmpl.Minimal:
		return renderMinimal(r)
	default:
		return renderStandard(r)
	}
}

// renderStandard 标准模板（v6.10.0 前唯一形态，行为逐字节保留）。
func renderStandard(r Report) string {
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
		// v5.7.1 运行中断 banner：全灭/大面积中断时收敛为顶部一段说明，
		// AC 行只留短后缀（jianmu MR47 实弹：24-30 行同款长文案刷屏）。
		rtFail := 0
		for _, ac := range r.ACs {
			if plugin.IsRuntimeFailDetail(ac.Evid) {
				rtFail++
			}
		}
		if rtFail > 0 {
			fmt.Fprintf(b, "\n> ⚠️ **本轮 %d/%d 条判定被运行中断**（限流/超时类瞬时故障）：判定作废、非需求不可验证；下方逐条仅留短注，建议错峰重跑任务。\n",
				rtFail, total)
		}
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
		b.WriteString(evidenceLine(f, "  "))
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

// evidenceLine Finding.Evidence 证据行（v6.18.0）：非空返回 indent 前缀的
// 「📎 证据：」续行（⚠️ 校验标注随之透出），空返回 ""——标准意见列表/风险链/
// 坏味组三面共用。转义与 comment 同纪律（敌对输入），换行折叠保列表结构。
// 渲染属性不入指纹，本行只读 Finding.Evidence。
func evidenceLine(f plugin.Finding, indent string) string {
	ev := strings.TrimSpace(fence.EscapeUntrusted(f.Evidence))
	if ev == "" {
		return ""
	}
	ev = strings.ReplaceAll(ev, "\n", " ")
	return indent + "📎 证据：" + ev + "\n"
}

// renderMeta 元信息尾注节（耗时/成本/交叉审查/抑制计数/CI 状态）。
func renderMeta(b *strings.Builder, r Report) {
	meta := fmt.Sprintf("task `%s` · 插件: %s · 耗时 %ds · tokens %d",
		r.TaskID, strings.Join(r.Plugins, ","), int(r.Elapsed.Seconds()), r.Tokens)
	if r.CostUSD > 0 {
		meta += fmt.Sprintf(" · 成本 ≈ $%.4f", r.CostUSD)
	}
	if ci := ciStatusLabel(r.CIStatus); ci != "" {
		meta += " · " + ci
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

// renderAnalyst 分析师深度报告（spec §4.6，例文骨架）。段落 N=0 一律省略；
// Analyst=nil（综合段降级）时退化为 standard 骨架（claims/design 等段缺席）。
func renderAnalyst(r Report) string {
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 🔬 Argus 深度审查报告")

	// 一段话总结：结论 + 变更规模（复用 VerdictText，R5 裁决口径不动）。
	fmt.Fprintf(&b, "\n\n**结论：%s。**", r.VerdictText)
	if scale := analystScaleLine(r); scale != "" {
		fmt.Fprintf(&b, " %s。", scale)
	}
	b.WriteString("\n")

	if r.Analyst != nil {
		renderClaimsTable(&b, r.Analyst.Claims)
	}

	// 风险链（high/critical）与代码坏味（medium/low）分组渲染，组内保持
	// severity 降序（merger 已排序，分组即切片）。
	risks, smells := splitFindings(r.Findings)
	renderRiskChain(&b, r, risks)
	renderSmells(&b, r, smells)

	if r.Analyst != nil {
		renderDesignSection(&b, r.Analyst.Design)
		renderGapsAndQuestions(&b, r.Analyst.TestGaps, r.Analyst.OpenQuestions)
	}

	// issue 关联场景的确定性验收清单与 scope creep 保留（与 claims 表并存：
	// 清单是确定性判定、claims 是模型对 MR 描述的取证）。
	if r.ShowCompliance {
		renderCompliance(&b, r)
	}
	if r.ImpactSection != "" {
		b.WriteString(r.ImpactSection)
	}
	renderDegradedAndCompleteness(&b, r)

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

// renderMinimal 极简模板：结论先行 + 仅 high/critical findings（封顶
// minimalMaxFindings，超出注计数）+ 一行 meta。给"别吵我"的仓。
func renderMinimal(r Report) string {
	const minimalMaxFindings = 10
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 📋 Argus 审查结论\n\n**")
	b.WriteString(r.VerdictText)
	b.WriteString("**\n")

	var highs []plugin.Finding
	for _, f := range r.Findings {
		if f.Severity == "high" || f.Severity == "critical" {
			highs = append(highs, f)
		}
	}
	if len(highs) > 0 {
		b.WriteString("\n### 高危意见\n")
		n := min(len(highs), minimalMaxFindings)
		for _, f := range highs[:n] {
			comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
			fmt.Fprintf(&b, "- `%s:%d` **[%s]** %s\n",
				fence.EscapeUntrusted(f.File), f.Line, strings.ToUpper(f.Severity), comment)
		}
		if len(highs) > minimalMaxFindings {
			fmt.Fprintf(&b, "\n> 另有 %d 条高危/严重意见从略（极简模板封顶 %d 条，详见行内评论）。\n",
				len(highs)-minimalMaxFindings, minimalMaxFindings)
		}
	}
	if r.Meta {
		fmt.Fprintf(&b, "\n> task `%s` · 耗时 %ds · tokens %d", r.TaskID, int(r.Elapsed.Seconds()), r.Tokens)
		if ci := ciStatusLabel(r.CIStatus); ci != "" {
			fmt.Fprintf(&b, " · %s", ci)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// analystScaleLine 规模统计（例文「7 文件 +519/−579」形态；v6.11.0 接入真实
// diff 统计——v6.10.0 留空实现已兑现）。无 diff 数据（ScaleFiles=0）整行省略。
func analystScaleLine(r Report) string {
	if r.ScaleFiles == 0 {
		return ""
	}
	line := fmt.Sprintf("规模：%d 文件", r.ScaleFiles)
	if r.ScaleAdditions > 0 || r.ScaleDeletions > 0 {
		line += fmt.Sprintf("（+%d/−%d）", r.ScaleAdditions, r.ScaleDeletions)
	}
	return line
}

// ciStatusLabel 统一 CI 状态 → 展示短标签（图标 + 中文；空输入返回空串，调用方省略）。
func ciStatusLabel(s string) string {
	switch s {
	case "success":
		return "✅ CI 通过"
	case "failure", "error":
		return "❌ CI 失败"
	case "canceled":
		return "⏹ CI 已取消"
	case "pending", "running":
		return "⏳ CI 进行中"
	default:
		return ""
	}
}

// claimIcon 声明判定五态图标（词表 plugin.ClaimStatus，spec §4.2）。
func claimIcon(status string) string {
	switch status {
	case plugin.ClaimImplemented:
		return "✅"
	case plugin.ClaimPartial:
		return "⚠️"
	case plugin.ClaimMissing:
		return "❌"
	case plugin.ClaimUndeclared:
		return "➕"
	case plugin.ClaimInconsistent:
		return "❗"
	default:
		return "❓"
	}
}

// claimStatusLabel 判定中文标签（表格「判定」列）。
func claimStatusLabel(status string) string {
	switch status {
	case plugin.ClaimImplemented:
		return "实现"
	case plugin.ClaimPartial:
		return "部分"
	case plugin.ClaimMissing:
		return "未实现"
	case plugin.ClaimUndeclared:
		return "超出声明"
	case plugin.ClaimInconsistent:
		return "口径不一致"
	default:
		return status
	}
}

// renderClaimsTable 声明对照表（| # | 声明项 | 代码证据 | 判定 |，例文一节形态）。
// 空表整段省略。
func renderClaimsTable(b *strings.Builder, claims []plugin.Claim) {
	if len(claims) == 0 {
		return
	}
	b.WriteString("\n\n### 一、需求符合性（声明 vs 代码）\n\n")
	b.WriteString("| # | 声明项 | 代码证据 | 判定 |\n|---|---|---|---|\n")
	for i, c := range claims {
		claim := strings.ReplaceAll(fence.EscapeUntrusted(c.Claim), "\n", " ")
		evid := strings.ReplaceAll(fence.EscapeUntrusted(c.Evidence), "\n", " ")
		if evid == "" {
			evid = "—"
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s %s |\n",
			i+1, claim, evid, claimIcon(c.Status), claimStatusLabel(c.Status))
	}
}

// splitFindings 按严重度切风险链/坏味两组（high/critical = 风险链主体；
// 其余 = 坏味组，编号渲染）。merger 已降序，切片保持序。
func splitFindings(findings []plugin.Finding) (risks, smells []plugin.Finding) {
	for _, f := range findings {
		if f.Severity == "high" || f.Severity == "critical" {
			risks = append(risks, f)
		} else {
			smells = append(smells, f)
		}
	}
	return risks, smells
}

// renderRiskChain 风险链（🔴🟠 分级，chain 证据链缩进子段 + 建议修复块）。
func renderRiskChain(b *strings.Builder, r Report, risks []plugin.Finding) {
	if len(risks) == 0 {
		return
	}
	b.WriteString("\n### 二、风险\n")
	for i, f := range risks {
		by := f.By
		if by == "" {
			by = "argus"
		}
		icon := "🟠"
		if f.Severity == "critical" {
			icon = "🔴"
		}
		comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
		fmt.Fprintf(b, "\n#### %s R%d（%s）`%s:%d`\n\n%s — by %s\n",
			icon, i+1, strings.ToUpper(f.Severity),
			fence.EscapeUntrusted(f.File), f.Line, comment, fence.EscapeUntrusted(by))
		b.WriteString(evidenceLine(f, "> "))
		if chain := strings.TrimSpace(fence.EscapeUntrusted(f.Chain)); chain != "" {
			chain = strings.ReplaceAll(chain, "\n", "\n> ")
			fmt.Fprintf(b, "\n> **证据链**：%s\n", chain)
		}
		if f.SuggestedFix != "" {
			n := codeFenceLen(f.SuggestedFix)
			fenceStr := strings.Repeat("`", n)
			fix := strings.ReplaceAll(f.SuggestedFix, "\n", "\n  ")
			fmt.Fprintf(b, "\n**建议修复：**\n\n%s\n%s\n%s\n", fenceStr, fix, fenceStr)
		}
	}
}

// renderSmells 代码坏味（🟡🟢 组，编号渲染，例文「三、代码坏味」形态）。
func renderSmells(b *strings.Builder, r Report, smells []plugin.Finding) {
	if len(smells) == 0 {
		return
	}
	b.WriteString("\n### 三、代码坏味\n")
	for i, f := range smells {
		comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
		fmt.Fprintf(b, "\n%d. **`%s:%d`** — %s\n",
			i+1, fence.EscapeUntrusted(f.File), f.Line, comment)
		b.WriteString(evidenceLine(f, "   "))
		if f.SuggestedFix != "" {
			n := codeFenceLen(f.SuggestedFix)
			fenceStr := strings.Repeat("`", n)
			fix := strings.ReplaceAll(f.SuggestedFix, "\n", "\n   ")
			fmt.Fprintf(b, "   %s\n   %s\n   %s\n", fenceStr, fix, fenceStr)
		}
	}
}

// renderDesignSection 结构维度（design.Triggered 才渲染；对象图/内聚/耦合/迁移）。
func renderDesignSection(b *strings.Builder, d *plugin.Design) {
	if d == nil || !d.Triggered {
		return
	}
	b.WriteString("\n### 四、结构维度（对象生命周期 × 内聚/耦合）\n")
	writeBulletGroup := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(b, "\n**%s**\n", title)
		for _, it := range items {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(it))
		}
	}
	writeBulletGroup("对象图（创建者→持有者→写者→读者→生命周期）", d.ObjectGraph)
	writeBulletGroup("内聚", d.Cohesion)
	writeBulletGroup("耦合", d.Coupling)
	if m := strings.TrimSpace(d.Migration); m != "" {
		fmt.Fprintf(b, "\n**迁移建议**\n\n%s\n", fence.EscapeUntrusted(m))
	}
}

// renderGapsAndQuestions 测试缺口与待确认清单（例文「四、测试与待确认」形态）。
func renderGapsAndQuestions(b *strings.Builder, gaps, questions []string) {
	if len(gaps) == 0 && len(questions) == 0 {
		return
	}
	if len(gaps) > 0 {
		b.WriteString("\n### 五、测试缺口\n")
		for _, g := range gaps {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(g))
		}
	}
	if len(questions) > 0 {
		b.WriteString("\n### 六、待确认\n")
		for _, q := range questions {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(q))
		}
	}
}

// renderDegradedAndCompleteness 降级说明 + 完备性段（analyst/standard 共用尾部）。
func renderDegradedAndCompleteness(b *strings.Builder, r Report) {
	if len(r.Degraded) > 0 {
		b.WriteString("\n<details><summary>插件降级说明</summary>\n\n")
		for _, d := range r.Degraded {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(d))
		}
		b.WriteString("\n</details>\n")
	}
	if len(r.Completeness) > 0 {
		b.WriteString("\n### ⚠️ 完备性检查（覆盖可能不完整）\n")
		for _, c := range r.Completeness {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(c))
		}
	}
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
