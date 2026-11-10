package argusctx

import (
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

// TestDiffPrep_SingleFileLimit_Scales 单文件上限随总量缩放。
func TestDiffPrep_SingleFileLimit_Scales(t *testing.T) {
	if got := singleFileLimit(0); got != SingleFileMaxChars {
		t.Fatalf("total=0: want %d, got %d", SingleFileMaxChars, got)
	}
	if got := singleFileLimit(1_400_000); got != 28_000 {
		t.Fatalf("total=1.4M: want 28000, got %d", got)
	}
	if got := singleFileLimit(50_000); got != SingleFileMaxChars {
		t.Fatalf("total=50k: want %d, got %d", SingleFileMaxChars, got)
	}
}

// TestDiffPrep_SmallDiff_TotalNotTruncated 小 diff 不截断、无 Truncated 标记。
func TestDiffPrep_SmallDiff_TotalNotTruncated(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "a.go", Diff: strings.Repeat("x", 100)},
	}}
	dc := Prepare(md, 1_000_000)
	if dc.Truncated {
		t.Fatal("小 diff 不应 Truncated")
	}
	if len(dc.Files) != 1 || !dc.Files[0].Included {
		t.Fatal("a.go 应 Included")
	}
}

// TestDiffPrep_LargeTotal_TotalTruncated 总量超限截断 + Truncated 标记。
func TestDiffPrep_LargeTotal_TotalTruncated(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "a.go", Diff: strings.Repeat("x", 8000)},
		{NewPath: "b.go", Diff: strings.Repeat("y", 8000)},
	}}
	dc := Prepare(md, 10_000) // 总量10k，两个8k文件第一个吃掉大部分
	if !dc.Truncated {
		t.Fatal("总量超限应 Truncated")
	}
	// a.go Included 全量，b.go 剩余 2000 rune → 截断后 Included
	if !dc.Files[0].Included {
		t.Fatal("a.go 应 Included")
	}
	if !dc.Files[1].Included {
		t.Fatal("b.go 应 Included（截断后仍有剩余空间）")
	}
	// 总文本应接近 10000 rune（a.go 8000 + b.go 截断到 ~2000）
	if len([]rune(dc.Text)) > 11000 {
		t.Fatalf("总文本应 ~10000 rune，实际 %d", len([]rune(dc.Text)))
	}
}

// TestDiffPrep_NoiseFiltered 噪声文件不进文本但保留元信息。
func TestDiffPrep_NoiseFiltered(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "go.sum", Diff: "should not appear"},
		{NewPath: "vendor/foo.go", Diff: "should not appear"},
		{NewPath: "a.min.js", Diff: "should not appear"},
		{NewPath: "real.go", Diff: "real diff"},
	}}
	dc := Prepare(md, 0)
	if strings.Contains(dc.Text, "should not appear") {
		t.Fatal("噪声文件不应进 Text")
	}
	if !strings.Contains(dc.Text, "real diff") {
		t.Fatal("real.go 的 diff 应保留")
	}
	for _, f := range dc.Files {
		if f.Path == "go.sum" || f.Path == "vendor/foo.go" || f.Path == "a.min.js" {
			if f.Included {
				t.Fatalf("噪声 %s 不应 Included", f.Path)
			}
		}
	}
}

// TestDiffPrep_FillFromRawDiff 后端只给 RawDiff 时自动回填各文件。
func TestDiffPrep_FillFromRawDiff(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ +1 @@\n+line1\ndiff --git a/b.go b/b.go\n+++ b/b.go\n@@ +1 @@\n+line2\n"
	md := &provider.MergeDiff{
		Files: []*provider.ChangedFile{
			{NewPath: "a.go"},
			{NewPath: "b.go"},
		},
		RawDiff: raw,
	}
	dc := Prepare(md, 0)
	if !strings.Contains(dc.Text, "+line1") || !strings.Contains(dc.Text, "+line2") {
		t.Fatalf("RawDiff 回填失败: %s", dc.Text[:100])
	}
}

// TestDiffPrep_SingleFileTruncated 单文件超限截断。
func TestDiffPrep_SingleFileTruncated(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "big.go", Diff: strings.Repeat("x", SingleFileMaxChars+1000)},
	}}
	dc := Prepare(md, 0)
	if !strings.Contains(dc.Text, "截断") {
		t.Fatal("大文件应截断并标注")
	}
	if len(dc.Text) > SingleFileMaxChars+200 { // 加上截断标记的容差
		t.Fatalf("截断后长度异常: %d", len(dc.Text))
	}
}

// TestDiffPrep_Binary 二进制文件不进文本但元信息保留。
func TestDiffPrep_Binary(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "img.png", IsBinary: true},
		{NewPath: "a.go", Diff: "code"},
	}}
	dc := Prepare(md, 0)
	if strings.Contains(dc.Text, "img.png") {
		t.Fatal("二进制不应进文本")
	}
	if len(dc.Files) != 2 {
		t.Fatal("二进制应保留元信息")
	}
}
