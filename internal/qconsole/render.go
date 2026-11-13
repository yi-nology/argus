package qconsole

import (
	"fmt"
	"html/template"
	"strings"
)

// pageTemplate 自包含静态页（inline CSS，无外部资源——离线可开的只读控制台）。
var pageTemplate = template.Must(template.New("qconsole").Funcs(template.FuncMap{
	"quotaBar": quotaBar,
}).Parse(`<!doctype html>
<html lang="zh"><head><meta charset="utf-8">
<title>Argus 审查质量控制台</title>
<style>
body{font-family:-apple-system,'PingFang SC',sans-serif;margin:24px;color:#1f2328;background:#f6f8fa}
h1{font-size:20px}h2{font-size:16px;margin-top:28px;border-bottom:1px solid #d0d7de;padding-bottom:4px}
table{border-collapse:collapse;background:#fff;font-size:13px;width:100%;margin-top:8px}
th,td{border:1px solid #d0d7de;padding:5px 9px;text-align:left}
th{background:#eaeef2}
.kpis{display:flex;gap:14px;flex-wrap:wrap;margin-top:10px}
.kpi{background:#fff;border:1px solid #d0d7de;border-radius:8px;padding:10px 16px;min-width:130px}
.kpi .v{font-size:22px;font-weight:600}.kpi .l{font-size:12px;color:#57606a}
.bar{background:#1f6feb;height:10px;border-radius:5px;display:inline-block;vertical-align:middle}
.muted{color:#57606a;font-size:12px}
.ok{color:#1a7f37}.warn{color:#9a6700}.bad{color:#cf222e}
</style></head><body>
<h1>Argus 审查质量控制台 <span class="muted">只读 · 生成于 {{.GeneratedAt.Format "2006-01-02 15:04:05"}} · 统计窗口 {{.WindowDays}} 天</span></h1>

<div class="kpis">
  <div class="kpi"><div class="v">{{.Tasks}}</div><div class="l">完成任务（窗口内）</div></div>
  <div class="kpi"><div class="v">{{.Suppressed}}</div><div class="l">误报抑制（累计指纹）</div></div>
  <div class="kpi"><div class="v">{{.Tokens}}</div><div class="l">Token 消耗（quota 账本）</div></div>
  <div class="kpi"><div class="v">{{printf "%.2f" .FPRateProxy}}</div><div class="l">RC 率（request_changes 占比）</div></div>
</div>

<h2>误报反馈（抑制面）与各仓概览</h2>
<table>
<tr><th>仓库</th><th>任务</th><th>approve</th><th>comment</th><th>request_changes</th><th>抑制</th><th>tokens</th></tr>
{{range .RepoRows}}<tr><td>{{.Platform}}/{{.Owner}}/{{.Repo}}</td><td>{{.TasksDone}}</td><td>{{.Approved}}</td><td>{{.Commented}}</td><td>{{.Rejected}}</td><td>{{.Suppressed}}</td><td>{{.Tokens}}</td></tr>{{end}}
</table>

<h2>插件表现（失败/跳过合计，/metrics 活面）</h2>
{{if .PluginRows}}<table><tr><th>插件</th><th>计数</th></tr>
{{range .PluginRows}}<tr><td>{{.Name}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="muted">无数据（/metrics 缺位或全部为零）</p>{{end}}

<h2>任务失败原因归类（LLM 降级 / 插件失败 / 预算超限 …）</h2>
{{if .FailReasons}}<table><tr><th>原因</th><th>计数</th></tr>
{{range .FailReasons}}<tr><td>{{.Name}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="muted">无失败记录（或 /metrics 缺位）</p>{{end}}

<h2>Token 消费（{{.Month}} 双账本）</h2>
<table>
<tr><th>账本</th><th>消耗</th><th>预算</th><th>水位</th></tr>
<tr><td>quota（done 口径）</td><td>{{.QuotaTotal}}</td><td>{{.QuotaBudget}}</td><td>{{quotaBar .QuotaTotal .QuotaBudget}}</td></tr>
<tr><td>real（全终态口径）</td><td>{{.RealTotal}}</td><td>{{.RealBudget}}</td><td>{{quotaBar .RealTotal .RealBudget}}</td></tr>
</table>
{{if .UsageRows}}<table><tr><th>仓库</th><th>本月 tokens</th></tr>
{{range .UsageRows}}<tr><td>{{.Platform}}/{{.Owner}}/{{.Repo}}</td><td>{{.Tokens}}</td></tr>{{end}}</table>{{end}}

<h2>质量趋势（P2-02 评测基线）</h2>
{{if .EvalTrend}}<p class="muted">模型 {{.EvalTrend.Model}} · 生成于 {{.EvalTrend.GeneratedAt.Format "2006-01-02 15:04"}} · 裁决稳定性 {{printf "%.3f" .EvalTrend.Stability}}</p>
<table><tr><th>维度</th><th>Precision</th><th>Recall</th><th>误报率</th><th>漏检率</th></tr>
<tr><td>总体</td><td>{{printf "%.3f" .EvalTrend.Overall.P}}</td><td>{{printf "%.3f" .EvalTrend.Overall.R}}</td><td>{{printf "%.3f" .EvalTrend.Overall.FP}}</td><td>{{printf "%.3f" .EvalTrend.Overall.Miss}}</td></tr>
{{range .EvalTrend.ByLanguage}}<tr><td>{{.Language}}</td><td>—</td><td>—</td><td>{{printf "%.3f" .FP}}</td><td>{{printf "%.3f" .Miss}}</td></tr>{{end}}
</table>{{else}}<p class="muted">无评测基线报告（make eval-baseline / eval-real 产出 eval/results/baseline-latest.json 后自动上屏）</p>{{end}}

<p class="muted">数据全部来自既有 API 聚合（/stats/overview · /usage/monthly · /metrics · 评测基线文件），无新表。</p>
</body></html>`))

// quotaBar 预算水位条（百分比宽度；超预算红色满条）。
func quotaBar(used, budget int64) template.HTML {
	if budget <= 0 {
		return template.HTML(`<span class="muted">未设预算</span>`)
	}
	cls := ""
	if pct := float64(used) / float64(budget) * 100; pct >= 90 {
		cls = "bad"
	} else if pct >= 70 {
		cls = "warn"
	}
	pct := float64(used) / float64(budget) * 100
	if pct > 100 {
		pct = 100
	}
	return template.HTML(fmt.Sprintf(`<span class="%s">%.0f%%</span> <span class="bar" style="width:%dpx"></span>`, cls, float64(used)/float64(budget)*100, int(pct)))
}

// FPRateProxy RC 率代理（骨架口径：误报率真值来自评测基线——verdict 分布里
// request_changes 占比是同面可见的第一近似；文档注明）。
func (s *Snapshot) FPRateProxy() float64 {
	den := s.Verdicts.Approve + s.Verdicts.Comment + s.Verdicts.RequestChanges
	if den == 0 {
		return 0
	}
	return float64(s.Verdicts.RequestChanges) / float64(den)
}

// RenderHTML 静态页输出（自包含单文件，离线可开）。
func RenderHTML(s *Snapshot) ([]byte, error) {
	var sb strings.Builder
	if err := pageTemplate.Execute(&sb, s); err != nil {
		return nil, fmt.Errorf("qconsole: render html: %w", err)
	}
	return []byte(sb.String()), nil
}

// RenderText CLI 文本输出（终端/工单粘贴面）。
func RenderText(s *Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Argus 审查质量控制台（只读）  生成于 %s · 窗口 %d 天\n", s.GeneratedAt.Format("2006-01-02 15:04:05"), s.WindowDays)
	fmt.Fprintf(&b, "%s\n", strings.Repeat("=", 72))
	fmt.Fprintf(&b, "KPI: 任务 %d · 误报抑制 %d · Token %d · RC 率 %.2f\n\n", s.Tasks, s.Suppressed, s.Tokens, s.FPRateProxy())
	fmt.Fprintln(&b, "误报反馈与各仓概览：")
	for _, r := range s.RepoRows {
		fmt.Fprintf(&b, "  %s/%s/%s  任务 %d  approve %d  comment %d  rc %d  抑制 %d  tokens %d\n",
			r.Platform, r.Owner, r.Repo, r.TasksDone, r.Approved, r.Commented, r.Rejected, r.Suppressed, r.Tokens)
	}
	fmt.Fprintln(&b, "\n插件表现（失败/跳过合计）：")
	if len(s.PluginRows) == 0 {
		b.WriteString("  （无数据）\n")
	}
	for _, r := range s.PluginRows {
		fmt.Fprintf(&b, "  %-24s %d\n", r.Name, r.Count)
	}
	fmt.Fprintln(&b, "\n失败原因归类：")
	if len(s.FailReasons) == 0 {
		b.WriteString("  （无失败记录）\n")
	}
	for _, r := range s.FailReasons {
		fmt.Fprintf(&b, "  %-24s %d\n", r.Name, r.Count)
	}
	fmt.Fprintf(&b, "\nToken 消费（%s 双账本）：quota %d/%s real %d/%s\n", s.Month,
		s.QuotaTotal, budgetWord(s.QuotaBudget), s.RealTotal, budgetWord(s.RealBudget))
	for _, r := range s.UsageRows {
		fmt.Fprintf(&b, "  %s/%s/%s  %d\n", r.Platform, r.Owner, r.Repo, r.Tokens)
	}
	fmt.Fprintln(&b, "\n质量趋势（评测基线）：")
	if s.EvalTrend == nil {
		b.WriteString("  （无评测基线报告）\n")
	} else {
		fmt.Fprintf(&b, "  模型 %s · 稳定性 %.3f · P %.3f · R %.3f · 误报率 %.3f · 漏检率 %.3f\n",
			s.EvalTrend.Model, s.EvalTrend.Stability, s.EvalTrend.Overall.P, s.EvalTrend.Overall.R, s.EvalTrend.Overall.FP, s.EvalTrend.Overall.Miss)
		for _, l := range s.EvalTrend.ByLanguage {
			fmt.Fprintf(&b, "  [%s] 误报率 %.3f · 漏检率 %.3f\n", l.Language, l.FP, l.Miss)
		}
	}
	return b.String()
}

func budgetWord(budget int64) string {
	if budget <= 0 {
		return "未设预算"
	}
	return fmt.Sprintf("%d", budget)
}
