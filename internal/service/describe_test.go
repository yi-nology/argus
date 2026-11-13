package service

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/commentcmd"
)

func TestExtractDescribe(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		match bool
	}{
		{"describe", "", true},
		{"describe 重构说明", "重构说明", true},
		{"describe  ", "", true},
		{"describex", "", false},
		{"description 更新", "", false},
		{"ask 为什么", "", false},
	}
	for _, c := range cases {
		got, ok := commentcmd.ExtractDescribe(c.in)
		if ok != c.match || (ok && got != c.want) {
			t.Fatalf("commentcmd.ExtractDescribe(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.match)
		}
	}
}

func TestExtractTestGen(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		match bool
	}{
		{"testgen", "", true},
		{"testgen internal/api/user.go", "internal/api/user.go", true},
		{"testgens", "", false},
		{"describe", "", false},
	}
	for _, c := range cases {
		got, ok := commentcmd.ExtractTestGen(c.in)
		if ok != c.match || (ok && got != c.want) {
			t.Fatalf("commentcmd.ExtractTestGen(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.match)
		}
	}
}

func TestFilterDiffByPath(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/b.go b/b.go\n@@ -1 +1 @@\n-x\n+y"
	got := filterDiffByPath(raw, "b.go")
	if !strings.Contains(got, "b.go") || strings.Contains(got, "a.go\n@@") {
		t.Fatalf("过滤错误: %q", got)
	}
	if got := filterDiffByPath(raw, "nope"); got != "" {
		t.Fatalf("未命中应为空: %q", got)
	}
}
