package codesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree 构造测试工作副本：a/x.go 与 b/y.py 有可命中内容，vendor/.git/
// testdata 里有应被排除的同名命中，big.bin 是二进制，large.txt 超单文件上限。
func writeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a/x.go":            "package a\n\nfunc caller() { helper() }\n",
		"b/y.py":            "def caller():\n    helper()\n",
		"b/nested/z.js":     "function caller() { helper(); }\n",
		"vendor/v.go":       "func caller() {}\n",
		".git/internal.cfg": "caller=1\n",
		"testdata/t.txt":    "caller here\n",
		"big.bin":           "caller\x00binary\n",
		"plain.txt":         "nothing relevant\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 超单文件上限的大文件（全大文件不产命中，只验证被跳过计数）
	big := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", int(DefaultMaxFileBytes)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSearchBasic(t *testing.T) {
	dir := writeTree(t)
	res, err := Search(context.Background(), dir, `caller\(`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range res.Hits {
		got = append(got, h.File+":"+h.Text)
	}
	want := []string{"a/x.go:func caller() { helper() }", "b/nested/z.js:function caller() { helper(); }", "b/y.py:def caller():"}
	if len(got) != 3 || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("命中集不符（字典序 + 排除目录）:\n got=%v\nwant=%v", got, want)
	}
	// Scanned 只计实际做了正则扫描的文本文件（big.bin 二进制跳过计入 Skipped，
	// large.txt 超单文件上限计入 Skipped，vendor/.git/testdata 在目录层排除不计数）
	if res.Truncated || res.Scanned != 4 || res.Skipped != 2 {
		t.Fatalf("计数不符: truncated=%v scanned=%d skipped=%d", res.Truncated, res.Scanned, res.Skipped)
	}
}

func TestSearchGlob(t *testing.T) {
	dir := writeTree(t)
	// 按文件名（无 /）
	res, err := Search(context.Background(), dir, "caller", Options{Globs: []string{"*.py"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].File != "b/y.py" {
		t.Fatalf("文件名 glob 过滤失效: %+v", res.Hits)
	}
	// 按相对路径（含 /）
	res, err = Search(context.Background(), dir, "caller", Options{Globs: []string{"b/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].File != "b/y.py" {
		t.Fatalf("路径 glob 过滤失效: %+v", res.Hits)
	}
	// 非法 glob
	if _, err := Search(context.Background(), dir, "x", Options{Globs: []string{"["}}); err == nil {
		t.Fatal("非法 glob 应报错")
	}
}

func TestSearchIgnoreCaseAndCaps(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("CallerOne()\n", 10)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), dir, `callerone\(\)`, Options{IgnoreCase: true, MaxHits: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 || !res.Truncated {
		t.Fatalf("MaxHits 截断失效: hits=%d truncated=%v", len(res.Hits), res.Truncated)
	}
	out := res.Render()
	if !strings.Contains(out, "已达上限") {
		t.Fatalf("截断注记缺失: %s", out)
	}

	// 输出 rune 上限截断（Render 层；单条命中恒展示，需 ≥2 条才触发）
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("zzzz\nzzzz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = Search(context.Background(), dir, "zzzz", Options{MaxOutputRuns: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Render(), "输出已达上限") {
		t.Fatalf("输出上限截断失效: %s", res.Render())
	}
}

func TestSearchErrorsAndEmpty(t *testing.T) {
	dir := writeTree(t)
	if _, err := Search(context.Background(), dir, "  ", Options{}); err == nil {
		t.Fatal("空 pattern 应报错")
	}
	if _, err := Search(context.Background(), dir, strings.Repeat("a", maxPatternLen+1), Options{}); err == nil {
		t.Fatal("超长 pattern 应报错")
	}
	if _, err := Search(context.Background(), dir, "(", Options{}); err == nil {
		t.Fatal("非法正则应报错")
	}
	if _, err := Search(context.Background(), filepath.Join(dir, "nope"), "x", Options{}); err == nil {
		t.Fatal("根不存在应报错")
	}
	res, err := Search(context.Background(), dir, `绝对不存在的模式xyz`, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || !strings.Contains(res.Render(), "无命中") {
		t.Fatalf("零命中渲染不符: %+v", res.Hits)
	}
}

func TestSearchHitTextTruncation(t *testing.T) {
	dir := t.TempDir()
	long := "caller " + strings.Repeat("字", 500)
	if err := os.WriteFile(filepath.Join(dir, "l.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), dir, "caller", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || len([]rune(res.Hits[0].Text)) > hitTextMaxRunes+1 {
		t.Fatalf("命中行截断失效: %d runes", len([]rune(res.Hits[0].Text)))
	}
}
