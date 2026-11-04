package commentcmd

import (
	"strings"
	"testing"
)

// v5.3.0 指令目录单源：matchAnyCommand/isDriveCommand/信息类解析与手册渲染。

func TestMatchAnyCommand(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"review", true},
		{"复核 fp:ab12cd34", true},
		{"recheck fp:ab12cd34", true},
		{"status", true},
		{"帮助", true},
		{"describe 重点看登录", true},
		{"fix fp:ab12cd34", true},
		{"changelog", true},
		{"帮我看看这个改动", false},
		{"asking for help", false}, // ask 词边界
		{"", false},
	}
	for _, c := range cases {
		if got := MatchAnyCommand(c.in); got != c.want {
			t.Errorf("MatchAnyCommand(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsDriveCommand(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"review", true},
		{"recheck fp:ab12cd34", true},
		{"复核 fp:ab12cd34", true},
		{"fix fp:ab12cd34", true},
		{"testgen internal/x.go", true},
		{"ask 这个改动安全吗", false},
		{"status", false},
		{"help", false},
		{"describe", false},
		{"changelog", false},
		{"suppress fp:ab12cd34", false}, // 误报反馈不烧预算，不门控
	}
	for _, c := range cases {
		if got := IsDriveCommand(c.in); got != c.want {
			t.Errorf("IsDriveCommand(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExtractInfoCommands(t *testing.T) {
	if !ExtractHelp("help") || !ExtractHelp("帮助") || ExtractHelp("helping") {
		t.Fatal("help 解析不符")
	}
	if !ExtractStatus("status") || !ExtractStatus("进度") || ExtractStatus("statusbar") {
		t.Fatal("status 解析不符")
	}
}

// TestHelpText_CoversCatalog help 手册目录全覆盖（每条指令的用法与说明必须出现在
// 手册里，防 help 漏项）；fixEnabled=false 时标注未启用。
func TestHelpText_CoversCatalog(t *testing.T) {
	out := HelpText(true)
	if !strings.Contains(out, "指令速查") {
		t.Fatalf("手册缺标题: %q", out)
	}
	for _, c := range cmdCatalog {
		if !strings.Contains(out, c.usage) {
			t.Errorf("手册缺指令 %q 的用法 %q", c.word, c.usage)
		}
		if !strings.Contains(out, c.desc) {
			t.Errorf("手册缺指令 %q 的说明", c.word)
		}
	}
	if !strings.Contains(out, "🔒") {
		t.Error("手册应标注门控指令")
	}
	if strings.Contains(HelpText(false), "fix 未启用") == false {
		t.Error("fixEnabled=false 应标注未启用")
	}
}
