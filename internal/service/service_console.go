// service_console.go —— 质量控制台（#27，v7.1 生产运营）：GET /console
// 自包含 HTML 页。数据全部来自既有聚合面（StatsOverview/UsageMonthly/
// obs.ConsoleMetrics），零新表——验收线「数据全部来自既有 API 聚合」。
// 行过滤：stats/usage 复用 AuthorizeRowFilter（约束 token 见约束内行）；
// Registry 计数是进程级全局量（无仓维度），与 /metrics 同口径透出。

package service

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/storage"
)

// QualityConsoleHTML 质量控制台页（text/html 直写，同 TaskImpact 形态）。
// 聚合四面：审查概览（误报抑制/verdict 分布）/ 月度 Token 消费 / 失败原因
// 归类（Registry 计数）/ 修复闭环有效率（fix_verify 分母 × fix_push 分子）。
func (s *Service) QualityConsoleHTML(ctx context.Context) (string, int) {
	stats, st := s.StatsOverview(ctx, "", "", "", 30)
	if st != http.StatusOK {
		return "", st
	}
	usage, _ := s.UsageMonthly(ctx) // 失败降级为空块（控制台不因用量面故障 500）
	metrics := obs.ConsoleMetrics()

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Argus 质量控制台</title><style>
:root{color-scheme:light dark;--bg:#0d1117;--card:#161b22;--fg:#e6edf3;--dim:#8b949e;--line:#30363d;--acc:#58a6ff;--ok:#3fb950;--bad:#f85149;--warn:#d29922}
@media(prefers-color-scheme:light){:root{--bg:#f6f8fa;--card:#fff;--fg:#1f2328;--dim:#656d76;--line:#d0d7de;--acc:#0969da;--ok:#1a7f37;--bad:#cf222e;--warn:#9a6700}}
*{box-sizing:border-box}body{margin:0;padding:24px;background:var(--bg);color:var(--fg);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
h1{font-size:20px;margin:0 0 4px}h2{font-size:15px;margin:24px 0 8px;color:var(--acc)}
.sub{color:var(--dim);font-size:12px;margin-bottom:20px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:12px}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:14px 16px}
.card .k{color:var(--dim);font-size:12px}.card .v{font-size:24px;font-weight:600;margin-top:2px}
.card .v.ok{color:var(--ok)}.card .v.bad{color:var(--bad)}.card .v.warn{color:var(--warn)}
table{border-collapse:collapse;width:100%;background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden}
th,td{padding:7px 12px;text-align:left;border-bottom:1px solid var(--line);font-size:13px}
th{color:var(--dim);font-weight:600;background:transparent}tr:last-child td{border-bottom:none}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums}
.bar{display:inline-block;height:8px;border-radius:4px;background:var(--acc);vertical-align:middle}
.muted{color:var(--dim)}.tag{display:inline-block;padding:1px 8px;border-radius:10px;font-size:11px;border:1px solid var(--line)}
a{color:var(--acc)}
</style></head><body>`)
	b.WriteString("<h1>📊 Argus 质量控制台</h1><div class=\"sub\">")
	fmt.Fprintf(&b, "窗口 %v 天 · 生成于 %s · 数据来自 /stats /usage /metrics 既有聚合面（零新表）",
		stats["window_days"], time.Now().Format("2006-01-02 15:04:05"))
	b.WriteString("</div>")

	renderConsoleOverview(&b, stats)
	renderConsoleUsage(&b, usage)
	renderConsoleFailures(&b, metrics)
	renderConsoleFixLoop(&b, metrics)
	renderConsoleRepos(&b, stats)

	b.WriteString("</body></html>")
	return b.String(), http.StatusOK
}

// renderConsoleOverview 概览卡片：任务量 / verdict 分布 / 抑制计数（误报反馈面）。
func renderConsoleOverview(b *strings.Builder, stats map[string]any) {
	b.WriteString("<h2>审查概览（30 天）</h2><div class=\"grid\">")
	card(b, "已完成任务", fmt.Sprintf("%v", stats["tasks_done"]), "")
	v, _ := stats["verdicts"].(map[string]int64)
	total := v["approve"] + v["comment"] + v["request_changes"]
	if total == 0 {
		total = 1
	}
	card(b, "通过 / 意见 / 打回",
		fmt.Sprintf("%d / %d / %d", v["approve"], v["comment"], v["request_changes"]), "")
	card(b, "意见占比（comment）",
		fmt.Sprintf("%.1f%%", float64(v["comment"])/float64(total)*100), "warn")
	card(b, "误报抑制（累计）", fmt.Sprintf("%v", stats["findings_suppressed"]), "ok")
	card(b, "覆盖仓库", fmt.Sprintf("%v", stats["repo_count"]), "")
	b.WriteString("</div>")
}

// renderConsoleUsage 月度 Token 消费（三口径：配额/实销/预留）。
func renderConsoleUsage(b *strings.Builder, usage map[string]any) {
	b.WriteString("<h2>Token 消费（本月）</h2><div class=\"grid\">")
	if usage == nil {
		b.WriteString("<div class=\"card muted\">用量面暂不可用</div></div>")
		return
	}
	if t, ok := usage["total"]; ok {
		card(b, "配额账（done 口径）", fmt.Sprintf("%v", t), "")
	}
	if t, ok := usage["real_total"]; ok {
		card(b, "实销（逐调用）", fmt.Sprintf("%v", t), "warn")
	}
	if t, ok := usage["reserved_total"]; ok {
		card(b, "活跃预留", fmt.Sprintf("%v", t), "")
	}
	if t, ok := usage["budget"]; ok && t != nil {
		card(b, "月度预算", fmt.Sprintf("%v", t), "")
	}
	b.WriteString("</div>")
}

// renderConsoleFailures 失败原因归类（Registry 计数，验收问题「任务失败原因」）。
func renderConsoleFailures(b *strings.Builder, ms []obs.ConsoleMetric) {
	b.WriteString("<h2>失败原因归类（进程累计）</h2><table><tr><th>类别</th><th>维度</th><th class=\"num\">次数</th></tr>")
	rows := 0
	for _, m := range ms {
		switch m.Name {
		case "argus_tasks_total":
			if m.Labels["status"] != "failed" {
				continue
			}
			rows++
			fmt.Fprintf(b, "<tr><td>任务失败</td><td class=\"muted\">%s/%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["platform"])), html.EscapeString(orDash(m.Labels["kind"])), int(m.Value))
		case "argus_budget_exceeded_total":
			rows++
			fmt.Fprintf(b, "<tr><td>预算超限拒绝</td><td class=\"muted\">ledger=%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["ledger"])), int(m.Value))
		case "argus_llm_failovers_total":
			rows++
			fmt.Fprintf(b, "<tr><td>LLM 主→备切换</td><td class=\"muted\">%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["stage"])), int(m.Value))
		case "argus_plugin_failures_total":
			rows++
			fmt.Fprintf(b, "<tr><td>插件失败</td><td class=\"muted\">%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["plugin"])), int(m.Value))
		case "argus_qa_failures_total":
			rows++
			fmt.Fprintf(b, "<tr><td>评论链路失败</td><td class=\"muted\">%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["reason"])), int(m.Value))
		case "argus_comment_post_failures_total":
			rows++
			fmt.Fprintf(b, "<tr><td>回帖失败</td><td class=\"muted\">%s</td><td class=\"num\">%d</td></tr>",
				html.EscapeString(orDash(m.Labels["kind"])), int(m.Value))
		}
	}
	if rows == 0 {
		b.WriteString("<tr><td colspan=\"3\" class=\"muted\">暂无失败记录 🎉</td></tr>")
	}
	b.WriteString("</table>")
}

// renderConsoleFixLoop 修复闭环有效率（阶段 3 验收问题「自动修复有效率」）：
// 分母=fix_verify passed+failed，分子=fix_push pushed+cr_created。
func renderConsoleFixLoop(b *strings.Builder, ms []obs.ConsoleMetric) {
	var verifyPassed, verifyFailed, pushOK, pushFail float64
	for _, m := range ms {
		switch m.Name {
		case "argus_fix_verify_total":
			if m.Labels["result"] == "passed" {
				verifyPassed += m.Value
			} else {
				verifyFailed += m.Value
			}
		case "argus_fix_push_total":
			switch m.Labels["result"] {
			case "pushed", "cr_created":
				pushOK += m.Value
			default:
				pushFail += m.Value
			}
		}
	}
	verifyTotal := verifyPassed + verifyFailed
	b.WriteString("<h2>修复闭环有效率（Verified Fix Loop）</h2><div class=\"grid\">")
	card(b, "沙箱验证 通过/尝试",
		fmt.Sprintf("%d / %d", int(verifyPassed), int(verifyTotal)), "")
	if verifyTotal > 0 {
		card(b, "验证通过率", fmt.Sprintf("%.1f%%", verifyPassed/verifyTotal*100),
			tone(verifyPassed/verifyTotal, 0.8, 0.5))
	} else {
		card(b, "验证通过率", "—", "")
	}
	pushTotal := pushOK + pushFail
	if pushTotal > 0 {
		card(b, "推送成功率（分子/尝试）",
			fmt.Sprintf("%.1f%% (%d/%d)", pushOK/pushTotal*100, int(pushOK), int(pushTotal)),
			tone(pushOK/pushTotal, 0.8, 0.5))
	} else {
		card(b, "推送成功率", "—（环未启用）", "")
	}
	b.WriteString("</div>")
}

// renderConsoleRepos 按仓明细表（行过滤后的 stats 透出；类型=StatsOverview
// 的 visible 切片 []storage.RepoStats）。
func renderConsoleRepos(b *strings.Builder, stats map[string]any) {
	b.WriteString("<h2>按仓明细（30 天）</h2>")
	repos, ok := stats["repos"].([]storage.RepoStats)
	if !ok {
		b.WriteString("<p class=\"muted\">明细不可用</p>")
		return
	}
	if len(repos) == 0 {
		b.WriteString("<p class=\"muted\">窗口内无任务</p>")
		return
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Tokens > repos[j].Tokens })
	b.WriteString("<table><tr><th>仓库</th><th class=\"num\">任务</th><th class=\"num\">通过</th>" +
		"<th class=\"num\">意见</th><th class=\"num\">打回</th><th class=\"num\">Token</th>" +
		"<th class=\"num\">抑制</th></tr>")
	for _, r := range repos {
		fmt.Fprintf(b, "<tr><td>%s/%s</td><td class=\"num\">%d</td><td class=\"num\">%d</td>"+
			"<td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%d</td>"+
			"<td class=\"num\">%d</td></tr>",
			html.EscapeString(r.Platform+"/"+r.Owner), html.EscapeString(r.Repo),
			r.TasksDone, r.Approved, r.Commented, r.Rejected, r.Tokens, r.Suppressed)
	}
	b.WriteString("</table>")
}

// card 概览卡片段。
func card(b *strings.Builder, k, v, tone string) {
	fmt.Fprintf(b, "<div class=\"card\"><div class=\"k\">%s</div><div class=\"v %s\">%s</div></div>",
		html.EscapeString(k), tone, html.EscapeString(v))
}

// tone 比例 → 卡片色阶（>=hi 绿 / >=mid 默认 / 否则红）。
func tone(ratio, hi, mid float64) string {
	switch {
	case ratio >= hi:
		return "ok"
	case ratio >= mid:
		return ""
	default:
		return "bad"
	}
}
