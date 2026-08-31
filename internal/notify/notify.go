// Package notify L1 扩展：审查终态 IM 推送（v3.1，经 octopus 消息平台）。
// 调 octopus /api/v2/send/quick（X-User-Id 机器账号头，octopus AuthMiddleware
// 按 header 自动建账号）。未配置 ARGUS_OCTOPUS_URL 时禁用（零调用）。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
)

// Notifier octopus 推送器（nil 安全：nil 接收者直接 no-op）。
type Notifier struct {
	BaseURL   string // 如 http://octopus.svc（不带尾斜杠）
	ChannelID int64  // octopus 渠道 ID（企微/飞书/钉钉机器人渠道）
	User      string // 机器账号名（默认 argus-bot）
	Token     string // 可选 Bearer（octopus 前置网关需要时用）
	Client    *http.Client
	Log       logx.Logger
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
	body, _ := json.Marshal(map[string]any{
		"channelId": n.ChannelID,
		"content":   content,
		"msgType":   "markdown",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(n.BaseURL, "/")+"/api/v2/send/quick", bytes.NewReader(body))
	if err != nil {
		n.Log.Warn("argus.notify.build_failed", "error", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", n.machineUser())
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		n.Log.Warn("argus.notify.send_failed", "task", e.TaskID, "error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		n.Log.Warn("argus.notify.send_rejected", "task", e.TaskID, "status", resp.StatusCode)
	}
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
