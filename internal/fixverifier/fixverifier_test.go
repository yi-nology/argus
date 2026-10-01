package fixverifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"argus/internal/plugin"
)

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 包装命令：验证目标是「文件能编译」——用 gofmt 代替真实编译（无工具链依赖
	// 差异；命令失败即验证失败）
	cfg := Config{Cmd: "gofmt -l . | grep -q . && exit 1 || exit 0", Timeout: 0}
	findings := []plugin.Finding{
		{File: "main.go", Line: 4, Severity: "low", Comment: "x", SuggestedFix: "\tprintln(\"hello\")"},
		{File: "main.go", Line: 99, SuggestedFix: "\tx"},     // 行越界
		{File: "missing.go", Line: 1, SuggestedFix: "\tx"},   // 文件缺失
		{File: "main.go", Line: 3, Comment: "no fix"},        // 无建议
		{File: "../escape.go", Line: 1, SuggestedFix: "\tx"}, // 路径越界
	}
	results := Verify(context.Background(), cfg, dir, findings)
	if len(results) != len(findings) {
		t.Fatalf("结果数不符: %d", len(results))
	}
	if !results[0].OK || !results[0].Applied {
		t.Fatalf("第 1 条应验证通过: %+v", results[0])
	}
	// 验证后文件必须还原
	raw, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(raw), `"hi"`) || strings.Contains(string(raw), `"hello"`) {
		t.Fatalf("验证后文件应还原: %s", raw)
	}
	for i, want := range []string{"锚定行越界", "文件不在工作副本", "无修复建议", "路径越界"} {
		if results[i+1].OK || results[i+1].Skipped == "" || !strings.Contains(results[i+1].Skipped, want) {
			t.Fatalf("第 %d 条应跳过(%s): %+v", i+1, want, results[i+1])
		}
	}
}

func TestVerifyNoCmd(t *testing.T) {
	results := Verify(context.Background(), Config{}, "/tmp", []plugin.Finding{{SuggestedFix: "x"}})
	if results[0].OK || results[0].Skipped == "" {
		t.Fatalf("未配置命令应整体跳过: %+v", results[0])
	}
}
