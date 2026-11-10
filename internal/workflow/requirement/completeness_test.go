package requirement

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

func sel(name string, lock bool) dispatch.Selection {
	return dispatch.Selection{Reg: &dispatch.Registration{
		Manifest:       plugin.AgentManifest{Name: name},
		LockRepoConfig: lock,
		Enabled:        true,
	}}
}

func okOutcome(name string) dispatch.Outcome {
	return dispatch.Outcome{Name: name, Result: &plugin.Result{}}
}

func TestJudgeCompleteness_AllPass(t *testing.T) {
	task := plugin.Task{Diff: &plugin.DiffContext{Text: "x", Files: []plugin.DiffFile{{Path: "a.go", Included: true}}}}
	spec := &plugin.RequirementSpec{ACs: []plugin.AcceptanceCriterion{{ID: "AC-1", Text: "x"}}}
	report := &plugin.ComplianceReport{Checks: []plugin.ACCheck{{ID: "AC-1", Status: plugin.ACImplemented, Evidence: "a.go:1"}}}
	rep := JudgeCompleteness([]dispatch.Selection{sel("correctness", false)},
		[]dispatch.Outcome{okOutcome("correctness")}, nil, task, spec, report)
	if !rep.OK || len(rep.Failures) != 0 {
		t.Fatalf("应全部通过: %+v", rep)
	}
	if rep.DegradedNotes() != nil {
		t.Fatal("通过时不应有注记")
	}
}

func TestJudgeCompleteness_DiffTruncated(t *testing.T) {
	task := plugin.Task{Diff: &plugin.DiffContext{Text: "x", Truncated: true}}
	rep := JudgeCompleteness(nil, nil, nil, task, nil, nil)
	if rep.OK {
		t.Fatal("截断应判不过")
	}
	found := false
	for _, it := range rep.Items {
		if it.Check == plugin.CheckDiffTruncated && !it.OK {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺 diff_truncated 失败项: %+v", rep.Items)
	}
}

func TestJudgeCompleteness_PluginsMissing(t *testing.T) {
	task := plugin.Task{}
	outcomes := []dispatch.Outcome{
		okOutcome("correctness"),
		{Name: "security", Err: errBoom()}, // 执行失败
		{Name: "ocr", SkippedReason: "熔断中（连续失败，5m0s 冷却后自动探测恢复）"}, // 熔断
	}
	sel := []dispatch.Selection{sel("correctness", false), sel("security", true), sel("ocr", false)}
	rep := JudgeCompleteness(sel, outcomes, []string{"heavy-expert"}, task, nil, nil)
	if rep.OK {
		t.Fatal("插件缺失应判不过")
	}
	text := strings.Join(rep.Failures, "\n")
	for _, want := range []string{"security（执行失败）", "ocr（熔断中", "heavy-expert（token 预算裁剪）"} {
		if !strings.Contains(text, want) {
			t.Fatalf("失败项缺 %q: %s", want, text)
		}
	}
	if !strings.Contains(text, "lockRepoConfig 锁定插件未产出：security") {
		t.Fatalf("锁定插件缺失应单列: %s", text)
	}
}

func TestJudgeCompleteness_ACUncovered(t *testing.T) {
	task := plugin.Task{}
	spec := &plugin.RequirementSpec{ACs: []plugin.AcceptanceCriterion{
		{ID: "AC-1"}, {ID: "AC-2"}, {ID: "AC-3"},
	}}
	report := &plugin.ComplianceReport{Checks: []plugin.ACCheck{
		{ID: "AC-1", Status: plugin.ACImplemented, Evidence: "a.go:1"},
	}}
	// R3 只判了 AC-1 → AC-2/AC-3 未覆盖
	rep := JudgeCompleteness(nil, []dispatch.Outcome{okOutcome("correctness")}, nil, task, spec, report)
	if rep.OK {
		t.Fatal("AC 漏判应判不过")
	}
	if !strings.Contains(strings.Join(rep.Failures, "\n"), "AC-2、AC-3") {
		t.Fatalf("应列出未覆盖 AC: %+v", rep.Failures)
	}
}

func TestJudgeCompleteness_UnverifiableRatio(t *testing.T) {
	task := plugin.Task{}
	spec := &plugin.RequirementSpec{ACs: []plugin.AcceptanceCriterion{{ID: "AC-1"}, {ID: "AC-2"}}}
	mkReport := func(s1, s2 string) *plugin.ComplianceReport {
		return &plugin.ComplianceReport{Checks: []plugin.ACCheck{
			{ID: "AC-1", Status: s1}, {ID: "AC-2", Status: s2},
		}}
	}
	if rep := JudgeCompleteness(nil, []dispatch.Outcome{okOutcome("x")}, nil, task, spec,
		mkReport(plugin.ACUnverifiable, plugin.ACImplemented)); !rep.OK {
		t.Fatalf("1/2 unverifiable 不应触发: %+v", rep.Failures)
	}
	if rep := JudgeCompleteness(nil, []dispatch.Outcome{okOutcome("x")}, nil, task, spec,
		mkReport(plugin.ACUnverifiable, plugin.ACUnverifiable)); rep.OK {
		t.Fatal("2/2 unverifiable 应触发")
	}
}

func TestJudgeCompleteness_ReviewerDegraded(t *testing.T) {
	task := plugin.Task{}
	degraded := okOutcome("correctness")
	degraded.Result.Notes = plugin.DegradationMarker + " ReAct 执行失败，已降级为单轮审查"
	rep := JudgeCompleteness(nil, []dispatch.Outcome{degraded}, nil, task, nil, nil)
	if rep.OK {
		t.Fatal("reviewer 自报降级应判不过")
	}
	if !strings.Contains(strings.Join(rep.Failures, "\n"), "correctness") {
		t.Fatalf("应点名降级插件: %+v", rep.Failures)
	}
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

func errBoom() error { return boomErr{} }
