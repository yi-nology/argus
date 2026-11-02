package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"time"

	"argus/internal/domain"
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

func (f *fakePrintStore) UpsertPrints(_ context.Context, _, _, _, _, _ string,
	_ time.Time, items []domain.PrintUpsert) []domain.PrintUpsertResult {
	out := make([]domain.PrintUpsertResult, len(items))
	for i, it := range items {
		out[i] = domain.PrintUpsertResult{
			Prior:      f.prior[it.FP],
			Suppressed: f.suppressed[it.FP],
		}
	}
	return out
}

func regretTestPR() PRContext {
	return PRContext{Platform: "github", Owner: "o", Repo: "r", Number: "1"}
}

// 正常：既有复现（TimesSeen>=2）与 regret（RegretCount>=2）标注到 comment；
// 无历史的指纹不得带任何标注。
func TestMergeAnnotatesRecurrenceAndRegret(t *testing.T) {
	fpA := Fingerprint("a.go", "空指针风险", 1)
	_ = Fingerprint("b.go", "命名不佳", 2) // 无历史指纹：负控（不得带标注）
	store := &fakePrintStore{prior: map[string]*PrintState{
		fpA: {Status: "open", TimesSeen: 2, RegretCount: 3},
	}}
	m := NewMerger(store, logx.Nop())
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
	fp := Fingerprint("c.go", "误报历史", 9)
	store := &fakePrintStore{
		prior:      map[string]*PrintState{fp: {Status: "suppressed", TimesSeen: 5}},
		suppressed: map[string]bool{fp: true},
	}
	m := NewMerger(store, logx.Nop())
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
	m := NewMerger(store, logx.Nop())
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

// v3.8.0 回归：即使 comment 被复现/regret 标注改写，Findings[].FP 也必须等于
// 入库指纹——渲染短码与人工反馈都消费它，重算必漂移（漂移=被标注条目无法抑制）。
func TestMergeFindingCarriesStoredFingerprint(t *testing.T) {
	fpA := Fingerprint("a.go", "空指针风险", 1)
	store := &fakePrintStore{prior: map[string]*PrintState{
		fpA: {Status: "open", TimesSeen: 2, RegretCount: 3},
	}}
	m := NewMerger(store, logx.Nop())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t-fp",
		[]Outcome{{Name: "s1", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 1, Severity: "high", Comment: "空指针风险"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(mf.Findings))
	}
	f := mf.Findings[0]
	if f.FP != fpA {
		t.Fatalf("FP = %q, want 入库指纹 %q", f.FP, fpA)
	}
	// 负控：标注后的终稿文本重算指纹必然不同——短码绝不允许走这条路。
	if drift := Fingerprint(f.File, f.Comment, f.Line); drift == fpA {
		t.Fatal("标注未改写 comment，测试前置失效")
	}
}
