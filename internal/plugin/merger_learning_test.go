package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakePatternStore 内存版 PatternStore：预置低置信模式表，记录 Posted 调用。
type fakePatternStore struct {
	low     []PatternState
	posted  []Finding
	listErr error
}

func (f *fakePatternStore) ListLowConfidencePatterns(_ context.Context, _, _, _ string,
	_ int, _ float64, _ int) ([]PatternState, error) {
	return f.low, f.listErr
}

func (f *fakePatternStore) PatternPosted(_ context.Context, _, _, _, _ string, finding Finding, _ time.Time) error {
	f.posted = append(f.posted, finding)
	return nil
}

// HardFloor：样本充足且置信度极低的 low 条目被丢弃；medium 不丢。
func TestMergeLearningHardFloorDropsLowOnly(t *testing.T) {
	commentLow := "建议提取魔法数字为常量"
	commentMid := "存在空指针解引用风险"
	ps := &fakePatternStore{low: []PatternState{
		{
			Plugin: "style", Pattern: PatternKey("style", commentLow),
			PatternText: PatternSample(commentLow),
			Posted: 0, Regret: 2, Suppress: 1,
			Confidence: Confidence(0, 2, 1, 0), Samples: PatternSamples(0, 2, 1),
		},
		{
			Plugin: "security", Pattern: PatternKey("security", commentMid),
			PatternText: PatternSample(commentMid),
			Posted: 0, Regret: 2, Suppress: 1,
			Confidence: Confidence(0, 2, 1, 0), Samples: PatternSamples(0, 2, 1),
		},
	}}
	m := NewMerger(nil, testNopLogger{}).WithLearning(ps, LearningConfig{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
		InjectFloor: 0.35, InjectTopN: 10,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1",
		[]Outcome{
			{Name: "style", Result: &Result{Findings: []Finding{
				{File: "a.go", Line: 1, Severity: SeverityLow, Comment: commentLow},
			}}},
			{Name: "security", Result: &Result{Findings: []Finding{
				{File: "b.go", Line: 2, Severity: SeverityMedium, Comment: commentMid},
			}}},
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("hard floor 应只留 medium: findings=%d degraded=%v", len(mf.Findings), mf.Degraded)
	}
	if mf.Findings[0].Severity != SeverityMedium {
		t.Fatalf("保留条应为 medium: %+v", mf.Findings[0])
	}
	if len(mf.Degraded) == 0 || !strings.Contains(strings.Join(mf.Degraded, ";"), "学习环抑制") {
		t.Fatalf("应有学习环抑制注明: %v", mf.Degraded)
	}
	// medium 命中 SoftFloor → 标注
	if !strings.Contains(mf.Findings[0].Comment, "历史置信度低") {
		t.Fatalf("medium 应带低置信标注: %q", mf.Findings[0].Comment)
	}
}

// 学习关闭时：不丢、不标、不记 posted。
func TestMergeLearningDisabledIsNoop(t *testing.T) {
	comment := "建议提取魔法数字为常量"
	ps := &fakePatternStore{low: []PatternState{{
		Plugin: "style", Pattern: PatternKey("style", comment),
		Confidence: 0.1, Samples: 5,
	}}}
	m := NewMerger(nil, testNopLogger{}).WithLearning(ps, DefaultLearningConfig())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t2",
		[]Outcome{{Name: "style", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 1, Severity: SeverityLow, Comment: comment, By: "style"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("关闭时不得丢弃: %d", len(mf.Findings))
	}
	if strings.Contains(mf.Findings[0].Comment, "置信度") {
		t.Fatalf("关闭时不得标注: %q", mf.Findings[0].Comment)
	}
	if len(ps.posted) != 0 {
		t.Fatalf("关闭时不得 PatternPosted: %d", len(ps.posted))
	}
}

// 小样本负控：samples < MinSamples 即使置信度极低也不消费。
func TestMergeLearningMinSamplesGuards(t *testing.T) {
	comment := "建议提取魔法数字为常量"
	ps := &fakePatternStore{low: []PatternState{{
		Plugin: "style", Pattern: PatternKey("style", comment),
		Confidence: 0.05, Samples: 1, // < MinSamples=3
	}}}
	m := NewMerger(nil, testNopLogger{}).WithLearning(ps, LearningConfig{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t3",
		[]Outcome{{Name: "style", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 1, Severity: SeverityLow, Comment: comment, By: "style"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("小样本不得消费: findings=%d", len(mf.Findings))
	}
}

// 开启学习时：非 suppressed 条目应 PatternPosted。
func TestMergeLearningRecordsPosted(t *testing.T) {
	ps := &fakePatternStore{}
	m := NewMerger(nil, testNopLogger{}).WithLearning(ps, LearningConfig{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	_, err := m.Merge(context.Background(), regretTestPR(), "t4",
		[]Outcome{{Name: "security", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 1, Severity: SeverityHigh, Comment: "存在空指针解引用风险", By: "security"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.posted) != 1 || ps.posted[0].Comment != "存在空指针解引用风险" {
		t.Fatalf("应记 1 次 posted: %+v", ps.posted)
	}
}
