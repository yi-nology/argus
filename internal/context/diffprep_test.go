package context

import (
	"strings"
	"testing"

	"github.com/yi-nology/git-platform-sdk/provider"
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
	if len(dc.Text) > 600 {
		t.Fatalf("总量截断未生效: %d", len(dc.Text))
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
