package context

import (
	"strings"
	"testing"

	"github.com/yi-nology/git-platform-sdk/provider"

	"argus/internal/plugin"
)

// 仓库级过滤（v3.15.0 P0-2）：exclude/include 命中文件不进 Text、保留元信息、
// 不进截断清单、不计入 Truncated。
func TestPrepareRepoExclude(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "internal/api/user.go", Diff: "diff --git a/internal/api/user.go b/internal/api/user.go\n+++ b/internal/api/user.go\n@@ -1,2 +1,3 @@\n+x", Additions: 1},
		{NewPath: "api/generated/types.go", Diff: "diff --git a/api/generated/types.go b/api/generated/types.go\n+++ b/api/generated/types.go\n@@ -1,2 +1,3 @@\n+y", Additions: 1},
	}}
	dc := Prepare(md, 0, PrepareOptions{Exclude: []string{"**/generated/**"}})
	if strings.Contains(dc.Text, "generated/types.go") {
		t.Fatal("被排除文件不应进 diff 文本")
	}
	if !strings.Contains(dc.Text, "user.go") {
		t.Fatal("正常文件应保留")
	}
	var gen *plugin.DiffFile
	for i := range dc.Files {
		if dc.Files[i].Path == "api/generated/types.go" {
			gen = &dc.Files[i]
		}
	}
	if gen == nil || !gen.RepoExcluded || gen.Included {
		t.Fatalf("排除文件元信息错误: %+v", gen)
	}
	if dc.Truncated {
		t.Fatal("仓库排除不应置 Truncated")
	}
	if strings.Contains(dc.Text, "未纳入本次审查") {
		t.Fatal("排除文件不应进截断清单")
	}
}

func TestPrepareRepoIncludeWhitelist(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "internal/x/a.go", Diff: "diff --git a/internal/x/a.go b/internal/x/a.go\n+++ b/internal/x/a.go\n@@ -1 +2 @@\n+a", Additions: 1},
		{NewPath: "cmd/main.go", Diff: "diff --git a/cmd/main.go b/cmd/main.go\n+++ b/cmd/main.go\n@@ -1 +2 @@\n+b", Additions: 1},
	}}
	dc := Prepare(md, 0, PrepareOptions{Include: []string{"internal/**"}})
	if strings.Contains(dc.Text, "cmd/main.go") || !strings.Contains(dc.Text, "internal/x/a.go") {
		t.Fatalf("白名单语义错误:\n%s", dc.Text)
	}
}

func TestPrepareNoOptionsUnchanged(t *testing.T) {
	md := &provider.MergeDiff{Files: []*provider.ChangedFile{
		{NewPath: "a/b.go", Diff: "diff --git a/a/b.go b/a/b.go\n+++ b/a/b.go\n@@ -1 +2 @@\n+x", Additions: 1},
	}}
	dc := Prepare(md, 0)
	if !dc.Files[0].Included || dc.Files[0].RepoExcluded {
		t.Fatalf("无选项行为应与既有一致: %+v", dc.Files[0])
	}
}
