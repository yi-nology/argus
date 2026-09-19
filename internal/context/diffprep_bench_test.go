package context

// 热路径基准（v3.25.1）：Prepare 是每 PR 必经的大文本装配点（rune 截断/噪声
// 过滤/总量预算都在这里），基准立尺给后续优化提供可复现口径。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yi-nology/git-platform-sdk/provider"
)

// benchMergeDiff 合成变更集：n 个 .go 文件、每文件 lines 行新增（单行约 40 字节）。
func benchMergeDiff(n, lines int) *provider.MergeDiff {
	files := make([]*provider.ChangedFile, 0, n)
	for i := 0; i < n; i++ {
		var b strings.Builder
		path := fmt.Sprintf("pkg/file%03d.go", i)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", path, path, path, path, lines, lines)
		for j := 0; j < lines; j++ {
			fmt.Fprintf(&b, "+func benchF%03d_%03d() { x := %d; _ = x }\n", i, j, j)
		}
		files = append(files, &provider.ChangedFile{NewPath: path, Diff: b.String(), Additions: lines})
	}
	return &provider.MergeDiff{Files: files}
}

func benchBytes(md *provider.MergeDiff) int64 {
	var total int64
	for _, f := range md.Files {
		total += int64(len(f.Diff))
	}
	return total
}

// BenchmarkPrepareLarge 无截断大 PR（200 文件 × 60 行 ≈ 0.5MB diff 文本）：
// 走完整噪声过滤 + 逐文件 rune 计数 + Builder 装配。
func BenchmarkPrepareLarge(b *testing.B) {
	md := benchMergeDiff(200, 60)
	b.SetBytes(benchBytes(md))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Prepare(md, 8<<20)
	}
}

// BenchmarkPrepareTruncated 触发总量截断（8MB 预算 → 512KB）：截断说明装配 +
// TruncRunes 逐文件收紧路径。
func BenchmarkPrepareTruncated(b *testing.B) {
	md := benchMergeDiff(200, 60)
	b.SetBytes(benchBytes(md))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Prepare(md, 512<<10)
	}
}
