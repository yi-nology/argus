// Package evalq_test 数据集装载门禁与打桩跑批回归（P2-02，#25）：
// 基准集 schema 校验在装载器（internal/evalq.LoadDir.Validate）里，本包守
// testdata 夹具形态与「装载→评测→报告」全链路；eval/baseline 起步集（go）
// 的规模/标签/严重度/出处门禁在本包，四语言扩容门禁随 P3-03（#28）并入。
package evalq_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// TestLoadDirSchemaAndRenameGuard 装载器门禁：language 与文件名一致性、
// id 唯一、label/verdict 词表、known_fp/known_miss 结构约束。
func TestLoadDirSchemaAndRenameGuard(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 合法集：go.yaml 两条（standard + known_miss）
	write("go.yaml", `language: go
description: Go 基准
cases:
  - id: go-001
    repo: acme/app
    title: nil 解引用
    label: standard
    diff: |
      --- a/x.go
      +++ b/x.go
    expect:
      verdict: request_changes
      must_find:
        - kind: nil-deref
          severity: high
  - id: go-002
    repo: acme/app
    title: 历史漏检
    label: known_miss
    diff: |
      --- a/y.go
      +++ b/y.go
    expect:
      verdict: comment
      must_find:
        - kind: error-swallow
`)
	ds, err := evalq.LoadDir(dir)
	if err != nil {
		t.Fatalf("合法集装载失败: %v", err)
	}
	if len(ds.Cases) != 2 || ds.Languages()[0] != "go" {
		t.Fatalf("装载结果: %+v", ds.Cases)
	}
	if langs := ds.ByLanguage(); len(langs["go"]) != 2 {
		t.Fatalf("分组: %+v", langs)
	}

	// 非法：文件名与 language 漂移
	write("python.yaml", `language: go
cases: []
`)
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("language 与文件名不一致应报错")
	}

	// 非法：known_fp 带 must_find
	write("python.yaml", `language: python
cases:
  - id: py-001
    label: known_fp
    diff: x
    expect:
      verdict: approve
      must_find:
        - kind: nil-deref
`)
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("known_fp 带 must_find 应报错")
	}

	// 非法：known_miss 无 must_find
	write("python.yaml", `language: python
cases:
  - id: py-001
    label: known_miss
    diff: x
    expect:
      verdict: comment
`)
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("known_miss 无 must_find 应报错")
	}

	// 非法：id 重复
	write("python.yaml", `language: python
cases:
  - id: py-001
    label: standard
    diff: x
    expect: {verdict: comment}
  - id: go-001
    label: standard
    diff: x
    expect: {verdict: comment}
`)
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("id 重复应报错")
	}

	// 非法：verdict 词表外
	write("python.yaml", `language: python
cases:
  - id: py-001
    label: standard
    diff: x
    expect: {verdict: maybe}
`)
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("verdict 词表外应报错")
	}
}

// TestLoadDirEmpty 目录无样例显式报错（空集不是合法基准）。
func TestLoadDirEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := evalq.LoadDir(dir); err == nil {
		t.Fatal("空目录应报错")
	}
}

// TestEvalBaselineStarterSet eval/baseline 起步集门禁（P2-02 #25 交付 3）：
// LoadDir+Validate 装载通过之外，守规模（≥6）、标签混合（standard/known_miss/
// known_fp）、must_find 三档严重度齐、分仓（≥3 仓）与出处（source 如实标注）
// ——合成夹具不得冒充现网已裁决 MR（现网 MR 样例归 P3-03 #28）。
func TestEvalBaselineStarterSet(t *testing.T) {
	ds, err := evalq.LoadDir(filepath.Join("..", "..", "eval", "baseline"))
	if err != nil {
		t.Fatalf("eval/baseline 装载失败: %v", err)
	}
	if len(ds.Cases) < 6 {
		t.Fatalf("起步集应至少 6 条样例，当前 %d", len(ds.Cases))
	}
	labels := map[string]int{}
	sevs := map[string]bool{}
	repos := map[string]bool{}
	for _, c := range ds.Cases {
		labels[c.Label]++
		repos[c.Repo] = true
		if strings.TrimSpace(c.Repo) == "" {
			t.Errorf("样例 %s 缺 repo（分仓维度）", c.ID)
		}
		if strings.TrimSpace(c.Source) == "" {
			t.Errorf("样例 %s 缺 source（出处必须如实标注，合成夹具要写明改写来源）", c.ID)
		}
		if strings.TrimSpace(c.Title) == "" {
			t.Errorf("样例 %s 缺 title", c.ID)
		}
		for _, e := range c.Expect.MustFind {
			if strings.TrimSpace(e.Severity) == "" {
				t.Errorf("样例 %s 的 must_find %q 缺 severity（分严重度聚合面依赖）", c.ID, e.Kind)
				continue
			}
			sevs[strings.ToLower(e.Severity)] = true
		}
	}
	for _, l := range []string{evalq.LabelStandard, evalq.LabelKnownMiss, evalq.LabelKnownFP} {
		if labels[l] == 0 {
			t.Errorf("起步集缺 %s 标签样例（标签混合面）: %v", l, labels)
		}
	}
	for _, s := range []string{"high", "medium", "low"} {
		if !sevs[s] {
			t.Errorf("起步集 must_find 缺 %s 档（严重度覆盖面）: %v", s, sevs)
		}
	}
	if len(repos) < 3 {
		t.Errorf("起步集应覆盖 ≥3 个仓（分仓维度），当前 %d: %v", len(repos), repos)
	}
}

// stubReviewer 确定性打桩 Reviewer（走通 装载→评测→报告 链路）：
// 命中全部金标准 must_find；known_fp 样例额外多报一条 style-nit（误报形态，
// 供报告误报类别断言）。
type stubReviewer struct{}

func (stubReviewer) Review(_ context.Context, c evalq.Case) (evalq.Outcome, error) {
	out := evalq.Outcome{Verdict: c.Expect.Verdict}
	for _, e := range c.Expect.MustFind {
		// Finding 与 FindingExpect 字段同构，直接转换（S1016）
		out.Findings = append(out.Findings, evalq.Finding(e))
	}
	if c.Label == evalq.LabelKnownFP {
		out.Findings = append(out.Findings, evalq.Finding{File: "a.go", Kind: "style-nit", Severity: "low"})
	}
	return out, nil
}
