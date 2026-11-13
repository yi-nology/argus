package plugin

import "testing"

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
	})()
}
