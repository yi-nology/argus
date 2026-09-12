package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakePrintStore 内存版 PrintStore：预置 prior 状态与抑制表，用于 Merger
// 跨轮标注逻辑的单测（不触库）。
type fakePrintStore struct {
	prior      map[string]*PrintState
	suppressed map[string]bool
	getErr     error
}

func (f *fakePrintStore) GetPrintState(_ context.Context, _, _, _, _, fp string) (*PrintState, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.prior[fp], nil
}

func (f *fakePrintStore) UpsertPrint(_ context.Context, _, _, _, _, fp string,
	_ Finding, _ string, _ time.Time) (bool, error) {
	return f.suppressed[fp], nil
}

func regretTestPR() PRContext {
	return PRContext{Platform: "github", Owner: "o", Repo: "r", Number: "1"}
}

// 正常：既有复现（TimesSeen>=2）与 regret（RegretCount>=2）标注到 comment；
// 无历史的指纹不得带任何标注。
func TestMergeAnnotatesRecurrenceAndRegret(t *testing.T) {
	fpA := Fingerprint("a.go", "空指针风险")
	fpB := Fingerprint("b.go", "命名不佳")
	store := &fakePrintStore{prior: map[string]*PrintState{
		fpA: {Status: "open", TimesSeen: 2, RegretCount: 3},
	}}
	m := NewMerger(store, testNopLogger{})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1",
		[]Outcome{{Name: "s1", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 1, Severity: "high", Comment: "空指针风险"},
			{File: "b.go", Line: 2, Severity: "low", Comment: "命名不佳"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(mf.Findings))
	}
	for _, f := range mf.Findings {
		switch f.File {
		case "a.go":
			if !strings.Contains(f.Comment, "跨轮复现 3 次") {
				t.Errorf("a.go 缺复现标注: %q", f.Comment)
			}
			if !strings.Contains(f.Comment, "剔除 3 次") {
				t.Errorf("a.go 缺 regret 标注: %q", f.Comment)
			}
		case "b.go":
			if strings.Contains(f.Comment, "（") {
				t.Errorf("b.go 无历史不应标注: %q", f.Comment)
			}
		}
	}
}

// 负控：suppressed 指纹依旧被剔除——标注逻辑不得复活被人工抑制的条目。
func TestMergeSuppressedPriorStillDropped(t *testing.T) {
	fp := Fingerprint("c.go", "误报历史")
	store := &fakePrintStore{
		prior:      map[string]*PrintState{fp: {Status: "suppressed", TimesSeen: 5}},
		suppressed: map[string]bool{fp: true},
	}
	m := NewMerger(store, testNopLogger{})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t2",
		[]Outcome{{Name: "s1", Result: &Result{Findings: []Finding{
			{File: "c.go", Line: 9, Severity: "medium", Comment: "误报历史"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 0 || mf.Suppressed != 1 {
		t.Fatalf("findings=%d suppressed=%d, want 0/1", len(mf.Findings), mf.Suppressed)
	}
}

// 异常：GetPrintState 失败只降级为告警，合并照常产出。
func TestMergeGetStateErrorKeepsMerge(t *testing.T) {
	store := &fakePrintStore{getErr: errors.New("db down")}
	m := NewMerger(store, testNopLogger{})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t3",
		[]Outcome{{Name: "s1", Result: &Result{Findings: []Finding{
			{File: "d.go", Line: 3, Severity: "high", Comment: "越界访问"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(mf.Findings))
	}
}
