package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"

	"time"

	"github.com/oekit/logx"
)

// fakePatternStore 内存版 domain.PatternStore：预置低置信模式表，记录 Posted 调用。
type fakePatternStore struct {
	low      []learning.PatternState
	posted   []domain.Finding
	postedBy []string // PatternPosted 的 pluginName 实参（键的 plugin 分量）
	listErr  error
}

func (f *fakePatternStore) ListLowConfidencePatterns(_ context.Context, _, _, _ string,
	_ int, _ float64, _ int) ([]learning.PatternState, error) {
	return f.low, f.listErr
}

func (f *fakePatternStore) PatternPosted(_ context.Context, _, _, _, pluginName string, finding domain.Finding, _ time.Time) error {
	f.posted = append(f.posted, finding)
	f.postedBy = append(f.postedBy, pluginName)
	return nil
}

// HardFloor：样本充足且置信度极低的 low 条目被丢弃；medium 不丢。
func TestMergeLearningHardFloorDropsLowOnly(t *testing.T) {
	commentLow := "建议提取魔法数字为常量"
	commentMid := "存在空指针解引用风险"
	ps := &fakePatternStore{low: []learning.PatternState{
		{
			Plugin: "style", Pattern: learning.PatternKey("style", commentLow),
			PatternText: learning.PatternSample(commentLow),
			Posted:      0, Regret: 2, Suppress: 1,
			Confidence: learning.Confidence(0, 2, 1, 0), Samples: learning.PatternSamples(0, 2, 1),
		},
		{
			Plugin: "security", Pattern: learning.PatternKey("security", commentMid),
			PatternText: learning.PatternSample(commentMid),
			Posted:      0, Regret: 2, Suppress: 1,
			Confidence: learning.Confidence(0, 2, 1, 0), Samples: learning.PatternSamples(0, 2, 1),
		},
	}}
	m := NewMerger(nil, logx.Nop()).WithLearning(ps, learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
		InjectFloor: 0.35, InjectTopN: 10,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1",
		[]Outcome{
			{Name: "style", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "a.go", Line: 1, Severity: plugin.SeverityLow, Comment: commentLow},
			}}},
			{Name: "security", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "b.go", Line: 2, Severity: plugin.SeverityMedium, Comment: commentMid},
			}}},
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("hard floor 应只留 medium: findings=%d degraded=%v", len(mf.Findings), mf.Degraded)
	}
	if mf.Findings[0].Severity != plugin.SeverityMedium {
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
	ps := &fakePatternStore{low: []learning.PatternState{{
		Plugin: "style", Pattern: learning.PatternKey("style", comment),
		Confidence: 0.1, Samples: 5,
	}}}
	m := NewMerger(nil, logx.Nop()).WithLearning(ps, learning.DefaultConfig())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t2",
		[]Outcome{{Name: "style", Result: &plugin.Result{Findings: []domain.Finding{
			{File: "a.go", Line: 1, Severity: plugin.SeverityLow, Comment: comment, By: "style"},
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
	ps := &fakePatternStore{low: []learning.PatternState{{
		Plugin: "style", Pattern: learning.PatternKey("style", comment),
		Confidence: 0.05, Samples: 1, // < MinSamples=3
	}}}
	m := NewMerger(nil, logx.Nop()).WithLearning(ps, learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t3",
		[]Outcome{{Name: "style", Result: &plugin.Result{Findings: []domain.Finding{
			{File: "a.go", Line: 1, Severity: plugin.SeverityLow, Comment: comment, By: "style"},
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
	m := NewMerger(nil, logx.Nop()).WithLearning(ps, learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	_, err := m.Merge(context.Background(), regretTestPR(), "t4",
		[]Outcome{{Name: "security", Result: &plugin.Result{Findings: []domain.Finding{
			{File: "a.go", Line: 1, Severity: plugin.SeverityHigh, Comment: "存在空指针解引用风险", By: "security"},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.posted) != 1 || ps.posted[0].Comment != "存在空指针解引用风险" {
		t.Fatalf("应记 1 次 posted: %+v", ps.posted)
	}
}

// v3.9.1 回归：SoftFloor 命中条目若先标注后记账，标注文本会进 PatternKey
// ——posted 记进"原文+标注"的影子行，真实模式行只累积 regret，置信度单调
// 下跌后 HardFloor 永久吞掉本应正确的条目。posted 必须基于标注前原文。
func TestMergeSoftFloorPostsPreAnnotationKey(t *testing.T) {
	comment := "存在空指针解引用风险"
	ps := &fakePatternStore{low: []learning.PatternState{{
		Plugin: "security", Pattern: learning.PatternKey("security", comment),
		PatternText: learning.PatternSample(comment),
		Confidence:  0.2, Samples: 5,
	}}}
	m := NewMerger(nil, logx.Nop()).WithLearning(ps, learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t-softfloor",
		[]Outcome{{Name: "security", Result: &plugin.Result{Findings: []domain.Finding{
			{File: "a.go", Line: 1, Severity: plugin.SeverityMedium, Comment: comment},
		}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 || !strings.Contains(mf.Findings[0].Comment, "历史置信度低") {
		t.Fatalf("应保留并标注: %+v", mf.Findings)
	}
	if len(ps.posted) != 1 {
		t.Fatalf("应记 1 次 posted: %d", len(ps.posted))
	}
	if ps.posted[0].Comment != comment {
		t.Fatalf("posted 评语必须为标注前原文: %q", ps.posted[0].Comment)
	}
}

// v3.9.1 回归：多插件合并条目（By="a,b"、评语拼接"（X 同位置补充）"）的 posted
// 必须落在首见插件的 prints 行原文键上——prints 原文是 regret 回查的权威来源，
// 两键不一致 = posted/regret 错账（真实模式行只缺不涨）。
func TestMergeMergedEntryPostsAuthoritativeComment(t *testing.T) {
	base := "空指针风险"
	fp := plugin.Fingerprint("a.go", base, 1)
	prints := &fakePrintStore{prior: map[string]*domain.PrintState{
		fp: {Status: "open", TimesSeen: 1, Comment: base},
	}}
	ps := &fakePatternStore{}
	m := NewMerger(prints, logx.Nop()).WithLearning(ps, learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20,
	})
	mf, err := m.Merge(context.Background(), regretTestPR(), "t-merged",
		[]Outcome{
			{Name: "sec", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "a.go", Line: 1, Severity: plugin.SeverityHigh, Comment: base},
			}}},
			{Name: "aux", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "a.go", Line: 1, Severity: plugin.SeverityMedium, Comment: "判空缺失另一种措辞"},
			}}},
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("同位置应并为一条: %d", len(mf.Findings))
	}
	if len(ps.posted) != 1 {
		t.Fatalf("应记 1 次 posted: %d", len(ps.posted))
	}
	if ps.posted[0].Comment != base {
		t.Fatalf("posted 必须用 prints 行原文（非拼接文本）: %q", ps.posted[0].Comment)
	}
	if len(ps.postedBy) != 1 || ps.postedBy[0] != "sec" {
		t.Fatalf("posted 归属必须取首段: %q", ps.postedBy)
	}
}
