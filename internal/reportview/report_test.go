package reportview

import (
	"strings"
	"testing"
	"time"

	"argus/internal/plugin"
)

func TestRenderFullReport(t *testing.T) {
	rep := Report{
		TaskID: "t-abc", HeadSHA: "sha123", Version: "v3.0.0",
		ReqSource:      "issue #12",
		ShowCompliance: true,
		ACs: []ACCheckView{
			{ID: "AC-1", Text: "支持登录", Status: plugin.ACImplemented, Evid: "src/login.go:42"},
			{ID: "AC-2", Text: "支持登出", Status: plugin.ACMissing, Evid: "diff 中未见登出逻辑"},
		},
		ScopeCreep: []ScopeView{{File: "extra.go", Note: "与本需求无关的重构"}},
		Findings: []plugin.Finding{
			{File: "a.go", Line: 10, Severity: "high", Comment: "SQL 拼接", By: "ocr"},
			{File: "b.go", Line: 3, Severity: "low", Comment: "命名", By: "correctness"},
		},
		VerdictText: "⛔ 需求未完成（1 项未达标）", Rule: "R5.1",
		Meta: true, Plugins: []string{"correctness", "ocr"},
		Elapsed: 1500 * time.Millisecond, Tokens: 12345, Suppressed: 2,
	}
	out := Render(rep)

	for _, want := range []string{
		"<!-- argus:report task_id=t-abc head=sha123 ver=v3.0.0 -->",
		"## 📋 Argus 需求复核报告（需求源: issue #12）",
		"### 验收清单完成度: 1/2 ⛔",
		"❌ **AC-2** 支持登出 — missing",
		"### ⚠️ 需求外改动（scope creep）",
		"`a.go:10` **[HIGH]** SQL 拼接 — by ocr",
		"### 结论: ⛔ 需求未完成（1 项未达标）（R5.1）",
		"> task `t-abc` · 插件: correctness,ocr · 耗时 1s · tokens 12345",
		"2 条历史意见已被标记为误报",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告缺片段 %q\n---\n%s", want, out)
		}
	}
}

func TestRenderNoACBranch(t *testing.T) {
	// §11 N=0 分支：不输出 0/0，输出"无验收清单"提示
	rep := Report{
		TaskID: "t-x", Version: "v3.0.0", ShowCompliance: true,
		VerdictText: "⚠️ 需人工确认", Rule: "R5.3",
	}
	out := Render(rep)
	if strings.Contains(out, "0/0") {
		t.Fatalf("N=0 不应输出 0/0:\n%s", out)
	}
	if !strings.Contains(out, "无验收清单（需求模糊或未关联，需人工确认）") {
		t.Fatalf("缺 N=0 分支文案:\n%s", out)
	}
}

func TestRenderReviewOnlyOmitsCompliance(t *testing.T) {
	rep := Report{TaskID: "t-y", Version: "v3.0.0", VerdictText: "💬 有审查意见，请参考", Rule: "R5.4",
		Findings: []plugin.Finding{{File: "x.go", Line: 1, Severity: "medium", Comment: "边界", By: "security"}}}
	out := Render(rep)
	if strings.Contains(out, "验收清单") {
		t.Fatalf("code_review-only 不应渲染需求段:\n%s", out)
	}
	if !strings.Contains(out, "`x.go:1` **[MEDIUM]**") {
		t.Fatalf("缺 finding 行:\n%s", out)
	}
}

func TestRenderMetaOff(t *testing.T) {
	rep := Report{TaskID: "t-z", Version: "v3.0.0", VerdictText: "✅ 通过", Meta: false, Tokens: 999}
	out := Render(rep)
	if strings.Contains(out, "tokens 999") {
		t.Fatal("Meta=false 不应输出元信息行")
	}
}
