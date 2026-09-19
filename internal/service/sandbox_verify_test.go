package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractGoTestFiles(t *testing.T) {
	gen := "### internal/x/a_test.go\n\n```go\npackage x\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n```\n\n### b.go\n\n```go\npackage x\n```\n\n### internal/x/a_test.go\n\n```go\npackage dup\n```\n"
	files := extractGoTestFiles(gen)
	if len(files) != 1 {
		t.Fatalf("应只收 _test.go 且去重: %v", files)
	}
	code, ok := files["internal/x/a_test.go"]
	if !ok || !strings.Contains(code, "TestA") {
		t.Fatalf("提取错误: %q", code)
	}
	// 非法路径拒收
	if files := extractGoTestFiles("### ../evil_test.go\n\n```go\nx\n```"); len(files) != 0 {
		t.Fatalf("路径穿越应拒收: %v", files)
	}
	// 非法路径拒收
	if files := extractGoTestFiles("### /abs/x_test.go\n\n```go\nx\n```"); len(files) != 0 {
		t.Fatalf("绝对路径应拒收: %v", files)
	}
}

func TestSanitizeTestPath(t *testing.T) {
	cases := map[string]string{
		"internal/x/a_test.go": "internal/x/a_test.go",
		"`internal/x/a_test.go`": "internal/x/a_test.go",
		"- internal/x/a_test.go": "internal/x/a_test.go",
		"../evil_test.go":        "",
		"/abs_test.go":           "",
		"a.go":                   "",
		"":                       "",
	}
	for in, want := range cases {
		if got := sanitizeTestPath(in); got != want {
			t.Fatalf("sanitize(%q)=%q want %q", in, got, want)
		}
	}
}

// 包装命令真跑：fake 命令检查写入的测试文件存在（go build 语义的替身）。
func TestVerifyGenerated(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module app\n\ngo 1.22\n")
	cfg := VerifyConfig{Cmd: `test -f {dir}/internal/x/a_test.go && echo found`, Timeout: 10 * time.Second}
	results := VerifyGenerated(context.Background(), cfg, dir,
		map[string]string{"internal/x/a_test.go": "package x\n"})
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	// 失败路径：包装命令报错 → 文件被清理（工作副本不留 argus 产物）。
	// 用独立文件名（success 用例已写入 a_test.go，同名会走 skip 分支）。
	cfgFail := VerifyConfig{Cmd: `exit 1`, Timeout: 10 * time.Second}
	results = VerifyGenerated(context.Background(), cfgFail, dir,
		map[string]string{"internal/x/b_test.go": "package x\n"})
	if len(results) != 1 || results[0].OK {
		t.Fatalf("失败应标记: %+v", results)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal/x/b_test.go")); !os.IsNotExist(err) {
		t.Fatal("失败后写入的测试文件应被清理")
	}
	// 已存在同名文件：跳过不覆盖
	existing := filepath.Join(dir, "internal/x/y_test.go")
	mustWriteFile(t, existing, "package x // user code")
	results = VerifyGenerated(context.Background(), cfg, dir,
		map[string]string{"internal/x/y_test.go": "package x // generated"})
	if results[0].Skipped == "" || results[0].OK {
		t.Fatalf("已存在应跳过: %+v", results)
	}
	if strings.Contains(mustReadFile(t, existing), "generated") {
		t.Fatal("用户文件不得被覆盖")
	}
}

func TestRenderVerifySection(t *testing.T) {
	ok := renderVerifySection([]VerifyResult{{Path: "a_test.go", OK: true}})
	if !strings.Contains(ok, "全部通过") || !strings.Contains(ok, "a_test.go") {
		t.Fatalf("全过段错误: %q", ok)
	}
	fail := renderVerifySection([]VerifyResult{{Path: "a_test.go", OK: false, Output: "boom"}})
	if !strings.Contains(fail, "验证失败") || !strings.Contains(fail, "boom") {
		t.Fatalf("失败段错误: %q", fail)
	}
	skip := renderVerifySection([]VerifyResult{{Path: "a_test.go", Skipped: "已存在"}})
	if !strings.Contains(skip, "已存在") {
		t.Fatalf("跳过段错误: %q", skip)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
