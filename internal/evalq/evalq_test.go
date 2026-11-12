package evalq_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// mkCase 快速构样例。
func mkCase(id, lang, label, verdict string, mustFind ...evalq.FindingExpect) evalq.Case {
	return evalq.Case{
		ID: id, Language: lang, Label: label, Repo: "acme/app", Title: "case " + id,
		Diff:   "--- a/x\n+++ b/x\n@@ -1 +2 @@",
		Expect: evalq.Expectation{Verdict: verdict, MustFind: mustFind},
	}
}

// TestScoreResultFindingMatching 单样例计分：命中/漏检/误报归因。
func TestScoreResultFindingMatching(t *testing.T) {
	c := mkCase("c1", "go", evalq.LabelStandard, evalq.VerdictRequestChanges,
		evalq.FindingExpect{Kind: "nil-deref", Severity: "high"},
		evalq.FindingExpect{File: "a.go", Kind: "sql-inject"},
	)
	out := evalq.Outcome{
		Verdict: evalq.VerdictRequestChanges,
		Findings: []evalq.Finding{
			{File: "b.go", Kind: "Nil-Deref", Severity: "high"}, // kind 大小写不敏感命中（期望未收窄文件）
			{File: "a.go", Kind: "sql-inject"},                  // 精确命中（kind+file）
			{File: "c.go", Kind: "style", Severity: "low"},      // 非禁报、非 clean → 不计 FP
		},
	}
	res := evalq.ScoreResult(c, out, nil)
	if !res.VerdictHit {
		t.Fatal("verdict 应命中")
	}
	if len(res.TPs) != 2 || len(res.FNs) != 0 {
		t.Fatalf("TP/FN = %v/%v, want 2 命中/0 漏检", res.TPs, res.FNs)
	}
	if len(res.FPs) != 0 {
		t.Fatalf("非常规样例的多报意见不应计 FP: %v", res.FPs)
	}

	// 漏检：只命中一条
	out2 := evalq.Outcome{Verdict: evalq.VerdictRequestChanges, Findings: []evalq.Finding{{File: "a.go", Kind: "sql-inject"}}}
	res2 := evalq.ScoreResult(c, out2, nil)
	if len(res2.TPs) != 1 || len(res2.FNs) != 1 {
		t.Fatalf("TP/FN = %v/%v, want 1/1", res2.TPs, res2.FNs)
	}
}

// TestScoreResultCleanFP clean 基准上的上报意见全部计误报（golden 北极星口径）。
func TestScoreResultCleanFP(t *testing.T) {
	c := mkCase("clean-1", "go", evalq.LabelKnownFP, evalq.VerdictApprove)
	out := evalq.Outcome{
		Verdict:  evalq.VerdictComment,
		Findings: []evalq.Finding{{File: "a.go", Kind: "style"}, {File: "b.go", Kind: "naming"}},
	}
	res := evalq.ScoreResult(c, out, nil)
	if res.VerdictHit {
		t.Fatal("approve 期望未命中")
	}
	if len(res.FPs) != 2 {
		t.Fatalf("clean 基准误报应全计: %v", res.FPs)
	}
}

// TestScoreResultMustNotFind must_not_find 命中的意见计误报。
func TestScoreResultMustNotFind(t *testing.T) {
	c := mkCase("c1", "go", evalq.LabelStandard, evalq.VerdictComment,
		evalq.FindingExpect{Kind: "nil-deref"})
	c.Expect.MustNotFind = []evalq.FindingExpect{{Kind: "style-nit"}}
	out := evalq.Outcome{
		Verdict:  evalq.VerdictComment,
		Findings: []evalq.Finding{{Kind: "nil-deref"}, {Kind: "STYLE-NIT"}},
	}
	res := evalq.ScoreResult(c, out, nil)
	if len(res.TPs) != 1 || len(res.FPs) != 1 {
		t.Fatalf("TP/FP = %v/%v, want 1/1（禁报命中计误报）", res.TPs, res.FPs)
	}
}

// TestScoreResultError 评测出错样例保守计：verdict 不命中 + 全部 must_find 记漏检。
func TestScoreResultError(t *testing.T) {
	c := mkCase("c1", "go", evalq.LabelStandard, evalq.VerdictApprove,
		evalq.FindingExpect{Kind: "nil-deref"})
	res := evalq.ScoreResult(c, evalq.Outcome{}, errors.New("llm timeout"))
	if res.Err == "" || res.VerdictHit {
		t.Fatalf("出错样例: %+v", res)
	}
	if len(res.FNs) != 1 {
		t.Fatalf("出错样例应记漏检: %v", res.FNs)
	}
}

// TestScoreAggregate 全量指标算术：手算对账。
func TestScoreAggregate(t *testing.T) {
	// 构 4 样例：
	//  s1: must_find 2 条，命中 1 漏 1（TP=1 FN=1）
	//  s2: clean，零上报（TN=1）
	//  s3: clean，上报 1（FP=1）
	//  s4: must_find 1 条，命中（TP+1=2）
	results := []evalq.CaseResult{
		{CaseID: "s1", VerdictHit: true, TPs: []int{0}, FNs: []int{1}},
		{CaseID: "s2", VerdictHit: true},
		{CaseID: "s3", VerdictHit: false, FPs: []evalq.Finding{{Kind: "style"}}},
		{CaseID: "s4", VerdictHit: true, TPs: []int{0}},
	}
	m := evalq.Score(results)
	if m.Cases != 4 || m.VerdictHit != 3 {
		t.Fatalf("cases/verdict = %d/%d, want 4/3", m.Cases, m.VerdictHit)
	}
	if m.Confusion.TP != 2 || m.Confusion.FN != 1 || m.Confusion.FP != 1 || m.Confusion.TN != 1 {
		t.Fatalf("混淆矩阵: %+v", m.Confusion)
	}
	if abs(m.Precision-2.0/3.0) > 1e-9 {
		t.Fatalf("Precision = %f, want %f", m.Precision, 2.0/3.0)
	}
	if abs(m.Recall-2.0/3.0) > 1e-9 {
		t.Fatalf("Recall = %f", m.Recall)
	}
	if abs(m.FPRate-0.5) > 1e-9 {
		t.Fatalf("FPRate = %f, want 0.5", m.FPRate)
	}
	if abs(m.MissRate-1.0/3.0) > 1e-9 {
		t.Fatalf("MissRate = %f, want 1/3", m.MissRate)
	}
	if abs(m.VerdictAccuracy-0.75) > 1e-9 {
		t.Fatalf("VerdictAccuracy = %f, want 0.75", m.VerdictAccuracy)
	}
	if m.F1 == 0 {
		t.Fatal("F1 应非零")
	}
}

// TestStability 裁决稳定性：全同=1、全异→modal 口径、摇摆案中值。
func TestStability(t *testing.T) {
	// 全同
	same := []map[string]string{
		{"a": "approve", "b": "comment"},
		{"a": "approve", "b": "comment"},
		{"a": "approve", "b": "comment"},
	}
	if got := evalq.Stability(same); got != 1 {
		t.Fatalf("全同应 1.0: %f", got)
	}
	// 摇摆：a 三轮 approve/approve/comment（一致率 2/3），b 三轮全 comment
	sw := []map[string]string{
		{"a": "approve", "b": "comment"},
		{"a": "approve", "b": "comment"},
		{"a": "comment", "b": "comment"},
	}
	if got := evalq.Stability(sw); abs(got-(2.0/3.0+1.0)/2.0) > 1e-9 {
		t.Fatalf("摇摆一致率 = %f", got)
	}
	// 轮数不足返回 1（无信息不惩罚）
	if got := evalq.Stability(same[:1]); got != 1 {
		t.Fatalf("单轮应 1.0: %f", got)
	}
	// 单案钻取
	if got := evalq.CaseStability(sw, "a"); abs(got-2.0/3.0) > 1e-9 {
		t.Fatalf("单案一致率 = %f", got)
	}
}

// TestEvaluateStabilityRuns 稳定性跑批：k 轮重复调 Reviewer 并汇总。
func TestEvaluateStabilityRuns(t *testing.T) {
	calls := 0
	// 第 1 轮后 a 摇摆一次：approve/comment 交替（可复现序列）
	rev := evalq.ReviewerFunc(func(_ context.Context, c evalq.Case) (evalq.Outcome, error) {
		calls++
		v := evalq.VerdictApprove
		if c.ID == "a" && calls%2 == 0 {
			v = evalq.VerdictComment
		}
		return evalq.Outcome{Verdict: v}, nil
	})
	cases := []evalq.Case{mkCase("a", "go", evalq.LabelKnownFP, evalq.VerdictApprove), mkCase("b", "go", evalq.LabelKnownFP, evalq.VerdictApprove)}
	rep, err := evalq.Evaluate(context.Background(), cases, rev, evalq.Options{Model: "stub", StabilityRuns: 3})
	if err != nil {
		t.Fatal(err)
	}
	// 3 轮 × 2 案 = 6 次调用（首轮复用不重跑）
	if calls != 6 {
		t.Fatalf("k 轮跑批调用数 = %d, want 6", calls)
	}
	if rep.Runs != 3 || rep.Stability <= 0 || rep.Stability > 1 {
		t.Fatalf("稳定性: runs=%d stability=%f", rep.Runs, rep.Stability)
	}
}

// TestEvaluateNilReviewer Reviewer 未装配显式报错（接入点契约）。
func TestEvaluateNilReviewer(t *testing.T) {
	if _, err := evalq.Evaluate(context.Background(), []evalq.Case{mkCase("a", "go", evalq.LabelStandard, "approve")}, nil, evalq.Options{}); err == nil {
		t.Fatal("nil Reviewer 应报错")
	}
}

// TestReportRender 报告双形态：JSON 关键段、Markdown 表头与逐案行。
func TestReportRender(t *testing.T) {
	rev := evalq.ReviewerFunc(func(_ context.Context, c evalq.Case) (evalq.Outcome, error) {
		if c.ID == "boom" {
			return evalq.Outcome{}, errors.New("llm timeout")
		}
		return evalq.Outcome{Verdict: evalq.VerdictApprove}, nil
	})
	cases := []evalq.Case{
		mkCase("ok", "go", evalq.LabelKnownFP, evalq.VerdictApprove),
		mkCase("boom", "go", evalq.LabelStandard, evalq.VerdictApprove, evalq.FindingExpect{Kind: "nil-deref"}),
	}
	rep, err := evalq.Evaluate(context.Background(), cases, rev, evalq.Options{Model: "stub", Params: map[string]string{"temperature": "0.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Errored != 1 {
		t.Fatalf("errored = %d, want 1", rep.Errored)
	}

	var jsonBuf bytes.Buffer
	if err := rep.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fp_rate"`, `"stability"`, `"by_language"`} {
		if !strings.Contains(jsonBuf.String(), want) {
			t.Errorf("JSON 报告缺 %s", want)
		}
	}

	var mdBuf bytes.Buffer
	if err := rep.WriteMarkdown(&mdBuf); err != nil {
		t.Fatal(err)
	}
	md := mdBuf.String()
	for _, want := range []string{"# 基准集质量评测报告", "| Precision |", "| go |", "## 逐案明细", "llm timeout"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺 %q", want)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
