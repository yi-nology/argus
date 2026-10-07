package domain

import "testing"

// TestTaskKindRegistry 钉死词表注册表：7 类全量在册、审查类恰为 R 系列三类、
// 字面值与 IDL 词表对齐（storage 侧曾裸字符串克隆漂移的教训——值回归即测红）。
func TestTaskKindRegistry(t *testing.T) {
	all := []struct {
		kind   string
		review bool
	}{
		{TaskKindRequirementCompliance, true},
		{TaskKindCodeReview, true},
		{TaskKindVisualCheck, true},
		{TaskKindDescribe, false},
		{TaskKindChangelog, false},
		{TaskKindCIAnalysis, false},
		{TaskKindDiffPreview, false},
	}
	for _, tc := range all {
		if !IsKnownKind(tc.kind) {
			t.Errorf("IsKnownKind(%q) = false, 词表漏注册", tc.kind)
		}
		if got := IsReviewKind(tc.kind); got != tc.review {
			t.Errorf("IsReviewKind(%q) = %v, want %v", tc.kind, got, tc.review)
		}
	}
	if IsKnownKind("no_such_kind") {
		t.Error("IsKnownKind 对未注册 kind 返回 true")
	}
	if IsReviewKind("no_such_kind") {
		t.Error("IsReviewKind 对未注册 kind 返回 true")
	}
	// 字面值对齐 IDL 词表（v3.106.0 前.storage isReviewKind 裸字符串漂移实证）。
	literals := map[string]string{
		"requirement_compliance": TaskKindRequirementCompliance,
		"code_review":            TaskKindCodeReview,
		"visual_check":           TaskKindVisualCheck,
		"describe":               TaskKindDescribe,
		"changelog":              TaskKindChangelog,
		"ci_analysis":            TaskKindCIAnalysis,
		"diff_preview":           TaskKindDiffPreview,
	}
	for lit, constant := range literals {
		if lit != constant {
			t.Errorf("词表常量 %q 与 IDL 对齐字面值 %q 不一致", constant, lit)
		}
	}
}
