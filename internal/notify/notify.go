// Package notify L1 扩展：审查终态 IM 推送（v3.1，经 octopus 消息平台）。
// 调 octopus /api/v2/send/quick（X-User-Id 机器账号头，octopus AuthMiddleware
// 按 header 自动建账号）。未配置 ARGUS_OCTOPUS_URL 时禁用（零调用）。
// HTTP 层曾走 ekit network/httpx；v0.29.0 移除 OnBeforeRequest 钩子后无默认
// 请求头注入口，X-User-Id 身份语义不可丢，故回到本包专用客户端（单一固定
// 上游、共享连接池、10s 超时，行为与 httpx 时期一致）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"argus/internal/obs"

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
	client *octoClient
}

// octoClient octopus 专用 HTTP 客户端（连接池共享，10s 超时）。
type octoClient struct {
	hc *http.Client
}

// sendPost POST JSON 并按状态码分支（非 2xx 归为 send_rejected，带状态码）。
func (c *octoClient) sendPost(ctx context.Context, baseURL, token, machineUser, path string, body any) (int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", machineUser)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
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
	if code, err := n.clientOf().sendPost(ctx, n.BaseURL, n.Token, n.machineUser(), "/api/v2/send/quick", map[string]any{
		"channelId": n.ChannelID,
		"content":   content,
		"msgType":   "markdown",
	}); err != nil {
		obs.IncrNotifyFailure("error")
		n.Log.Warn("argus.notify.send_failed", "task", e.TaskID, "error", err.Error())
	} else if code < 200 || code > 299 {
		obs.IncrNotifyFailure(fmt.Sprintf("%d", code))
		n.Log.Warn("argus.notify.send_rejected", "task", e.TaskID, "status", code)
	}
}

// NotifyRaw 通用 markdown 推送（G5 健康报告等周期性内容；空内容不推送）。
func (n *Notifier) NotifyRaw(ctx context.Context, content string) {
	if n == nil || n.BaseURL == "" || n.Log == nil || strings.TrimSpace(content) == "" {
		return
	}
	if code, err := n.clientOf().sendPost(ctx, n.BaseURL, n.Token, n.machineUser(), "/api/v2/send/quick", map[string]any{
		"channelId": n.ChannelID,
		"content":   content,
		"msgType":   "markdown",
	}); err != nil {
		obs.IncrNotifyFailure("error")
		n.Log.Warn("argus.notify.raw_failed", "error", err.Error())
	} else if code < 200 || code > 299 {
		obs.IncrNotifyFailure(fmt.Sprintf("%d", code))
		n.Log.Warn("argus.notify.raw_rejected", "status", code)
	}
}

// clientOf 惰性构建 octopus 客户端：10s 超时 + X-User-Id 机器账号头 + 可选 Bearer
// （连接池进程级共享；X-User-Id 为 octopus AuthMiddleware 的建账号语义，必带）。
func (n *Notifier) clientOf() *octoClient {
	n.once.Do(func() {
		n.client = &octoClient{hc: &http.Client{Timeout: 10 * time.Second}}
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
		// v3.8.0：FailReason 可能含上游错误串（端点/URL 细节），IM 推送截断防泄露长串
		reason := e.FailReason
		if rc := utf8.RuneCountInString(reason); rc > 300 {
			r := []rune(reason)
			reason = string(r[:300]) + "…"
		}
		return fmt.Sprintf("**Argus 审查失败** %s\n- 原因: %s\n- task: %s", pr, reason, e.TaskID)
	default:
		return "" // pending/running 不推送
	}
}
