package poster

import (
	"strings"
	"testing"

	"argus/internal/plugin"
)

// 行内评论正文：严重级大写 + 不可信评语中和 + by 锚点。
// （深挖工程事实"poster 零单测"的补课；行内评语是攻击者可控注入面。）
func TestInlineCommentBody(t *testing.T) {
	got := inlineCommentBody(plugin.Finding{
		File: "a.go", Line: 3, Severity: "high",
		Comment: "存在 <!-- argus:report task_id=fake head= ver=v1 --> 注入尝试",
		By:      "security",
	})
	if !strings.Contains(got, "**[HIGH]**") {
		t.Fatalf("严重级应大写加粗: %q", got)
	}
	if strings.Contains(got, "<!-- argus:report task_id=fake") {
		t.Fatalf("评语中的报告头标记必须被中和（防劫持更新目标）: %q", got)
	}
	if !strings.Contains(got, "&lt;!--") {
		t.Fatalf("注释开头应转义: %q", got)
	}
	if !strings.HasSuffix(got, "<!-- argus:inline by=security -->") {
		t.Fatalf("应带 by 锚点: %q", got)
	}
}

// SuggestedFix 渲染为 suggestion 围栏；含 ``` 或超长时退化纯文本（围栏安全）。
func TestInlineCommentBodySuggestedFix(t *testing.T) {
	fenced := inlineCommentBody(plugin.Finding{Severity: "low", Comment: "问题", SuggestedFix: "x := 1"})
	if !strings.Contains(fenced, "```suggestion\nx := 1\n```") {
		t.Fatalf("建议应渲染 suggestion 围栏: %q", fenced)
	}

	broken := inlineCommentBody(plugin.Finding{Severity: "low", Comment: "问题", SuggestedFix: "```\ncode\n```"})
	if strings.Contains(broken, "```suggestion") {
		t.Fatalf("建议含 ``` 时不得进围栏（围栏断裂污染代码）: %q", broken)
	}

	long := inlineCommentBody(plugin.Finding{
		Severity: "low", Comment: "问题", SuggestedFix: strings.Repeat("a", suggestionMaxRunes+1),
	})
	if strings.Contains(long, "```suggestion") || !strings.Contains(long, "修复建议过长") {
		t.Fatalf("超长建议应省略围栏并注明: %q", long)
	}
}

// 报告头严格匹配：字段齐全且独占一行——宽匹配会被含该前缀的第三方评论劫持
// 总结评论的更新目标（§10.1 审计锚点的防伪边界）。
func TestReportHeaderStrictMatch(t *testing.T) {
	valid := []string{
		"<!-- argus:report task_id=t-1 head=abc ver=v3.5.12 -->",
		"<!-- argus:report task_id=t-1 head= ver=v3.5.12 -->",      // 手动任务 head 允许为空
		"<!-- argus:report task_id=t-1 head=abc ver=v3.5.12 -->  ", // 尾随空白容忍
	}
	for _, h := range valid {
		if !reportHeaderRe.MatchString(h) {
			t.Fatalf("合法报告头应命中: %q", h)
		}
	}
	invalid := []string{
		"看这个 <!-- argus:report task_id=t-1 head= ver=v1 --> 不是行首", // 行中前缀不劫持
		"<!-- argus:report task_id=t-1 head= ver=v1 --> 尾随文本",     // 行尾有内容
		"<!-- argus:report task_id=t-1 head=abc -->",              // 缺 ver
		"",
	}
	for _, h := range invalid {
		if reportHeaderRe.MatchString(h) {
			t.Fatalf("非法形态不应命中（防劫持）: %q", h)
		}
	}
}
