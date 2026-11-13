package plugin

import (
	"strings"
	"testing"
)

// TestParseEvidenceRefs 自由文本解析 file:line（含中文/多引用/无引用三形态）。
func TestParseEvidenceRefs(t *testing.T) {
	refs := ParseEvidenceRefs("改动侧 src/foo.go:12 与消费侧 src/bar.go:34 不一致")
	if len(refs) != 2 {
		t.Fatalf("应解析 2 条引用, got %d", len(refs))
	}
	if refs[0].File != "src/foo.go" || refs[0].Line != 12 {
		t.Fatalf("refs[0] 解析错: %+v", refs[0])
	}
	// 纯文字（差距说明）无引用
	if got := ParseEvidenceRefs("声明了但代码没有实现该能力"); len(got) != 0 {
		t.Fatalf("纯文字应无引用, got %+v", got)
	}
	// ASCII 路径解析（中文路径 best-effort 不覆盖——\w 不含 UTF-8 中文，缺失不阻断）
	refs2 := ParseEvidenceRefs("见 docs/guide.md:5 与 cmd/x.go:1")
	if len(refs2) != 2 {
		t.Fatalf("复杂路径应解析 2 条, got %+v", refs2)
	}
}

// TestVerifyClaimEvidence 交叉校验：改动集内可验证/集外标 ⚠️/纯文字不标。
func TestVerifyClaimEvidence(t *testing.T) {
	changed := []string{"src/foo.go", "src/bar.go"}
	touched := []string{"src/other.go"}

	c := Claim{Claim: "X", Evidence: "src/foo.go:12 与 src/bar.go:34 不一致", Status: ClaimInconsistent}
	res := VerifyClaimEvidence(c, changed, touched)
	if !res.Verified {
		t.Fatalf("改动集内引用应验证通过: %+v", res)
	}

	c2 := Claim{Claim: "Y", Evidence: "external/lib.go:99 缺失", Status: ClaimMissing}
	res2 := VerifyClaimEvidence(c2, changed, touched)
	if res2.Verified {
		t.Fatalf("集外引用不应验证通过: %+v", res2)
	}
	if !contains(res2.Note, "⚠️ 证据待验证") {
		t.Fatalf("证据待验证注记缺失: %q", res2.Note)
	}

	// 行号无效（line=0）也算不可验证
	c3 := Claim{Claim: "Z", Evidence: "src/foo.go:abc", Status: ClaimMissing}
	res3 := VerifyClaimEvidence(c3, changed, touched)
	if len(res3.Refs) != 0 && res3.Verified {
		t.Fatalf("行号无效引用不应验证通过: %+v", res3)
	}

	// 纯文字不算证据缺陷
	c4 := Claim{Claim: "W", Evidence: "差距：模块 X 未与 Y 对齐", Status: ClaimMissing}
	res4 := VerifyClaimEvidence(c4, changed, touched)
	if !res4.Verified || res4.Note != "" {
		t.Fatalf("纯文字应无缺陷: %+v", res4)
	}
}

// TestVerifyClaimsEvidence 高风险降级 + 软标注（不丢弃声明）。
func TestVerifyClaimsEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}
	sec := &AnalystSections{Claims: []Claim{
		{Claim: "a", Evidence: "src/foo.go:1", Status: ClaimMissing},
		{Claim: "b", Evidence: "external/x.go:2", Status: ClaimMissing},
		{Claim: "c", Evidence: "src/touch.go:3", Status: ClaimImplemented},
	}}
	verified, total := VerifyClaimsEvidence(sec, changed, touched)
	if total != 3 {
		t.Fatalf("total=%d want 3", total)
	}
	if verified != 2 {
		t.Fatalf("verified=%d want 2", verified)
	}
	// a 可验证（missing 保留）
	if sec.Claims[0].Status != ClaimMissing {
		t.Fatalf("可验证 missing 不应降级: %v", sec.Claims[0])
	}
	// b 集外 + 高风险 missing → 降级 partial，Evidence 追加 ⚠️
	if sec.Claims[1].Status != ClaimPartial {
		t.Fatalf("不可验证 missing 应降级 partial: %v", sec.Claims[1])
	}
	if !contains(sec.Claims[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("应追加 ⚠️ 注记: %q", sec.Claims[1].Evidence)
	}
	// c 可验证 implemented 不动
	if sec.Claims[2].Status != ClaimImplemented {
		t.Fatalf("implemented 不应变: %v", sec.Claims[2])
	}
}

// contains 简易子串（测试辅助）。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// TestVerifyFindingEvidence Finding 级证据交叉校验（v6.18.0，与 Claim 级同口径）：
// 集内引用可验证 / 集外引用标 ⚠️ / 无引用（空或纯文字）不判。
func TestVerifyFindingEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}

	f := Finding{File: "src/foo.go", Line: 12, Severity: "high", Comment: "缺判空",
		Evidence: "src/foo.go:12 未做空值检查"}
	res := VerifyFindingEvidence(f, changed, touched)
	if !res.Verified {
		t.Fatalf("改动集内引用应验证通过: %+v", res)
	}

	f2 := Finding{File: "src/foo.go", Line: 20, Severity: "high", Comment: "越权",
		Evidence: "external/lib.go:99 未鉴权"}
	res2 := VerifyFindingEvidence(f2, changed, touched)
	if res2.Verified {
		t.Fatalf("集外引用不应验证通过: %+v", res2)
	}
	if !contains(res2.Note, "⚠️ 证据待验证") {
		t.Fatalf("证据待验证注记缺失: %q", res2.Note)
	}

	// 空证据：无引用可校验，不判（Finding.Evidence 是可选字段）
	f3 := Finding{File: "src/foo.go", Line: 1, Severity: "low", Comment: "命名"}
	res3 := VerifyFindingEvidence(f3, changed, touched)
	if !res3.Verified || res3.Note != "" {
		t.Fatalf("空证据不应判缺陷: %+v", res3)
	}

	// 纯文字差距说明（无 file:line）同 Claim 级纪律：不判
	f4 := Finding{File: "src/foo.go", Line: 2, Severity: "medium", Comment: "口径不一",
		Evidence: "改动与既有模块口径存在差距"}
	res4 := VerifyFindingEvidence(f4, changed, touched)
	if !res4.Verified || res4.Note != "" {
		t.Fatalf("纯文字证据不应判缺陷: %+v", res4)
	}
}

// TestVerifyFindingsEvidence 批量：高危（critical/high）证据不可验证降级 medium
// 退出 R5 一票否决面 + ⚠️ 软标注保留线索；medium/low 只标注不降；幂等不叠加。
func TestVerifyFindingsEvidence(t *testing.T) {
	changed := []string{"src/foo.go"}
	touched := []string{"src/touch.go"}
	mf := &MergedFindings{Findings: []Finding{
		// 1. 可验证 critical → 不动
		{File: "src/foo.go", Line: 1, Severity: "critical", Comment: "a",
			Evidence: "src/foo.go:1 数据损坏路径"},
		// 2. 集外 high → 降级 medium + ⚠️
		{File: "src/foo.go", Line: 2, Severity: "high", Comment: "b",
			Evidence: "external/lib.go:9 越权调用"},
		// 3. 集外 critical → 降级 medium
		{File: "src/foo.go", Line: 3, Severity: "critical", Comment: "c",
			Evidence: "external/x.go:1 注入面"},
		// 4. 集外 medium → 保级 + ⚠️
		{File: "src/foo.go", Line: 4, Severity: "medium", Comment: "d",
			Evidence: "external/y.go:2 边界遗漏"},
		// 5. 触达集内 high → 可验证不动
		{File: "src/foo.go", Line: 5, Severity: "high", Comment: "e",
			Evidence: "src/touch.go:7 消费侧依赖旧语义"},
		// 6. 空证据 low → 不判不动
		{File: "src/foo.go", Line: 6, Severity: "low", Comment: "f"},
	}}
	verified, total := VerifyFindingsEvidence(mf, changed, touched)
	if total != 6 || verified != 3 {
		t.Fatalf("total=%d verified=%d, want 6/3", total, verified)
	}
	fs := mf.Findings
	if fs[0].Severity != "critical" {
		t.Fatalf("可验证 critical 不应降级: %v", fs[0])
	}
	if fs[1].Severity != "medium" || !contains(fs[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 high 应降级 medium + ⚠️: sev=%q evid=%q", fs[1].Severity, fs[1].Evidence)
	}
	if fs[2].Severity != "medium" {
		t.Fatalf("集外 critical 应降级 medium: %q", fs[2].Severity)
	}
	if fs[3].Severity != "medium" || !contains(fs[3].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 medium 应保级 + ⚠️: sev=%q evid=%q", fs[3].Severity, fs[3].Evidence)
	}
	if fs[4].Severity != "high" || contains(fs[4].Evidence, "⚠️") {
		t.Fatalf("触达集内 high 不应动: sev=%q evid=%q", fs[4].Severity, fs[4].Evidence)
	}
	if fs[5].Severity != "low" || fs[5].Evidence != "" {
		t.Fatalf("空证据 low 不应动: %+v", fs[5])
	}

	// 幂等：二次调用注记不叠加、严重度不再变
	VerifyFindingsEvidence(mf, changed, touched)
	if n := strings.Count(mf.Findings[1].Evidence, "⚠️ 证据待验证"); n != 1 {
		t.Fatalf("注记应幂等只出现一次, got %d", n)
	}
	if mf.Findings[1].Severity != "medium" {
		t.Fatalf("二次调用严重度不应再变: %q", mf.Findings[1].Severity)
	}

	// nil 安全
	if v, tot := VerifyFindingsEvidence(nil, changed, touched); v != 0 || tot != 0 {
		t.Fatalf("nil 应返回 0/0, got %d/%d", v, tot)
	}
}
