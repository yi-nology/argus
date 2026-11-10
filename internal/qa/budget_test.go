package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/yi-nology/argus/internal/storage"
)

// fakeSummaryModel 返回定值摘要的 QA 模型（压实路径专用桩）。
type fakeSummaryModel struct{ reply string }

func (f *fakeSummaryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Content: f.reply}, nil
}
func (f *fakeSummaryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func bigTurns(n int) []storage.QATurn {
	out := make([]storage.QATurn, n)
	for i := range out {
		out[i] = storage.QATurn{
			Question: strings.Repeat("问", 300),
			Answer:   strings.Repeat("答", 300),
			At:       "2026-10-20T10:00:00Z",
		}
	}
	return out
}

func TestCompactHistoryCriticalPressure(t *testing.T) {
	e := &Engine{
		QAModel:             &fakeSummaryModel{reply: "压缩后的摘要"},
		HistoryBudgetTokens: 600, // 600 rune/轮 ≈ 200 token；12 轮 ≈ 2400 token ≫ 90% 档
	}
	history := bigTurns(12)
	newSummary, tail, ok := e.compactHistory(context.Background(), "p/o/r#1", history, "旧摘要")
	if !ok {
		t.Fatal("Critical 压力档压实应成功")
	}
	if newSummary != "压缩后的摘要" {
		t.Fatalf("摘要 = %q", newSummary)
	}
	if len(tail) != maxTurns {
		t.Fatalf("逐字尾部 = %d 轮，want %d", len(tail), maxTurns)
	}
	// 尾部必须是**末尾**轮次（不是开头）
	if tail[0].Question != history[len(history)-maxTurns].Question {
		t.Fatal("尾部轮次错位：应保留最末 maxTurns 轮")
	}
}

func TestCompactHistoryGuardsReject(t *testing.T) {
	e := &Engine{QAModel: &fakeSummaryModel{reply: "x"}}
	// 条目数不足 MinEffectiveItems：计划守卫拒绝，ok=false 回落轮数窗
	_, _, ok := e.compactHistory(context.Background(), "p/o/r#1", bigTurns(3), "")
	if ok {
		t.Fatal("条目不足时压实应被拒（ok=false）")
	}
}

func TestHistoryBudgetDefault(t *testing.T) {
	e := &Engine{}
	if got := e.historyBudget(); got != defaultHistoryBudgetTokens {
		t.Fatalf("缺省预算 = %d, want %d", got, defaultHistoryBudgetTokens)
	}
	e2 := &Engine{HistoryBudgetTokens: 1234}
	if got := e2.historyBudget(); got != 1234 {
		t.Fatalf("显式预算 = %d, want 1234", got)
	}
}

func TestHistoryItemsShape(t *testing.T) {
	items, contents, total := historyItems(bigTurns(2), "摘")
	if len(items) != 3 || len(contents) != 3 { // 摘要 1 + 轮次 2
		t.Fatalf("条目/正文数 = %d/%d, want 3/3", len(items), len(contents))
	}
	if items[0].ID != summaryItemID {
		t.Fatalf("首条应为摘要条目，got %q", items[0].ID)
	}
	if total <= 0 {
		t.Fatalf("total 应 >0, got %d", total)
	}
	// 摘要为空：无摘要条目
	items2, _, _ := historyItems(bigTurns(2), "")
	if len(items2) != 2 {
		t.Fatalf("空摘要条目数 = %d, want 2", len(items2))
	}
}
