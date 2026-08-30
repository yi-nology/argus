// Package llm LLM 客户端薄封装（design §10.2 错误处理矩阵）：
//   - 生成失败：指数退避重试 1 次，仍败上抛（task=failed，不在 PR 评论）；
//   - JSON 输出：解析失败带错误信息重试 1 次，仍败由调用方按阶段降级；
//   - token 记账：优先读响应 usage，缺省按 chars/4 估算；同时累计到任务预算与观测指标。
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/plugin"
)

// Client 阶段级 LLM 调用器。
type Client struct {
	Model     model.BaseChatModel
	ModelName string

	// OnUsage 每次调用后的 token 记账回调（stage, promptTokens, completionTokens）。
	OnUsage func(stage string, prompt, completion int)
	// Budget 任务 token 预算累计器（可 nil）。
	Budget *plugin.TokenBudget

	retryDelay time.Duration
}

func NewClient(m model.BaseChatModel, modelName string, budget *plugin.TokenBudget) *Client {
	return &Client{Model: m, ModelName: modelName, Budget: budget, retryDelay: 2 * time.Second}
}

// SetRetryDelay 测试用。
func (c *Client) SetRetryDelay(d time.Duration) { c.retryDelay = d }

func estimateTokens(s string) int { return len(s) / 4 }

func (c *Client) account(stage string, in []*schema.Message, out *schema.Message) {
	var p, comp int
	if out != nil && out.ResponseMeta != nil && out.ResponseMeta.Usage != nil {
		u := out.ResponseMeta.Usage
		p, comp = u.PromptTokens, u.CompletionTokens
	} else {
		for _, m := range in {
			p += estimateTokens(m.Content)
		}
		if out != nil {
			comp = estimateTokens(out.Content)
		}
	}
	if c.OnUsage != nil {
		c.OnUsage(stage, p, comp)
	}
	if c.Budget != nil {
		c.Budget.Add(p + comp)
	}
	plugin.ReportLLMTokens(stage, c.ModelName, p+comp)
}

// Generate 带重试的普通生成（指数退避 1 次；stage 仅用于记账/指标）。
func (c *Client) Generate(ctx context.Context, stage string, msgs []*schema.Message) (*schema.Message, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay): // 指数退避
			}
		}
		out, err := c.Model.Generate(ctx, msgs)
		if err == nil {
			c.account(stage, msgs, out)
			return out, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("llm: %s 调用失败（已重试 1 次）: %w", stage, lastErr)
}

// GenerateJSON 生成并解析 JSON；解析失败把原始输出与错误回喂重试 1 次（§10.2 R3 行）。
// out 必须是 *T。生成失败直接上抛（重试额度在 Generate 内已耗尽）。
func (c *Client) GenerateJSON(ctx context.Context, stage string, msgs []*schema.Message, out any) error {
	var lastErr error
	lastRaw := ""
	for attempt := 0; attempt < 2; attempt++ {
		callMsgs := msgs
		if attempt > 0 {
			callMsgs = append(append([]*schema.Message{}, msgs...),
				&schema.Message{Role: schema.Assistant, Content: lastRaw},
				schema.UserMessage(fmt.Sprintf(
					"你上一轮的输出无法解析为合法 JSON（错误：%v）。\n请重新输出，且只输出合法 JSON：不要解释、不要 markdown 代码围栏。", lastErr)),
			)
		}
		resp, err := c.Generate(ctx, stage, callMsgs)
		if err != nil {
			return err
		}
		raw := ExtractJSON(resp.Content)
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			lastErr = err
			lastRaw = resp.Content
			continue
		}
		return nil
	}
	return fmt.Errorf("llm: %s JSON 解析失败（已带错误重试 1 次）: %w", stage, lastErr)
}

// ExtractJSON 从模型输出中提取 JSON 文本：剥 ``` 围栏、截取首个 {/[ 到末个 }/]。
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	openCh := s[start]
	closeCh := byte('}')
	if openCh == '[' {
		closeCh = ']'
	}
	rest := s[start:]
	end := strings.LastIndexByte(rest, closeCh)
	if end < 0 {
		return rest
	}
	return rest[:end+1]
}
