package context

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func bigTool(role schema.RoleType, n int) *schema.Message {
	return &schema.Message{Role: role, Content: strings.Repeat("x", n)}
}

func TestFitMessagesPassthrough(t *testing.T) {
	msgs := []*schema.Message{
		bigTool(schema.System, 100),
		bigTool(schema.User, 100),
	}
	out, res := FitMessages(msgs, 10_000)
	same := len(out) == len(msgs) && out[0] == msgs[0] && out[1] == msgs[1]
	if res.Action != FitPass || !same {
		t.Fatalf("未超限应零拷贝直通，got action=%s same=%v", res.Action, same)
	}
}

func TestFitMessagesMicrocompact(t *testing.T) {
	// 8 条大工具结果：清更早 3 条（保最近 5 条）即可入窗
	msgs := []*schema.Message{bigTool(schema.System, 10), bigTool(schema.User, 100)}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, bigTool(schema.Tool, 2000))
	}
	limit := 10 + 100 + 5*2000 + 1000 // 清 3 条后留余量
	out, res := FitMessages(msgs, limit)
	if res.Action != FitMicro {
		t.Fatalf("期望 FitMicro，got %s（before=%d limit=%d）", res.Action, res.Before, res.Limit)
	}
	if res.Cleared != 3 {
		t.Fatalf("期望清理 3 条，got %d", res.Cleared)
	}
	if len(out) != len(msgs) {
		t.Fatal("消息条数必须不变（语法安全）")
	}
	for i := 0; i < 5; i++ {
		if got := out[5+i].Content; got != strings.Repeat("x", 2000) {
			t.Fatalf("最近 5 条不应被清理，第 %d 条 len=%d", i, len(got))
		}
	}
	for i := 0; i < 3; i++ {
		if out[2+i] == msgs[2+i] {
			t.Fatal("被清理消息应为克隆，不得污染调用方")
		}
		if got := out[2+i].Content; got != toolPlaceholder {
			t.Fatalf("旧工具结果应为占位符，got %q", got)
		}
	}
	if msgs[2].Content == toolPlaceholder {
		t.Fatal("不得改写调用方原消息")
	}
}

func TestFitMessagesMicrocompactBelowThresholdAbandoned(t *testing.T) {
	// 唯一旧工具结果太小（清理量 < minClearSavings）：L1 放弃，仅 L2 截 user
	msgs := []*schema.Message{bigTool(schema.User, 6000), bigTool(schema.Tool, 400)}
	out, res := FitMessages(msgs, 4600)
	if res.Action != FitTruncated {
		t.Fatalf("L1 不足应落 L2 截断，got %s", res.Action)
	}
	if res.Cleared != 0 {
		t.Fatalf("L1 放弃则 Cleared 应为 0，got %d", res.Cleared)
	}
	if out[1].Content != msgs[1].Content {
		t.Fatal("L1 放弃且未触发 L3 时工具结果不应被改")
	}
	if len([]rune(out[0].Content)) >= 6000 {
		t.Fatal("user 消息应被截断变短")
	}
}

func TestFitMessagesTruncateLongestUser(t *testing.T) {
	msgs := []*schema.Message{
		bigTool(schema.System, 50),
		bigTool(schema.User, 8000),
		bigTool(schema.Assistant, 50),
	}
	out, res := FitMessages(msgs, 4000)
	if res.Action != FitTruncated {
		t.Fatalf("期望 FitTruncated，got %s", res.Action)
	}
	if len([]rune(out[1].Content)) >= 8000 {
		t.Fatal("user 消息应被截断")
	}
	if !strings.Contains(out[1].Content, truncNote) {
		t.Fatal("截断应带留痕注记")
	}
	if !strings.HasPrefix(out[1].Content, "\n") {
		t.Fatal("截断格式应与 agentkit fitInput 一致（前导换行）")
	}
}

func TestFitMessagesLevel3ToolTruncation(t *testing.T) {
	// user 很小、工具结果巨大且多：L1 清 1 条不够、L2 截 user 也不够 → L3 迭代
	msgs := []*schema.Message{bigTool(schema.User, 100)}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, bigTool(schema.Tool, 3000))
	}
	out, res := FitMessages(msgs, 6000)
	if res.After > res.Limit {
		t.Fatalf("L3 后应入窗，after=%d limit=%d", res.After, res.Limit)
	}
	if res.Action != FitMicroTrunc {
		t.Fatalf("期望 FitMicroTrunc，got %s", res.Action)
	}
	if len(out) != len(msgs) {
		t.Fatal("消息条数必须不变")
	}
}

func TestTokensPerCharCalibration(t *testing.T) {
	calibLock.Lock()
	oldU, oldC := calibUsage, calibChars
	calibUsage, calibChars = 0, 0
	calibLock.Unlock()
	defer func() {
		calibLock.Lock()
		calibUsage, calibChars = oldU, oldC
		calibLock.Unlock()
	}()

	if tokensPerChar() != 0 {
		t.Fatal("无样本应返回 0")
	}
	recordSentChars(1000)
	RecordUsage("any", 800)
	if got := tokensPerChar(); got < 0.79 || got > 0.81 {
		t.Fatalf("系数应为 0.8，got %f", got)
	}
	RecordUsage("any", 9000) // 系数 9 > 4：脏样本
	if tokensPerChar() != 0 {
		t.Fatal("脏样本应返回 0")
	}
}

// ---- FitModel 装饰器 ----

type fakeChatModel struct {
	mu     sync.Mutex
	lastIn []*schema.Message
}

func (f *fakeChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	f.lastIn = input
	f.mu.Unlock()
	return bigTool(schema.Assistant, 5), nil
}

func (f *fakeChatModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	f.lastIn = input
	f.mu.Unlock()
	return nil, errors.New("fake: stream 未实现")
}

func (f *fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

func TestFitModelGenerateFits(t *testing.T) {
	inner := &fakeChatModel{}
	var events []FitResult
	fm := NewFitModel(inner, "R3", "m", 10_000, 0, func(stage string, res FitResult) {
		events = append(events, res)
	})
	msgs := []*schema.Message{bigTool(schema.User, 30_000)}
	out, err := fm.Generate(context.Background(), msgs)
	if err != nil || out == nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	inner.mu.Lock()
	sent := inner.lastIn
	inner.mu.Unlock()
	if len([]rune(sent[0].Content)) >= 30_000 {
		t.Fatal("发送前应完成拟合截断")
	}
	if len(events) != 1 || events[0].Action != FitTruncated {
		t.Fatalf("拟合事件应上报 OnFit，got %v", events)
	}
}

func TestFitModelPassthroughZeroWindow(t *testing.T) {
	inner := &fakeChatModel{}
	fm := NewFitModel(inner, "qa", "m", 0, 0, nil)
	msgs := []*schema.Message{bigTool(schema.User, 100_000)}
	if _, err := fm.Generate(context.Background(), msgs); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	inner.mu.Lock()
	lastIn := inner.lastIn
	inner.mu.Unlock()
	if len([]rune(lastIn[0].Content)) != 100_000 {
		t.Fatal("窗口 ≤0 应完全直通")
	}
}

func TestFitModelWithToolsRewrap(t *testing.T) {
	inner := &fakeChatModel{}
	fm := NewFitModel(inner, "R3", "m", 10_000, 0, nil)
	bound, err := fm.WithTools([]*schema.ToolInfo{})
	if err != nil {
		t.Fatalf("WithTools 失败: %v", err)
	}
	bfm, ok := bound.(*FitModel)
	if !ok {
		t.Fatalf("绑定产物应仍是 FitModel，got %T", bound)
	}
	if bfm.BaseChatModel != model.BaseChatModel(inner) {
		t.Fatal("绑定产物内层应是 WithTools 返回的模型（拟合不因绑定失效）")
	}
}

// 时序不变量（v3.37.0 测试卡点）：拟合只允许改 Tool/User 消息的 Content——
// 消息条数、role 序列、tool_call/tool_result 配对字段必须逐字节保持。
func TestFitMessagesPreservesGrammar(t *testing.T) {
	msgs := []*schema.Message{
		bigTool(schema.System, 50),
		bigTool(schema.User, 500),
		{Role: schema.Assistant, Content: "查看", ToolCalls: []schema.ToolCall{
			{ID: "call1", Function: schema.FunctionCall{Name: "get_file", Arguments: `{"path":"a.go"}`}},
		}},
		bigTool(schema.Tool, 3000),
		{Role: schema.Assistant, Content: "再看"},
		bigTool(schema.Tool, 3000),
	}
	for i := range msgs[3].Content {
		_ = i
	}
	msgs[3].ToolCallID = "call1"
	msgs[5].ToolCallID = "call2"

	out, _ := FitMessages(msgs, 2000)
	if len(out) != len(msgs) {
		t.Fatal("消息条数必须不变")
	}
	for i := range msgs {
		if out[i].Role != msgs[i].Role {
			t.Fatalf("role 序列必须不变，第 %d 条 %s→%s", i, msgs[i].Role, out[i].Role)
		}
		if len(out[i].ToolCalls) != len(msgs[i].ToolCalls) {
			t.Fatalf("ToolCalls 必须不变，第 %d 条", i)
		}
		if out[i].ToolCallID != msgs[i].ToolCallID {
			t.Fatalf("ToolCallID 必须不变，第 %d 条", i)
		}
		if msgs[i].Role != schema.Tool && msgs[i].Role != schema.User && out[i].Content != msgs[i].Content {
			t.Fatalf("非 Tool/User 消息的 Content 不得改动，第 %d 条 role=%s", i, msgs[i].Role)
		}
	}
}
