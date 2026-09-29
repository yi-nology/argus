package storage

import (
	"context"
	"testing"
	"time"

	"argus/internal/domain"
)

// 信号往返：Posted 累加；Regret/Suppress 压低置信度；Restore 回抬。
func TestPatternSignalsLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	f := domain.Finding{File: "a.go", Line: 3, Severity: "low", Comment: "未检查 error 返回"}

	// 创建 + Posted
	if err := s.PatternPosted(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetPattern(ctx, "gitea", "acme", "app", "security", f.Comment)
	if err != nil || p == nil || p.Posted != 1 {
		t.Fatalf("posted 后 p=%+v err=%v", p, err)
	}

	// Regret×2 + Suppress×1 → 低于 soft floor
	for i := 0; i < 2; i++ {
		if err := s.PatternRegret(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PatternSuppress(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPattern(ctx, "gitea", "acme", "app", "security", f.Comment)
	if p == nil || p.Regret != 2 || p.Suppress != 1 {
		t.Fatalf("计数不对: %+v", p)
	}
	if p.Confidence >= 0.35 {
		t.Fatalf("confidence=%v 应低于 soft floor 0.35", p.Confidence)
	}

	// Restore 回抬
	if err := s.PatternRestore(ctx, "gitea", "acme", "app", "security", f, at); err != nil {
		t.Fatal(err)
	}
	p2, _ := s.GetPattern(ctx, "gitea", "acme", "app", "security", f.Comment)
	if p2 == nil || p2.Confidence <= p.Confidence {
		t.Fatalf("restore 未回抬: before=%v after=%+v", p.Confidence, p2)
	}

	// 不同 plugin 隔离
	if err := s.PatternPosted(ctx, "gitea", "acme", "app", "style", f, at); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.GetPattern(ctx, "gitea", "acme", "app", "style", f.Comment)
	if ps == nil || ps.Regret != 0 {
		t.Fatalf("plugin 隔离失败: %+v", ps)
	}
	// security 的 regret 不应出现在 style
	if ps.Suppress != 0 {
		t.Fatalf("style 不应继承 suppress: %+v", ps)
	}
}

// 负控：小样本不进 ListLowConfidencePatterns；仓库隔离。
func TestListLowConfidencePatternsFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	fLow := domain.Finding{Comment: "建议提取常量"}
	fHigh := domain.Finding{Comment: "存在空指针解引用风险"}

	// 低置信：samples=4, regret2+suppress1
	for i := 0; i < 2; i++ {
		_ = s.PatternRegret(ctx, "gitea", "acme", "app", "style", fLow, at)
	}
	_ = s.PatternSuppress(ctx, "gitea", "acme", "app", "style", fLow, at)
	_ = s.PatternPosted(ctx, "gitea", "acme", "app", "style", fLow, at)

	// 高置信：大量 posted
	for i := 0; i < 5; i++ {
		_ = s.PatternPosted(ctx, "gitea", "acme", "app", "security", fHigh, at)
	}

	// 小样本负控：只有 1 次 posted，minSamples=3 时不得出现
	_ = s.PatternPosted(ctx, "gitea", "acme", "app", "cli", domain.Finding{Comment: "仅一次"}, at)

	list, err := s.ListLowConfidencePatterns(ctx, "gitea", "acme", "app", 3, 0.35, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 low-confidence, got %d: %+v", len(list), list)
	}
	if list[0].Plugin != "style" {
		t.Fatalf("want style, got %s", list[0].Plugin)
	}

	// 仓库隔离：另一仓库看不到
	list2, _ := s.ListLowConfidencePatterns(ctx, "gitea", "acme", "other", 3, 0.35, 10)
	if len(list2) != 0 {
		t.Fatalf("仓库隔离失败: %+v", list2)
	}
}
