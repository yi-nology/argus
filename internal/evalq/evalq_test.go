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
	if len(res.Unlabeled) != 1 || res.Unlabeled[0].Kind != "style" || len(res.UnlabeledKinds) != 1 {
		t.Fatalf("非 clean 多报应进 Unlabeled 显式化（不丢弃不计 FP）: %+v", res.Unlabeled)
	}
	if res.Clean {
		t.Fatal("must_find 非空样例 Clean 应 false")
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
	results := []evalq.CaseResult{
		{CaseID: "s1", VerdictHit: true, TPs: []int{0}, FNs: []int{1}},
		{CaseID: "s2", Clean: true, VerdictHit: true},
		{CaseID: "s3", Clean: true, VerdictHit: false, FPs: []evalq.Finding{{Kind: "style"}}},
		{CaseID: "s4", VerdictHit: true, TPs: []int{0}},
		{CaseID: "s5", Unlabeled: []evalq.Finding{{Kind: "nit"}}, UnlabeledKinds: []string{"nit"}},
	}
	m := evalq.Score(results)
	if m.Cases != 5 || m.VerdictHit != 3 {
		t.Fatalf("cases/verdict = %d/%d, want 5/3", m.Cases, m.VerdictHit)
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
	// Phase1②：干净样例误报发生率 = 有 FP 的干净样例 / 干净样例 = s3/s2,s3 = 1/2
	// （样例单位，修复旧 fp_rate 分子分母单位混用）。
	if abs(m.CleanCaseFPRate-0.5) > 1e-9 {
		t.Fatalf("CleanCaseFPRate = %f, want 0.5", m.CleanCaseFPRate)
	}
	// Unlabeled 聚合：s5 一条，不入 FP。
	if m.Unlabeled != 1 {
		t.Fatalf("Unlabeled = %d, want 1", m.Unlabeled)
	}
	if abs(m.MissRate-1.0/3.0) > 1e-9 {
		t.Fatalf("MissRate = %f, want 1/3", m.MissRate)
	}
	// 5 案 3 命中 → 3/5=0.6（brief 草稿沿用旧 4 案 0.75，加 s5 后按其
	// Cases=5/VerdictHit=3 断言口径修正）。
	if abs(m.VerdictAccuracy-3.0/5.0) > 1e-9 {
		t.Fatalf("VerdictAccuracy = %f, want 0.6", m.VerdictAccuracy)
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

// TestScoreSeverity 分严重度聚合：期望按金标准 must_find 的 severity 归桶、
// 实际上报按实际上报 severity 归桶、TP/FP/FN 分档对账、零分母置 0。
func TestScoreSeverity(t *testing.T) {
	cases := []evalq.Case{
		// 两条 high 期望（一条命中一条漏检）+ 实际上报 2 high 1 low
		mkCase("h1", "go", evalq.LabelStandard, evalq.VerdictRequestChanges,
			evalq.FindingExpect{Kind: "nil-deref", Severity: "high"},
			evalq.FindingExpect{Kind: "missing-tx", Severity: "HIGH"}), // 大小写归一
		// medium 期望漏检，实际另报一条 medium（非常规样例的多报不计 FP）
		mkCase("m1", "go", evalq.LabelStandard, evalq.VerdictComment,
			evalq.FindingExpect{Kind: "error-swallow", Severity: "medium"}),
		// 无 severity 标注的期望归 unspecified 桶
		mkCase("u1", "go", evalq.LabelStandard, evalq.VerdictComment,
			evalq.FindingExpect{Kind: "style-nit"}),
		// clean 样例上报一条 high → FP 计 high 桶
		mkCase("c1", "go", evalq.LabelKnownFP, evalq.VerdictApprove),
	}
	results := []evalq.CaseResult{
		{CaseID: "h1", VerdictHit: true, TPs: []int{0}, FNs: []int{1},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictRequestChanges, Findings: []evalq.Finding{
				{Kind: "nil-deref", Severity: "high"},
				{Kind: "missing-tx", Severity: "high"},
				{Kind: "style-nit", Severity: "low"},
			}}},
		{CaseID: "m1", FNs: []int{0},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictComment, Findings: []evalq.Finding{
				{Kind: "doc-comment", Severity: "medium"},
			}}},
		{CaseID: "u1", FNs: []int{0}, Outcome: evalq.Outcome{Verdict: evalq.VerdictComment}},
		{CaseID: "c1", VerdictHit: false,
			FPs: []evalq.Finding{{Kind: "naming", Severity: "high"}},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictComment, Findings: []evalq.Finding{
				{Kind: "naming", Severity: "high"},
			}}},
	}
	rows := evalq.ScoreSeverity(cases, results)
	bySev := map[string]evalq.SeverityReport{}
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		bySev[r.Severity] = r
		order = append(order, r.Severity)
	}
	// 行序：词表（high/medium/low）在前，未标注垫底
	wantOrder := []string{"high", "medium", "low", "unspecified"}
	if len(order) != len(wantOrder) {
		t.Fatalf("分严重度行 = %v, want %v", order, wantOrder)
	}
	for i, s := range wantOrder {
		if order[i] != s {
			t.Fatalf("分严重度行序 = %v, want %v", order, wantOrder)
		}
	}

	h := bySev["high"]
	if h.Expected != 2 || h.Reported != 3 || h.TP != 1 || h.FN != 1 || h.FP != 1 {
		t.Fatalf("high 桶: %+v, want expected=2 reported=3 tp=1 fn=1 fp=1", h)
	}
	if abs(h.Precision-0.5) > 1e-9 || abs(h.Recall-0.5) > 1e-9 || abs(h.MissRate-0.5) > 1e-9 {
		t.Fatalf("high 桶比率: p=%f r=%f miss=%f, want 全 0.5", h.Precision, h.Recall, h.MissRate)
	}

	m := bySev["medium"]
	if m.Expected != 1 || m.Reported != 1 || m.TP != 0 || m.FN != 1 || m.FP != 0 {
		t.Fatalf("medium 桶: %+v, want expected=1 reported=1 tp=0 fn=1 fp=0", m)
	}
	// 零分母置 0（Precision 分母 TP+FP=0）
	if m.Precision != 0 || m.Recall != 0 || abs(m.MissRate-1) > 1e-9 {
		t.Fatalf("medium 桶比率: p=%f r=%f miss=%f, want 0/0/1", m.Precision, m.Recall, m.MissRate)
	}

	l := bySev["low"]
	if l.Expected != 0 || l.Reported != 1 || l.TP != 0 || l.FP != 0 || l.FN != 0 {
		t.Fatalf("low 桶: %+v, want expected=0 reported=1 全 0 计数", l)
	}
	// 零分母置 0（无期望也无误报）
	if l.Precision != 0 || l.Recall != 0 || l.MissRate != 0 {
		t.Fatalf("low 桶比率应全 0: %+v", l)
	}

	u := bySev["unspecified"]
	if u.Expected != 1 || u.FN != 1 {
		t.Fatalf("unspecified 桶: %+v, want expected=1 fn=1", u)
	}
}

// TestScoreSeverityEmpty 空数据也出词表行（零分母置 0，表形态稳定）。
func TestScoreSeverityEmpty(t *testing.T) {
	rows := evalq.ScoreSeverity(nil, nil)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Severity] = true
	}
	for _, s := range []string{"high", "medium", "low"} {
		if !got[s] {
			t.Fatalf("空数据应仍出 %s 行: %v", s, got)
		}
	}
	for _, r := range rows {
		if r.Expected != 0 || r.Reported != 0 || r.TP != 0 || r.FP != 0 || r.FN != 0 ||
			r.Precision != 0 || r.Recall != 0 || r.MissRate != 0 {
			t.Fatalf("空数据行应全 0: %+v", r)
		}
	}
}

// TestSeverityReportRender 分严重度聚合双形态：JSON 段 + Markdown 分严重度表。
func TestSeverityReportRender(t *testing.T) {
	cases := []evalq.Case{
		mkCase("h1", "go", evalq.LabelStandard, evalq.VerdictRequestChanges,
			evalq.FindingExpect{Kind: "nil-deref", Severity: "high"}),
	}
	results := []evalq.CaseResult{
		{CaseID: "h1", VerdictHit: true, TPs: []int{0},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictRequestChanges, Findings: []evalq.Finding{
				{Kind: "nil-deref", Severity: "high"},
			}}},
	}
	rep := &evalq.Report{Cases: 1, Overall: evalq.Score(results), Results: results}
	rep.BySeverity = evalq.ScoreSeverity(cases, results)

	var jsonBuf bytes.Buffer
	if err := rep.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonBuf.String(), `"by_severity"`) {
		t.Error("JSON 报告缺 by_severity 段")
	}

	var mdBuf bytes.Buffer
	if err := rep.WriteMarkdown(&mdBuf); err != nil {
		t.Fatal(err)
	}
	md := mdBuf.String()
	for _, want := range []string{"## 分严重度指标", "| high |", "| 期望条数 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺分严重度表 %q", want)
		}
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
	for _, want := range []string{`"fp_rate"`, `"stability"`, `"by_language"`,
		`"clean_case_fp_rate"`, `"unlabeled_count"`} {
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
