// Package sink 审查结果上报（push 模型旁路）：审查完成后把任务 + 合并后 findings
// POST 给 review-service 的承接端点（POST /api/v1/argus/reviews，契约见
// review-service biz/handler/argus_service），供其前端「Argus 审查」页展示。
//
// 旁路纪律：上报失败只告警不阻断审查主流程——调用方（runner）用短超时 ctx +
// 后台 goroutine 触发，错误不回流审查管线。
package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"
)

// 硬上限：payload 意见条数与单次超时（超限截断并标记，防异常刷爆对端）。
// URL 来自部署方配置（ARGUS_SINK_URL，运维信任源），非用户可控输入。
const (
	MaxFindings    = 500
	defaultTimeout = 10 * time.Second
)

// Reporter 结果上报器（nil = 关闭）。
type Reporter struct {
	URL   string // 承接端点，如 http://review-service:8000/api/v1/argus/reviews
	Token string // Bearer 凭证（ARGUS_SINK_TOKEN；空 = 不带 Authorization 头）
	Log   logx.Logger
}

// NewReporter 构造上报器（bootstrap 装配用）。
func NewReporter(url, token string, log logx.Logger) *Reporter {
	return &Reporter{URL: url, Token: token, Log: log}
}

// Payload 上报契约（与 review-service ingestReq 同构）。
type Payload struct {
	ArgusTaskID string    `json:"argus_task_id"`
	Platform    string    `json:"platform"`
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Number      string    `json:"number"`
	Title       string    `json:"title"`
	HeadSHA     string    `json:"head_sha"`
	BaseBranch  string    `json:"base_branch,omitempty"`
	Verdict     string    `json:"verdict"`
	Findings    []Finding `json:"findings"`
	Plugins     []string  `json:"plugins,omitempty"`
	DurationMS  int64     `json:"duration_ms,omitempty"`
	TokenUsage  int       `json:"token_usage,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Finding 单条意见（severity 为 argus 归一化词表，对端校验同词表）。
type Finding struct {
	FP           string `json:"fp,omitempty"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	Severity     string `json:"severity"`
	Comment      string `json:"comment"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	By           string `json:"by,omitempty"`
}

// Enabled 是否已配置上报端点。
func (r *Reporter) Enabled() bool { return r != nil && r.URL != "" }

// BuildPayload 从审查输出组装上报契约。
// findings 取合并且带规范指纹的条目（Merger 写定 FP；无 FP 的旧数据照报，对端兜底）。
// 意见超 MaxFindings 截断（保序，前面的优先）。
func BuildPayload(taskID string, pr *plugin.PRContext, findings []plugin.Finding,
	verdict string, plugins []string, durationMS int64, tokenUsage int, occurredAt time.Time) *Payload {
	p := &Payload{
		ArgusTaskID: taskID,
		Platform:    pr.Platform, Owner: pr.Owner, Repo: pr.Repo, Number: pr.Number,
		Title: pr.Title, HeadSHA: pr.HeadSHA, BaseBranch: pr.BaseBranch,
		Verdict: verdict, Plugins: plugins,
		DurationMS: durationMS, TokenUsage: tokenUsage, OccurredAt: occurredAt,
		Findings: make([]Finding, 0, len(findings)),
	}
	for i, f := range findings {
		if i >= MaxFindings {
			break
		}
		p.Findings = append(p.Findings, Finding{
			FP: f.FP, File: f.File, Line: f.Line, Severity: f.Severity,
			Comment: f.Comment, SuggestedFix: f.SuggestedFix, By: f.By,
		})
	}
	return p
}

// Report 上报（带 10s 超时 + 1 次重试；错误仅记日志）。
func (r *Reporter) Report(payload *Payload) {
	if !r.Enabled() {
		return
	}
	go r.reportWithRetry(payload)
}

func (r *Reporter) reportWithRetry(payload *Payload) {
	body, err := json.Marshal(payload)
	if err != nil {
		r.logWarn("序列化失败", err)
		return
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if lastErr = r.post(body); lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	r.logWarn("上报失败（重试耗尽）", lastErr)
}

func (r *Reporter) post(body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sink 返回 %s", resp.Status)
	}
	return nil
}

func (r *Reporter) logWarn(msg string, err error) {
	if r.Log != nil {
		r.Log.Warn("argus.sink.report_failed", "reason", msg, "error", err.Error())
	}
}
