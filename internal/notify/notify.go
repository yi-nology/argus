// Package notify L1 扩展：审查终态 IM 推送（v3.1，经 octopus 消息平台）。
// HTTP 层 v3.81.0 下沉 ekit/network/octopus（X-User-Id 机器账号纪律单源）；
// 本包保留任务终态文案渲染与指标记账。未配置 ARGUS_OCTOPUS_URL 时禁用（零调用）。
package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"git.example.com/enjoydream/ekit/network/octopus"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/obs"
)

// Notifier octopus 推送器（nil 安全：nil 接收者直接 no-op）。
type Notifier struct {
	BaseURL   string // 如 http://octopus.svc（不带尾斜杠）
	ChannelID int64  // octopus 渠道 ID（企微/飞书/钉钉机器人渠道）
	User      string // 机器账号名（默认 argus-bot）
	Token     string // 可选 Bearer（octopus 前置网关需要时用）
	Log       logx.Logger

	once   sync.Once
	client *octopus.Client
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
	n.send(ctx, "argus.notify.send", e.TaskID, content)
}

// NotifyRaw 通用 markdown 推送（G5 健康报告等周期性内容；空内容不推送）。
func (n *Notifier) NotifyRaw(ctx context.Context, content string) {
	if n == nil || n.BaseURL == "" || n.Log == nil || strings.TrimSpace(content) == "" {
		return
	}
	n.send(ctx, "argus.notify.raw", "", content)
}

// send 推送执行体（HTTP 层 ekit/network/octopus；状态码/错误在此记账）。
func (n *Notifier) send(ctx context.Context, logKey, taskID, content string) {
	n.once.Do(func() {
		n.client = octopus.New(n.BaseURL, n.ChannelID, n.machineUser(), n.Token, 10*time.Second, nil)
	})
	code, err := n.client.SendMarkdown(ctx, content)
	switch {
	case err != nil:
		obs.IncrNotifyFailure("error")
		n.logWarn(logKey+"_failed", taskID, err.Error())
	case code < 200 || code > 299:
		obs.IncrNotifyFailure(fmt.Sprintf("%d", code))
		n.logWarn(logKey+"_rejected", taskID, fmt.Sprintf("%d", code))
	}
}

func (n *Notifier) logWarn(msg, taskID, detail string) {
	if taskID != "" {
		n.Log.Warn(msg, "task", taskID, "error", detail)
		return
	}
	n.Log.Warn(msg, "detail", detail)
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
