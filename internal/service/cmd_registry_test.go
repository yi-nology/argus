package service

import (
	"context"
	"strings"
	"testing"

	"argus/internal/config"
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
		if got := matchAnyCommand(c.in); got != c.want {
			t.Errorf("matchAnyCommand(%q) = %v, want %v", c.in, got, c.want)
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
		if got := isDriveCommand(c.in); got != c.want {
			t.Errorf("isDriveCommand(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExtractInfoCommands(t *testing.T) {
	if !extractHelp("help") || !extractHelp("帮助") || extractHelp("helping") {
		t.Fatal("help 解析不符")
	}
	if !extractStatus("status") || !extractStatus("进度") || extractStatus("statusbar") {
		t.Fatal("status 解析不符")
	}
}

// help 手册：目录全覆盖（每条指令的用法必须出现在手册里，防 help 漏项）。
func TestHelpText_CoversCatalog(t *testing.T) {
	s := &Service{Cfg: &config.Config{CommentGate: "collaborator"}}
	out := s.helpText()
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
}

// actorCanDrive：open 模式零调用放行；collaborator 模式空 actor 拒绝。
func TestActorCanDrive_OpenAndEmpty(t *testing.T) {
	s := &Service{Cfg: &config.Config{CommentGate: "open"}}
	if !s.actorCanDrive(context.Background(), "gitea", "o", "r", "anyone") {
		t.Fatal("open 模式应放行")
	}
	s.Cfg.CommentGate = "collaborator"
	if s.actorCanDrive(context.Background(), "gitea", "o", "r", "") {
		t.Fatal("collaborator 模式空 actor 应拒绝（无需 SDK）")
	}
}
