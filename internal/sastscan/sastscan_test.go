package sastscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/textx/reportutil"
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

// 门面映射契约：SDK Scan 结果 → plugin.Outcome（Name/Findings/By 映射 + 密钥
// 红线在端到端仍可见）。SDK 侧行为测试（解析/封顶/聚焦/参数构造）在 oagentkit/tools/sast。
func TestRunMapsToOutcomes(t *testing.T) {
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
	if o.Err != nil {
		t.Fatalf("不应失败: %v", o.Err)
	}
	if o.Name != "gitleaks" || o.Manifest.Kind != "cli" {
		t.Fatalf("清单映射错误: %+v", o.Manifest)
	}
	if len(o.Result.Findings) != 1 {
		t.Fatalf("findings = %+v", o.Result.Findings)
	}
	f := o.Result.Findings[0]
	if f.By != "gitleaks" || f.Severity != reportutil.High ||
		f.File != "cmd/main.go" || f.Line != 42 {
		t.Fatalf("归一错误: %+v", f)
	}
	// 红线：密钥原文绝不进评论（防泄漏进 PR）
	if strings.Contains(f.Comment, "AKIA") {
		t.Fatalf("密钥原文不得出现在意见中: %q", f.Comment)
	}
}

func TestRunToolFailureOutcome(t *testing.T) {
	dir := t.TempDir()
	bin := fakeBin(t, dir, "gitleaks", `echo "boom" >&2; exit 1`)
	outcomes := Run(context.Background(),
		Config{GitleaksBin: bin, Timeout: 30 * time.Second}, dir, "main", nil, nopLogger{})
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("失败应产出带 Err 的 outcome: %+v", outcomes)
	}
}

func TestRunDisabled(t *testing.T) {
	if out := Run(context.Background(), Config{}, "/tmp", "", nil, nopLogger{}); out != nil {
		t.Fatalf("未配置应产出 nil: %+v", out)
	}
}
