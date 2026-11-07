// model_retry.go —— ReAct/直调出口的退避重试装饰层（v5.5.3）。
//
// 背景：Client.generateRetry 的韧性重试环只覆盖单轮 Generator 路径；ReAct 走
// eino ChatModel 直调旁路（agentrun），一发 429 即整轮阵亡（MR186 实弹：R3 首轮
// 429 → 9 条 AC 全部作废降级，security ReAct 同秒阵亡落单轮）。分钟级 RPM 限流
// 下"重试即愈"，缺的只是这一环。
//
// 纪律：错误分类复用 llm.ClassifyLLMError 单源（与单轮同语义）；Stream 只在
// "调用即失败"（未吐任何内容）时重发——流中途失败不可安全重放，原样透传。
// 退避曲线对齐 Client：429 下限 5s、指数、单边向上 jitter、封顶 ceil。

package bootstrap

import (
	"context"
	"fmt"
	mrand "math/rand"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"
	"github.com/oekit/logx"
)

// retryChatModel 包一层 BaseChatModel：可重试错误按退避重发至 attempts 次。
type retryChatModel struct {
	einomodel.BaseChatModel
	attempts int
	base     time.Duration
	ceil     time.Duration
	stage    string
	log      logx.Logger
}

var _ einomodel.ToolCallingChatModel = (*retryChatModel)(nil)

// newRetryChatModel attempts<=1 时直通不包（省一层调用栈与断言面）。
func newRetryChatModel(inner einomodel.BaseChatModel, attempts int,
	base, ceil time.Duration, stage string, log logx.Logger) einomodel.BaseChatModel {
	if inner == nil || attempts <= 1 {
		return inner
	}
	if ceil <= 0 {
		ceil = 30 * time.Second
	}
	return &retryChatModel{BaseChatModel: inner, attempts: attempts,
		base: base, ceil: ceil, stage: stage, log: log}
}

func (m *retryChatModel) Generate(ctx context.Context, input []*schema.Message,
	opts ...einomodel.Option) (*schema.Message, error) {
	var last error
	for attempt := 1; attempt <= m.attempts; attempt++ {
		out, err := m.BaseChatModel.Generate(ctx, input, opts...)
		if err == nil {
			return out, nil
		}
		last = err
		if !llm.ClassifyLLMError(err).Retryable || attempt == m.attempts {
			return nil, err
		}
		if werr := m.wait(ctx, attempt, err); werr != nil {
			return nil, werr
		}
	}
	return nil, last
}

func (m *retryChatModel) Stream(ctx context.Context, input []*schema.Message,
	opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	var last error
	for attempt := 1; attempt <= m.attempts; attempt++ {
		st, err := m.BaseChatModel.Stream(ctx, input, opts...)
		if err == nil {
			return st, nil
		}
		last = err
		if !llm.ClassifyLLMError(err).Retryable || attempt == m.attempts {
			return nil, err
		}
		if werr := m.wait(ctx, attempt, err); werr != nil {
			return nil, werr
		}
	}
	return nil, last
}

// WithTools 绑定透传（与 Fit/Continue 同纪律）：绑定产物再包一层重试。
func (m *retryChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	bound, ok := m.BaseChatModel.(einomodel.ToolCallingChatModel)
	if !ok {
		return nil, fmt.Errorf("llm: retry 包装：底层模型 %T 不支持工具绑定", m.BaseChatModel)
	}
	b, err := bound.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &retryChatModel{BaseChatModel: b, attempts: m.attempts,
		base: m.base, ceil: m.ceil, stage: m.stage, log: m.log}, nil
}

// wait 退避等待：指数 + 单边向上 jitter（[d,1.5d)），429 下限 5s，整体封顶 ceil；
// ctx 取消即弃。返回值非 nil 时调用方带原错退出。
func (m *retryChatModel) wait(ctx context.Context, attempt int, err error) error {
	delay := m.base * time.Duration(1<<uint(attempt-1))
	if llm.ClassifyLLMError(err).IsRateLimit {
		if fl := time.Duration(5) * time.Second; delay < fl && fl <= m.ceil {
			delay = fl
		}
	}
	delay += time.Duration(mrand.Int63n(int64(delay/2 + 1)))
	if delay > m.ceil {
		delay = m.ceil
	}
	if m.log != nil {
		m.log.Warn("argus.llm.react_retry", "stage", m.stage, "attempt", attempt,
			"delay_ms", delay.Milliseconds(), "error", err.Error())
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}
