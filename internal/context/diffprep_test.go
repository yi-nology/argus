package context

import (
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

func diffFile(path, text string, adds int) *provider.ChangedFile {
	return &provider.ChangedFile{OldPath: path, NewPath: path, Diff: text, Additions: adds}
}

func TestPrepareCleaning(t *testing.T) {
	md := &provider.MergeDiff{
		Files: []*provider.ChangedFile{
			diffFile("go.sum", "diff --git a/go.sum b/go.sum\n+lots", 100),
			diffFile("vendor/x/y.go", "+vendor code", 5),
			diffFile("proto/gen.pb.go", "+generated", 5),
			diffFile("main.go", "+package main\n+func main() {}", 2),
			diffFile("logo.png", "", 0),
		},
	}
	md.Files[4].IsBinary = true

	dc := Prepare(md, 100000)
	paths := map[string]struct {
		Included bool
		IsBinary bool
	}{}
	for _, f := range dc.Files {
		paths[f.Path] = struct {
			Included bool
			IsBinary bool
		}{f.Included, f.IsBinary}
	}

	if paths["go.sum"].Included {
		t.Fatal("lockfile 不应进入 diff 文本")
	}
	if paths["vendor/x/y.go"].Included {
		t.Fatal("vendor 不应进入 diff 文本")
	}
	if paths["proto/gen.pb.go"].Included {
		t.Fatal("生成物不应进入 diff 文本")
	}
	if paths["logo.png"].Included || !paths["logo.png"].IsBinary {
		t.Fatal("二进制不进入文本但保留 IsBinary 元信息")
	}
	if !paths["main.go"].Included {
		t.Fatal("正常文件应进入 diff 文本")
	}
	if !strings.Contains(dc.Text, "package main") {
		t.Fatal("diff 文本缺正常文件内容")
	}
	if strings.Contains(dc.Text, "vendor code") {
		t.Fatal("diff 文本混入 vendor 内容")
	}
	if dc.Truncated {
		t.Fatal("未超限不应标记截断")
	}
	// 元信息保留全部文件
	if len(dc.Files) != 5 {
		t.Fatalf("Files 元信息应含全部 5 个文件，got %d", len(dc.Files))
	}
}

func TestPrepareTotalTruncation(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 1000; i++ {
		big.WriteString("+line of code that matters\n")
	}
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", big.String(), 1000),
		diffFile("b.go", big.String(), 1000),
	}}
	dc := Prepare(md, 500)
	if !dc.Truncated {
		t.Fatal("超限应标记 Truncated")
	}
	// 截断说明（被排除文件清单）会附加在文本尾部，放宽为"主体 500 + 说明 < 900"
	if len(dc.Text) > 1400 {
		t.Fatalf("总量截断未生效: %d", len(dc.Text))
	}
	if !strings.Contains(dc.Text, "截断说明") || !strings.Contains(dc.Text, "- b.go") {
		t.Fatal("被预算排除的文件应在截断说明中留痕")
	}
}

func TestPrepareSingleFileTruncation(t *testing.T) {
	var big strings.Builder
	for i := 0; i < SingleFileMaxChars+100; i++ {
		big.WriteString("x")
	}
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{diffFile("a.go", big.String(), 1)}}
	dc := Prepare(md, 0) // 0 = 不限总量
	if len(dc.Text) >= SingleFileMaxChars+100 {
		t.Fatal("单文件截断未生效")
	}
	if !strings.Contains(dc.Text, "截断") {
		t.Fatal("缺截断标记")
	}
}

// v3.0.1 真实试点回归：gitea 后端 ChangedFile.Diff 为空、内容在 RawDiff → 按文件切块回填。
func TestPrepareFillFromRawDiff(t *testing.T) {
	raw := "diff --git a/login.go b/login.go\nnew file mode 100644\n--- /dev/null\n+++ b/login.go\n@@ -0,0 +1,3 @@\n+package auth\n+\n+func login() {}\n" +
		"diff --git a/go.sum b/go.sum\n--- /dev/null\n+++ b/go.sum\n@@ -0,0 +1,1 @@\n+huge lockfile\n"
	md := &provider.MergeDiff{
		Files: []*provider.ChangedFile{
			{NewPath: "login.go", Additions: 3},
			{NewPath: "go.sum", Additions: 1},
		},
		RawDiff: raw,
	}
	dc := Prepare(md, 100000)
	got := map[string]struct {
		Included bool
		IsBinary bool
	}{}
	for _, f := range dc.Files {
		got[f.Path] = struct {
			Included bool
			IsBinary bool
		}{f.Included, f.IsBinary}
	}
	if !got["login.go"].Included {
		t.Fatal("login.go 应被回填并进入文本")
	}
	if !strings.Contains(dc.Text, "func login()") {
		t.Fatalf("diff 文本缺 RawDiff 切块内容:\n%s", dc.Text)
	}
	if got["go.sum"].Included {
		t.Fatal("go.sum 是噪声，切块后仍应被剔除")
	}
}

// --- PrepareIncremental 测试 ---

func TestPrepareIncremental_NoLastDiff_FullReview(t *testing.T) {
	// lastDiffText 为空 → 等价于全量审查
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", "diff --git a/a.go b/a.go\n+++ b/a.go\n+line1\n", 1),
		diffFile("b.go", "diff --git a/b.go b/b.go\n+++ b/b.go\n+line2\n", 1),
	}}
	dc := PrepareIncremental(md, "", 100000)
	if dc.NoNewChanges {
		t.Fatal("首次审查不应标记 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "+line1") || !strings.Contains(dc.Text, "+line2") {
		t.Fatal("首次审查应包含全部文件 diff")
	}
	for _, f := range dc.Files {
		if f.AlreadyReviewed {
			t.Fatalf("首次审查不应有 AlreadyReviewed 文件: %s", f.Path)
		}
	}
}

func TestPrepareIncremental_IdenticalDiff_NoNewChanges(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n+line1\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n+line2\n"
	lastDiff := diffA + "\n" + diffB

	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", diffA, 1),
		diffFile("b.go", diffB, 1),
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if !dc.NoNewChanges {
		t.Fatal("diff 完全一致应标记 NoNewChanges")
	}
	if dc.Text != "" {
		t.Fatalf("NoNewChanges 时 Text 应为空，got: %q", dc.Text)
	}
	for _, f := range dc.Files {
		if !f.AlreadyReviewed {
			t.Fatalf("文件 %s 应标记 AlreadyReviewed", f.Path)
		}
	}
}

func TestPrepareIncremental_PartialChange(t *testing.T) {
	// diff 超过 minIncrementalChars(100) 以避免回退全量审查
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-old line that is long enough to exceed one hundred characters for the incremental test\n+replaced with a new line\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n@@ -1,3 +1,3 @@\n package util\n-func old() { return }\n+func New() { return }\n"
	lastDiff := diffA + "\n" + diffB

	// a.go 有变化，b.go 不变
	newDiffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-old line that changed again in this push\n+final replacement line\n"
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", newDiffA, 1),
		diffFile("b.go", diffB, 1),
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有文件变化不应标记 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "final replacement") {
		t.Fatal("变化文件的 diff 应进入 Text")
	}
	if strings.Contains(dc.Text, "func New()") {
		t.Fatal("未变化文件的 diff 不应进入 Text")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 有变化不应 AlreadyReviewed")
	}
	if !filesByName["b.go"].AlreadyReviewed {
		t.Fatal("b.go 未变化应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_NewFile(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-func old() { return }\n+func main() { fmt.Println() }\n"
	lastDiff := diffA

	// 新增 c.go（diff 超过 minIncrementalChars=100）
	diffC := "diff --git a/c.go b/c.go\n+++ b/c.go\n@@ -0,0 +1,3 @@\n+package util\n+func NewHelper() error {\n+    return nil\n+}\n"
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", diffA, 1),
		diffFile("c.go", diffC, 1),
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有新文件不应 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "NewHelper") {
		t.Fatal("新文件 diff 应进入 Text")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if !filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 未变化应 AlreadyReviewed")
	}
	if filesByName["c.go"].AlreadyReviewed {
		t.Fatal("c.go 是新文件不应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_DeletedFile(t *testing.T) {
	diffA := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package main\n-func old() { return }\n+func main() { fmt.Println() }\n"
	diffB := "diff --git a/b.go b/b.go\n+++ b/b.go\n@@ -1,3 +1,3 @@\n package util\n-func helper() error {\n+func Helper() error {\n    return nil\n}\n"
	lastDiff := diffA + "\n" + diffB

	// b.go 被删除（deleted diff 超过 minIncrementalChars）
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", diffA, 1),
		{OldPath: "b.go", NewPath: "", IsDeleted: true, Diff: "diff --git a/b.go b/b.go\ndeleted file mode 100644\n--- a/b.go\n+++ /dev/null\n@@ -1,5 +0,0 @@\n-package util\n-func helper() error {\n-    return nil\n-}\n"},
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("有文件删除不应 NoNewChanges")
	}
	filesByName := map[string]plugin.DiffFile{}
	for _, f := range dc.Files {
		filesByName[f.Path] = f
	}
	if !filesByName["a.go"].AlreadyReviewed {
		t.Fatal("a.go 未变化应 AlreadyReviewed")
	}
	if filesByName["b.go"].AlreadyReviewed {
		t.Fatal("b.go 被删除不应 AlreadyReviewed")
	}
}

func TestPrepareIncremental_SmallDiffFallback(t *testing.T) {
	// 增量 diff 太短（< 100 字符）→ 回退全量审查
	lastDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+old\n"
	newDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+x\n" // ~40 chars

	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", newDiff, 1),
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	// 回退全量后，NoNewChanges 应为 false，Text 应有内容
	if dc.NoNewChanges {
		t.Fatal("回退全量不应 NoNewChanges")
	}
	if dc.Text == "" {
		t.Fatal("回退全量后 Text 不应为空")
	}
	// 回退全量时 AlreadyReviewed 应全为 false
	for _, f := range dc.Files {
		if f.AlreadyReviewed {
			t.Fatalf("回退全量后不应有 AlreadyReviewed: %s", f.Path)
		}
	}
}

func TestPrepareIncremental_NoiseFiltered(t *testing.T) {
	// 噪声文件即使有变化也不进文本
	lastGoSum := "diff --git a/go.sum b/go.sum\n+++ b/go.sum\n+old\n"
	lastDiff := lastGoSum

	newGoSum := "diff --git a/go.sum b/go.sum\n+++ b/go.sum\n+new\n"
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("go.sum", newGoSum, 1),
	}}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if strings.Contains(dc.Text, "go.sum") {
		t.Fatal("噪声文件不应进 Text")
	}
	// 噪声文件不标记 AlreadyReviewed（走 noise 分支）
	for _, f := range dc.Files {
		if f.Path == "go.sum" && f.AlreadyReviewed {
			t.Fatal("噪声文件不应标记 AlreadyReviewed")
		}
	}
}

func TestPrepareIncremental_RawDiffBackfill(t *testing.T) {
	// ChangedFile.Diff 为空，内容在 RawDiff → 自动回填
	lastDiff := "diff --git a/a.go b/a.go\n+++ b/a.go\n+old line\n"
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ +1 @@\n+new line\n"

	md := &provider.MergeDiff{
		Files:   []*provider.ChangedFile{{NewPath: "a.go", Additions: 1}},
		RawDiff: raw,
	}
	dc := PrepareIncremental(md, lastDiff, 100000)
	if dc.NoNewChanges {
		t.Fatal("RawDiff 回填后有变化不应 NoNewChanges")
	}
	if !strings.Contains(dc.Text, "+new line") {
		t.Fatal("RawDiff 回填内容应进 Text")
	}
}

func TestFillFromRawDiff(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ +1 @@\n+line1\ndiff --git b/b.go b/b.go\n+++ b/b.go\n@@ +1 @@\n+line2\n"
	files := []*provider.ChangedFile{{NewPath: "a.go"}, {NewPath: "b.go"}}
	out := fillFromRawDiff(files, raw)
	if len(out) != 2 {
		t.Fatalf("应回填 2 个文件，got %d", len(out))
	}
	// 存储值不带 "diff --git " 前缀（与 ChangedFile.Diff 形态一致，读写两端契约）
	if strings.HasPrefix(out[0].Diff, "diff --git ") || !strings.Contains(out[0].Diff, "+line1") {
		t.Fatalf("a.go 回填错误: %q", out[0].Diff)
	}
	if !strings.Contains(out[1].Diff, "+line2") {
		t.Fatalf("b.go 回填错误: %q", out[1].Diff)
	}
	// 不改动调用方切片（浅拷贝语义）
	if files[0].Diff != "" {
		t.Fatalf("不得改写调用方原文件: %q", files[0].Diff)
	}
}

func TestFillFromRawDiff_Empty(t *testing.T) {
	files := []*provider.ChangedFile{{NewPath: "a.go"}}
	if out := fillFromRawDiff(files, ""); out[0].Diff != "" {
		t.Fatalf("空 RawDiff 不应回填, got %q", out[0].Diff)
	}
}

// 深挖修复回归：目录噪声规则按路径段匹配，"cargo-build/"、"ui-dist/" 不再被
// "build/"、"dist/" 的无边界子串匹配误伤。
func TestNoiseSegmentMatch(t *testing.T) {
	noiseCases := map[string]bool{
		"a/build/b.go":      true,  // 目录段命中
		"vendor/x/y.go":     true,  // 顶层 vendor
		"cmd/a/vendor/x.go": true,  // 嵌套 vendor
		"ui-dist/x.go":      false, // 子串误伤修复
		"cargo-build/x.go":  false, // 子串误伤修复
		"myvendor/x.go":     false, // 子串误伤修复
		"go.sum":            true,  // 文件名精确匹配
		"x.pb.go":           true,  // 后缀匹配
		"distillary/d/a.go": false, // 目录段不等同前缀
	}
	for p, want := range noiseCases {
		if got := noise(p); got != want {
			t.Errorf("noise(%q) = %v, want %v", p, got, want)
		}
	}
}

// 深挖修复回归：噪声/二进制文件不进截断说明（它们是按规则排除，不是预算截断）。
func TestTruncationNoteExcludesNoise(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 1000; i++ {
		big.WriteString("+code\n")
	}
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		diffFile("a.go", big.String(), 1000),
		diffFile("b.go", big.String(), 1000),
		diffFile("go.sum", big.String(), 1000),
	}}
	dc := Prepare(md, 500)
	if !dc.Truncated {
		t.Fatal("应标记 Truncated")
	}
	if strings.Contains(dc.Text, "- go.sum") {
		t.Fatal("噪声文件不应出现在截断说明里")
	}
	if !strings.Contains(dc.Text, "- b.go") {
		t.Fatalf("预算排除的 b.go 应在截断说明中，文本尾:\n%s", dc.Text[len(dc.Text)-400:])
	}
}
