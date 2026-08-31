package llm

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func msg(role schema.RoleType, content string) *schema.Message {
	return &schema.Message{Role: role, Content: content}
}

// TestFitInput_NoOp 输入未超限时不做任何截断。
func TestFitInput_NoOp(t *testing.T) {
	c := &Client{ContextTokens: 1_000_000, MaxOutputTokens: 50_000}
	msgs := []*schema.Message{msg(schema.User, "hello")}
	c.fitInput(msgs)
	if msgs[0].Content != "hello" {
		t.Fatalf("未超限不应截断: %q", msgs[0].Content)
	}
}

// TestFitInput_Disabled ContextTokens=0 时不启用。
func TestFitInput_Disabled(t *testing.T) {
	c := &Client{ContextTokens: 0}
	msgs := []*schema.Message{msg(schema.User, "hello")}
	c.fitInput(msgs)
	if msgs[0].Content != "hello" {
		t.Fatalf("disabled 不应截断")
	}
}

// TestFitInput_TruncatesLongestUser 超限时截断最长的 user 消息。
func TestFitInput_TruncatesLongestUser(t *testing.T) {
	// 限制：窗口1000 token，输出预留100 → 输入上限= (1000-100)*2*0.9=1620 chars
	c := &Client{ContextTokens: 1000, MaxOutputTokens: 100}
	short := "短"
	long := string(make([]byte, 2000)) //2000 bytes >1620 limit
	msgs := []*schema.Message{
		msg(schema.System, "你是审查专家"),
		msg(schema.User, short),
		msg(schema.User, long),
	}
	c.fitInput(msgs)
	if len(msgs[0].Content) != len("你是审查专家") {
		t.Fatalf("system 不应截断")
	}
	if msgs[1].Content != short {
		t.Fatalf("短 user 不应截断")
	}
	if len(msgs[2].Content) >= len(long) {
		t.Fatalf("长 user 应截断")
	}
}

// TestFitInput_OnlyUser 最长消息是 system 时不截断（只截 user）。
func TestFitInput_OnlyUser(t *testing.T) {
	c := &Client{ContextTokens: 100, MaxOutputTokens: 10}
	big := string(make([]byte, 2000))
	msgs := []*schema.Message{
		msg(schema.System, big), // system 是最长的
		msg(schema.User, "hi"),
	}
	c.fitInput(msgs)
	if len(msgs[0].Content) != len(big) {
		t.Fatalf("system 不应被截断，只有 user 才截断")
	}
}

// TestRetryableLLMError_ContextLength 上下文超限错误不重试。
func TestRetryableLLMError_ContextLength(t *testing.T) {
	for _, msg := range []string{
		"maximum context length is128000 tokens",
		"This model's maximum context length is128000",
		"request too long",
		"reduce the length of the messages",
		"Input length500000 exceeds max input length",
	} {
		if retryableLLMError(errStr(msg)) {
			t.Fatalf("应不可重试: %q", msg)
		}
	}
	// 可重试：429 rate limit
	if !retryableLLMError(errStr("rate limit exceeded 429")) {
		t.Fatal("429 应可重试")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
