package context

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"golang.org/x/time/rate"
)

// 门面冒烟：别名与 SDK 实现同源（行为测试在 agentkit/llm/wrap，此处钉接线）。
func TestWrapFacadeAliases(t *testing.T) {
	inner := &facadeFake{}
	fm := NewFitModel(inner, "chat", "m", 10_000, 0, func(stage string, res FitResult) {
		if res.Action != FitTruncated {
			t.Fatalf("超长输入应截断，got %s", res.Action)
		}
	})
	fm.OnBreakdown = func(stage string, byRole map[string]int) {
		if stage != "chat" || byRole["user"] == 0 {
			t.Fatalf("OnBreakdown 分解缺失: %v", byRole)
		}
	}
	if _, err := fm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: string(make([]rune, 30_000))}}); err != nil {
		t.Fatalf("FitModel.Generate: %v", err)
	}

	cm := NewContinueModel(&facadeSeq{}, "R3", nil)
	if cm == nil {
		t.Fatal("ContinueModel 门面构造失败")
	}

	if got := NewRateModel(inner, nil); got != model.BaseChatModel(inner) {
		t.Fatal("nil limiter 应零开销直通")
	}
	if NewRateModel(inner, rate.NewLimiter(rate.Inf, 1)) == model.BaseChatModel(inner) {
		t.Fatal("有 limiter 应包 RateModel")
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "abc"}}
	out, _ := FitMessages(msgs, 10_000)
	if len(out) != 1 || out[0] != msgs[0] {
		t.Fatal("未超限应零拷贝直通")
	}
	RecordUsage("any", 100) // 校准回灌门面直通不 panic
}

type facadeFake struct{ model.BaseChatModel }

func (f *facadeFake) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

type facadeSeq struct{ model.BaseChatModel }

func (f *facadeSeq) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done",
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}, nil
}
