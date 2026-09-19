package sastscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.example.com/enjoydream/agentkit/reportutil"
)

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}

// fakeBin 写一个可执行 shell 脚本（测试专用 fake 工具）。
func fakeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnabled(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("全空应关闭")
	}
	if !(Config{GitleaksBin: "/x/gitleaks"}).Enabled() {
		t.Fatal("配 gitleaks 应开启")
	}
	if !(Config{SemgrepBin: "/x/semgrep", SemgrepConfig: "p/go"}).Enabled() {
		t.Fatal("配 semgrep 应开启")
	}
}

func TestGitleaksScan(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "gitleaks", `
while [ $# -gt 0 ]; do
  if [ "$1" = "--report-path" ]; then
    printf '%s' '[{"RuleID":"aws-access-key-id","Description":"AWS Access Key","File":"cmd/main.go","StartLine":42,"Secret":"AKIA..."}]' > "$2"
  fi
  shift
done
exit 0
`)
	outcomes := Run(context.Background(),
		Config{GitleaksBin: bin, Timeout: 30 * time.Second}, dir, "main", nil, nopLogger{})
	if len(outcomes) != 1 {
		t.Fatalf("应产出 1 个 outcome: %d", len(outcomes))
	}
	o := outcomes[0]
	if o.Err != nil || o.SkippedReason != "" {
		t.Fatalf("不应失败: %v %s", o.Err, o.SkippedReason)
	}
	if len(o.Result.Findings) != 1 {
		t.Fatalf("findings = %+v", o.Result.Findings)
	}
	f := o.Result.Findings[0]
	if f.By != "gitleaks" || f.Severity != reportutil.High ||
		f.File != "cmd/main.go" || f.Line != 42 {
		t.Fatalf("归一错误: %+v", f)
	}
	if !strings.Contains(f.Comment, "aws-access-key-id") {
		t.Fatalf("评论应含规则名: %q", f.Comment)
	}
	// 红线：密钥原文绝不进评论（防泄漏进 PR）
	if strings.Contains(f.Comment, "AKIA") {
		t.Fatalf("密钥原文不得出现在意见中: %q", f.Comment)
	}
}

func TestSemgrepScan(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "semgrep", `printf '%s' '{"results":[{"check_id":"go.lang.correct","path":"internal/x/a.go","start":{"line":7,"col":1},"extra":{"severity":"WARNING","message":"err not checked"}}]}'`)
	outcomes := Run(context.Background(),
		Config{SemgrepBin: bin, SemgrepConfig: "p/go", Timeout: 30 * time.Second},
		dir, "", []string{"internal/x/a.go", "internal/x/b.go"}, nopLogger{})
	if len(outcomes) != 1 || outcomes[0].Err != nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	f := outcomes[0].Result.Findings[0]
	if f.File != "internal/x/a.go" || f.Line != 7 || f.Severity != reportutil.Medium {
		t.Fatalf("归一错误: %+v", f)
	}
	if !strings.Contains(f.Comment, "err not checked") {
		t.Fatalf("评论应含 semgrep message: %q", f.Comment)
	}
}

// semgrep 聚焦路径整形（v3.21.0）：去重/去空/防路径穿越/超限回退全仓。
func TestSemgrepPaths(t *testing.T) {
	focused := semgrepPaths([]string{"a.go", " a.go ", "", "b.go", "../escape.go"})
	if len(focused) != 2 || focused[0] != "a.go" || focused[1] != "b.go" {
		t.Fatalf("整形错误: %v", focused)
	}
	if semgrepPaths(nil) != nil {
		t.Fatal("空清单应回退全仓")
	}
	many := make([]string, semgrepMaxPaths+1)
	for i := range many {
		many[i] = "f.go"
	}
	if semgrepPaths(many) != nil {
		t.Fatal("超限应回退全仓")
	}
}

// semgrep bin 配了但 config 未配：不产 outcome（不算覆盖缺口），只告警。
func TestSemgrepNoConfigSkipped(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "semgrep", `exit 0`)
	outcomes := Run(context.Background(),
		Config{SemgrepBin: bin, Timeout: 30 * time.Second}, dir, "", nil, nopLogger{})
	if len(outcomes) != 0 {
		t.Fatalf("未配 config 不应产出 outcome: %+v", outcomes)
	}
}

func TestToolFailureOutcome(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "gitleaks", `echo "boom" >&2; exit 1`)
	outcomes := Run(context.Background(),
		Config{GitleaksBin: bin, Timeout: 30 * time.Second}, dir, "main", nil, nopLogger{})
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("失败应产出带 Err 的 outcome: %+v", outcomes)
	}
}

func TestFindingsCapped(t *testing.T) {
	dir := t.TempDir()
	// 生成 60 条 findings 的 JSON
	var items []string
	for i := 0; i < 60; i++ {
		items = append(items, `{"RuleID":"r","Description":"d","File":"a.go","StartLine":1}`)
	}
	json := `[` + strings.Join(items, ",") + `]`
	bin := fakeBin(t, dir, "gitleaks", strings.ReplaceAll(`
while [ $# -gt 0 ]; do
  if [ "$1" = "--report-path" ]; then printf '%s' '@JSON@' > "$2"; fi
  shift
done
exit 0
`, "@JSON@", json))
	outcomes := Run(context.Background(),
		Config{GitleaksBin: bin, Timeout: 30 * time.Second}, dir, "", nil, nopLogger{})
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d", len(outcomes))
	}
	if len(outcomes[0].Result.Findings) != maxFindingsPerTool {
		t.Fatalf("findings 应封顶 %d, got %d", maxFindingsPerTool, len(outcomes[0].Result.Findings))
	}
	if !strings.Contains(outcomes[0].Result.Notes, "截断") {
		t.Fatalf("封顶应留痕 Notes: %q", outcomes[0].Result.Notes)
	}
}

// gitleaks 命令行构造：带 baseRef 传 --log-opts（diff 范围扫描）+ redact/exit-code 防线。
func TestGitleaksArgs(t *testing.T) {
	withBase := strings.Join(gitleaksArgs("/repo", "origin/main..HEAD"), " ")
	if !strings.Contains(withBase, "--log-opts origin/main..HEAD") {
		t.Fatalf("缺 --log-opts: %s", withBase)
	}
	if !strings.Contains(withBase, "--redact") || !strings.Contains(withBase, "--exit-code 0") {
		t.Fatalf("缺 redact/exit-code 防线: %s", withBase)
	}
	withoutBase := strings.Join(gitleaksArgs("/repo", ""), " ")
	if strings.Contains(withoutBase, "--log-opts") {
		t.Fatal("无 baseRef 不应传 --log-opts")
	}
}

// gitleaksJSON 空数组：零 findings 不算失败。
func TestGitleaksCleanReport(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "gitleaks", `
while [ $# -gt 0 ]; do
  if [ "$1" = "--report-path" ]; then printf '%s' '[]' > "$2"; fi
  shift
done
exit 0
`)
	outcomes := Run(context.Background(),
		Config{GitleaksBin: bin, Timeout: 30 * time.Second}, dir, "", nil, nopLogger{})
	if len(outcomes) != 1 || outcomes[0].Err != nil || len(outcomes[0].Result.Findings) != 0 {
		t.Fatalf("干净报告应产出空 findings: %+v", outcomes)
	}
}
