// Package notify L1 扩展：审查终态 IM 推送（v3.1，经 octopus 消息平台）。
// 调 octopus /api/v2/send/quick（X-User-Id 机器账号头，octopus AuthMiddleware
// 按 header 自动建账号）。未配置 ARGUS_OCTOPUS_URL 时禁用（零调用）。
// HTTP 层走 ekit network/httpx（单一固定上游：统一超时/Bearer/机器账号头）。
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/protocol"

	"argus/internal/obs"

	"git.example.com/enjoydream/ekit/network/httpx"
	"git.example.com/enjoydream/ekit/observability/logx"
)

// Notifier octopus 推送器（nil 安全：nil 接收者直接 no-op）。
type Notifier struct {
	BaseURL   string // 如 http://octopus.svc（不带尾斜杠）
	ChannelID int64  // octopus 渠道 ID（企微/飞书/钉钉机器人渠道）
	User      string // 机器账号名（默认 argus-bot）
	Token     string // 可选 Bearer（octopus 前置网关需要时用）
	Log       logx.Logger

	once   sync.Once
	client *httpx.Client
}

// TaskEvent 终态摘要（runner 装配）。
type TaskEvent struct {
	TaskID     string
	Platform   string
	Owner      string
	Repo       string
	Number     string
	Status     string // done / failed / stale
	Verdict    string
	Rule       string
	Findings   int
	Tokens     int
	Elapsed    time.Duration
	FailReason string
}

// NotifyTerminal 任务终态推送。done/stale 发结论卡，failed 发告警。
func (n *Notifier) NotifyTerminal(ctx context.Context, e TaskEvent) {
	if n == nil || n.BaseURL == "" || n.Log == nil {
		return
	}
	content := renderMarkdown(e)
	if content == "" {
		return
	}
	var out map[string]any
	if err := n.clientOf().PostJSON(ctx, "/api/v2/send/quick", map[string]any{
		"channelId": n.ChannelID,
		"content":   content,
		"msgType":   "markdown",
	}, &out); err != nil {
		var svcErr *httpx.ServiceError
		if errors.As(err, &svcErr) {
			obs.IncrNotifyFailure(fmt.Sprintf("%d", svcErr.Code))
			n.Log.Warn("argus.notify.send_rejected", "task", e.TaskID, "status", svcErr.Code)
			return
		}
		obs.IncrNotifyFailure("error")
		n.Log.Warn("argus.notify.send_failed", "task", e.TaskID, "error", err.Error())
	}
}

// clientOf 惰性构建 httpx 客户端：10s 超时 + X-User-Id 机器账号头 + 可选 Bearer
// （X-User-Id 无对应 Option，经 OnBeforeRequest 注入）。
func (n *Notifier) clientOf() *httpx.Client {
	n.once.Do(func() {
		opts := []httpx.Option{
			httpx.WithBaseURL(strings.TrimRight(n.BaseURL, "/")),
			httpx.WithTimeout(10 * time.Second),
			httpx.WithOnBeforeRequest(func(_ context.Context, r *protocol.Request) error {
				r.Header.Set("X-User-Id", n.machineUser())
				return nil
			}),
		}
		if n.Token != "" {
			opts = append(opts, httpx.WithBearerToken(n.Token))
		}
		n.client = httpx.NewClient("argus-notify", opts...)
	})
	return n.client
}

func (n *Notifier) machineUser() string {
	if n.User != "" {
		return n.User
	}
	return "argus-bot"
}

// renderMarkdown 结论卡（octopus markdown 渠道）。
func renderMarkdown(e TaskEvent) string {
	pr := fmt.Sprintf("%s/%s#%s", e.Owner, e.Repo, e.Number)
	switch e.Status {
	case "done", "stale":
		return fmt.Sprintf("**Argus 审查完成** %s\n- 结论: `%s`（%s）\n- findings: %d\n- 用时: %s / tokens: %d\n- task: %s",
			pr, e.Verdict, e.Rule, e.Findings, e.Elapsed.Round(time.Second), e.Tokens, e.TaskID)
	case "failed":
		return fmt.Sprintf("**Argus 审查失败** %s\n- 原因: %s\n- task: %s", pr, e.FailReason, e.TaskID)
	default:
		return "" // pending/running 不推送
	}
}
