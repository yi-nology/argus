package evalq_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// TestEvalPipelineStub 装载→评测→报告 全链路（打桩 Reviewer，不联网）：
// CI 常跑的编排语义回归；真实模型评测走 ARGUS_EVAL=1 的 eval-real 面
// （Reviewer 接口装配真实模型——本骨架不内置）。
func TestEvalPipelineStub(t *testing.T) {
	dir := t.TempDir()
	datasetYAML := `language: go
description: 链路回归夹具
cases:
` + strings.Repeat("", 0) + `  - id: go-101
    repo: acme/app
    title: 空 error 吞没
    label: standard
    diff: |
      --- a/main.go
      +++ b/main.go
      @@ -1,3 +1,5 @@
       package main
      +if err != nil {}
    expect:
      verdict: comment
      must_find:
        - kind: error-swallow
          severity: medium
  - id: go-102
    repo: acme/app
    title: 干净 diff
    label: known_fp
    diff: |
      --- a/doc.md
      +++ b/doc.md
      @@ -1 +1,2 @@
      +hello
    expect:
      verdict: approve
`
	if err := os.WriteFile(filepath.Join(dir, "go.yaml"), []byte(datasetYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := evalq.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := evalq.Evaluate(context.Background(), ds.Cases, stubReviewer{}, evalq.Options{
		Model:  "stub",
		Params: map[string]string{"mode": "pipeline-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Overall.VerdictAccuracy != 1 {
		t.Fatalf("打桩全命中: %+v", rep.Overall)
	}
	// TP=1（go-101 命中 error-swallow）+ FP=1（known_fp 样例多报 style-nit）
	if rep.Overall.Confusion.TP != 1 || rep.Overall.Confusion.FP != 1 {
		t.Fatalf("混淆矩阵: %+v", rep.Overall.Confusion)
	}
	if len(rep.ByLanguage) != 1 || rep.ByLanguage[0].Language != "go" {
		t.Fatalf("分语言聚合: %+v", rep.ByLanguage)
	}

	var md bytes.Buffer
	if err := rep.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "go-101") || !strings.Contains(md.String(), "style-nit") {
		t.Fatal("Markdown 逐案明细应含样例与误报类别")
	}

	// artifact 落盘口径与 golden eval 一致：写 eval/results/（gitignore），
	// 目录从仓库根相对定位（本测试 cwd = test/evalq）。
	outDir := filepath.Join("..", "..", "eval", "results")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 打桩样例与真实跑批分名（baseline-sample vs baseline-latest）：同名会让
	// `make eval-baseline` 覆盖真实基线归档（#25 验收交付物），B 线收尾时钉住。
	if err := os.WriteFile(filepath.Join(outDir, "baseline-sample.md"), md.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("打桩跑批报告样例已写 eval/results/baseline-sample.md（artifact 面示例）")
}
