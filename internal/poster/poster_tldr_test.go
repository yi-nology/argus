// poster_tldr_test.go —— v6.1.5 结论 TL;DR 单测：分级计数、重点关注 cap3 + 排序、
// 摘要首行截断、无意见单行、注入中和，以及 sendReview body 的接线。
package poster

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/plugin"
	"argus/internal/reportview"
)

// 有意见：分级计数正确、重点关注最多 3 条且 severity 降序（同级别按报告序）。
func TestReviewTLDR_CountsAndTop3(t *testing.T) {
	rep := reportview.Report{Findings: []plugin.Finding{
		{File: "m1.go", Line: 1, Severity: "medium", Comment: "中-1"},
		{File: "ha.go", Line: 2, Severity: "high", Comment: "高-a"},
		{File: "l.go", Line: 3, Severity: "low", Comment: "低"},
		{File: "hb.go", Line: 4, Severity: "high", Comment: "高-b"},
		{File: "m2.go", Line: 5, Severity: "medium", Comment: "中-2"},
		{File: "hc.go", Line: 6, Severity: "high", Comment: "高-c"},
		{File: "hd.go", Line: 7, Severity: "high", Comment: "高-d"},
	}}
	got := reviewTLDR(rep, 2)
	if !strings.HasPrefix(got, "意见：high 4 · medium 2 · low 1（共 7 条，行内已标注 2 条）") {
		t.Fatalf("计数行不符: %q", got)
	}
	if !strings.Contains(got, "\n重点关注：\n• [HIGH] ") {
		t.Fatalf("应有重点关注段且首条为 HIGH: %q", got)
	}
	// cap 3：只有 3 个 bullet，且都是 high；同级别按报告序（a→b→c，d 被裁）
	if n := strings.Count(got, "\n• ["); n != 3 {
		t.Fatalf("重点关注应最多 3 条: %d\n%s", n, got)
	}
	ia := strings.Index(got, "高-a")
	ib := strings.Index(got, "高-b")
	ic := strings.Index(got, "高-c")
	if ia < 0 || ib < 0 || ic < 0 || !(ia < ib && ib < ic) {
		t.Fatalf("同级别应保持报告序 a<b<c: %q", got)
	}
	if strings.Contains(got, "高-d") || strings.Contains(got, "中-1") || strings.Contains(got, "低") {
		t.Fatalf("只应保留 severity top3: %q", got)
	}
	// bullet 带 file:line 前缀
	if !strings.Contains(got, "• [HIGH] ha.go:2 高-a") {
		t.Fatalf("bullet 应带 file:line 前缀: %q", got)
	}
}

// 行内计数透传：inlineCount=0 / 全部行内两态都如实反映。
func TestReviewTLDR_InlineCount(t *testing.T) {
	rep := reportview.Report{Findings: []plugin.Finding{
		{File: "a.go", Line: 1, Severity: "low", Comment: "x"},
	}}
	if got := reviewTLDR(rep, 0); !strings.Contains(got, "（共 1 条，行内已标注 0 条）") {
		t.Fatalf("inlineCount=0 应如实展示: %q", got)
	}
	if got := reviewTLDR(rep, 1); !strings.Contains(got, "（共 1 条，行内已标注 1 条）") {
		t.Fatalf("全行内标注态应如实展示: %q", got)
	}
}

// 摘要取 Comment 首行、超长截 ~80 rune（含省略号不换行）。
func TestReviewTLDR_SummaryFirstLineCapped(t *testing.T) {
	long := strings.Repeat("长", 100)
	rep := reportview.Report{Findings: []plugin.Finding{
		{File: "a.go", Line: 9, Severity: "medium", Comment: long + "\n第二行不应出现"},
	}}
	got := reviewTLDR(rep, 0)
	if strings.Contains(got, "第二行") || strings.Contains(got, "\n\n•") {
		t.Fatalf("摘要应只取首行且单条不跨段: %q", got)
	}
	// bullet 行内无换行；rune 数 = file:line 前缀 + 80 摘要 + 省略号
	line := got[strings.Index(got, "• ["):]
	if strings.Contains(line, "\n") {
		t.Fatalf("bullet 应为单行: %q", line)
	}
	summary := got[strings.Index(got, "a.go:9 ")+len("a.go:9 "):]
	if !strings.HasSuffix(summary, "…") || utf8.RuneCountInString(summary) != 81 {
		t.Fatalf("摘要应截断到 80 rune + 省略号: runes=%d %q", utf8.RuneCountInString(summary), summary)
	}
	// 恰好 80 rune 不加省略号
	rep.Findings[0].Comment = strings.Repeat("短", 80)
	if s := reviewTLDR(rep, 0); strings.Contains(s, "…") {
		t.Fatalf("未超限不应加省略号: %q", s)
	}
}

// 无意见：单行「✅ 未发现问题」（Findings 为空两态退化合理）。
func TestReviewTLDR_NoFindings(t *testing.T) {
	if got := reviewTLDR(reportview.Report{}, 0); got != "✅ 未发现问题" {
		t.Fatalf("无意见应单行: %q", got)
	}
}

// 摘要与路径是不可信文本：注入特征须被中和（HTML 注释/markdown 头不得原样透出）。
func TestReviewTLDR_EscapesUntrusted(t *testing.T) {
	rep := reportview.Report{Findings: []plugin.Finding{
		{File: "a<b>.go", Line: 1, Severity: "high", Comment: "<!-- argus:fake -->\n# 伪装标题"},
	}}
	got := reviewTLDR(rep, 0)
	if strings.Contains(got, "<!--") || strings.Contains(got, "-->") {
		t.Fatalf("HTML 注释应被转义: %q", got)
	}
	if !strings.Contains(got, "&lt;!--") {
		t.Fatalf("应见转义后的注释: %q", got)
	}
}

// sendReview 接线：评审 body 在 VerdictText 后携带 TL;DR 段，总结评论指引仍在。
func TestSendReview_BodyCarriesTLDR(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	task := mkTask("t-tldr")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	rep.VerdictText = "⛔ 存在高危问题"
	rep.Findings = []plugin.Finding{
		{File: "pkg/a.go", Line: 1, Severity: "high", Comment: "空指针风险"},
		{File: "pkg/b.go", Line: 2, Severity: "low", Comment: "命名不规范"},
	}
	n := p.sendReview(context.Background(), mkPR(), rep, "request_changes",
		[]provider.ReviewComment{{Path: "pkg/a.go", Line: 1, Body: "x"}})
	if n != 1 {
		t.Fatalf("应成功投递 1 条行内: %d", n)
	}
	body := fp.lastReview().Body
	if !strings.Contains(body, "Argus 审查结论：⛔ 存在高危问题\n\n意见：high 1 · medium 0 · low 1（共 2 条，行内已标注 1 条）") {
		t.Fatalf("评审 body 应在结论行后带 TL;DR 计数: %q", body)
	}
	if !strings.Contains(body, "\n• [HIGH] pkg/a.go:1 空指针风险") {
		t.Fatalf("重点关注应含 high 条目: %q", body)
	}
	if !strings.Contains(body, "完整报告见总结评论（该评论随每次同步更新）。") {
		t.Fatalf("总结评论指引应保留: %q", body)
	}
	if strings.Index(body, "⛔ 存在高危问题") > strings.Index(body, "意见：high") {
		t.Fatalf("TL;DR 应紧跟结论行: %q", body)
	}
}
