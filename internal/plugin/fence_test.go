package plugin

import (
	"strings"
	"testing"
)

// TestResolveShellTemplate {dir} 占位符替换的转义契约（v5.3.2）：
// 含空格/单引号/shell 元字符的路径必须以单一 shell word 形态进模板，
// 否则 `sh -c` 拆词后验证命令静默跑错目录。
func TestResolveShellTemplate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tmpl string
		dir  string
		want string
	}{
		{"plain", "docker run -v {dir}:/w scan", "/work/repo", "docker run -v '/work/repo':/w scan"},
		{"space", "cd {dir} && make", "/work/my repo", "cd '/work/my repo' && make"},
		{"quote", "ls {dir}", "/work/it's", "ls '/work/it'\\''s'"},
		{"metachar", "ls {dir}", "/work/$(rm -rf /)", "ls '/work/$(rm -rf /)'"},
		{"empty", "ls {dir}", "", "ls ''"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveShellTemplate(tc.tmpl, tc.dir)
			if got != tc.want {
				t.Fatalf("ResolveShellTemplate(%q, %q) = %q, want %q", tc.tmpl, tc.dir, got, tc.want)
			}
		})
	}
}

// TestResolveShellTemplateNoPlaceholder 无占位符模板原样返回（部署方模板不含
// {dir} 时零干预）。
func TestResolveShellTemplateNoPlaceholder(t *testing.T) {
	t.Parallel()
	tmpl := "echo hello && exit 0"
	if got := ResolveShellTemplate(tmpl, "/work"); got != tmpl {
		t.Fatalf("got %q, want原样 %q", got, tmpl)
	}
	if strings.Count(ResolveShellTemplate("{dir} {dir}", "/w"), "'/w'") != 2 {
		t.Fatal("多处占位符应全部替换")
	}
}
