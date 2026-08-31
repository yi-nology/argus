// Package adapter_remote remote 形态适配器（design §3.2/§3.5）。
// v3.1 实装协议子集：mcp（mark3labs/mcp-go streamable http client）与
// a2a（Agent Card 发现 → message/send → tasks/get 轮询 artifact）。
// 与远端的调用契约：发送结构化任务 payload，收回 findings JSON
// （{"findings":[{file,line,severity,comment}...],"notes"}），与 builtin/cli 同一 schema。
package adapter_remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"argus/internal/llm"
	"argus/internal/plugin"
)

// payloadBodyMaxChars remote payload 里 PR 描述的截断上限。
// diff 不在此截断（diffprep 已按模型窗口清洗），描述截断只防恶意刷。
const payloadBodyMaxChars = 24_000

// Remote 远端 agent 插件。endpoint/tool 只在 plugins.yaml 声明（§3.4 红线），
// 仓库 .reviewagents.yaml 仅能启用/禁用/排除（与 cli 插件同路径）。
type Remote struct {
	manifest plugin.AgentManifest
	Endpoint string
	Tool     string // mcp 协议：要调用的工具名（缺省 review）
}

// New 按 plugins.yaml 声明构造；protocol 仅接受 a2a|mcp。
func New(m plugin.AgentManifest, protocol plugin.Protocol, endpoint, tool string) (*Remote, error) {
	m.Kind = plugin.KindRemote
	m.Protocol = protocol
	if m.Timeout <= 0 {
		m.Timeout = 180 * time.Second
	}
	if endpoint == "" {
		return nil, fmt.Errorf("remote 插件 %s 缺少 endpoint", m.Name)
	}
	if tool == "" {
		tool = "review"
	}
	return &Remote{manifest: m, Endpoint: endpoint, Tool: tool}, nil
}

func (r *Remote) Manifest() plugin.AgentManifest { return r.manifest }

// Invoke 按协议分发。远端失败返回错误即触发 Merger 部分失败容忍（降级注明）。
func (r *Remote) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.manifest.Timeout)
	defer cancel()
	switch r.manifest.Protocol {
	case plugin.ProtocolMCP:
		return r.invokeMCP(ctx, task)
	case plugin.ProtocolA2A:
		return r.invokeA2A(ctx, task)
	default:
		return plugin.Result{}, fmt.Errorf("remote 插件 %s 不支持的协议: %s", r.manifest.Name, r.manifest.Protocol)
	}
}

// remotePayload 发给远端的结构化任务（与 builtin/cli 共享的审查输入语义）。
type remotePayload struct {
	Platform      string   `json:"platform"`
	Owner         string   `json:"owner"`
	Repo          string   `json:"repo"`
	Number        string   `json:"number"`
	Title         string   `json:"title"`
	Body          string   `json:"body,omitempty"`
	Author        string   `json:"author,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	HeadSHA       string   `json:"head_sha,omitempty"`
	BaseBranch    string   `json:"base_branch,omitempty"`
	HeadBranch    string   `json:"head_branch,omitempty"`
	Diff          string   `json:"diff,omitempty"`
	ChangeSummary string   `json:"change_summary,omitempty"`
}

func buildPayload(task plugin.Task) (*remotePayload, string, error) {
	p := &remotePayload{
		Platform:      task.PR.Platform,
		Owner:         task.PR.Owner,
		Repo:          task.PR.Repo,
		Number:        task.PR.Number,
		Title:         task.PR.Title,
		Body:          task.PR.Body,
		Author:        task.PR.Author,
		Labels:        task.PR.Labels,
		HeadSHA:       task.PR.HeadSHA,
		BaseBranch:    task.PR.BaseBranch,
		HeadBranch:    task.PR.HeadBranch,
		ChangeSummary: task.Artifacts["change_summary"],
	}
	if t, truncated := truncRunes(p.Body, payloadBodyMaxChars); truncated {
		p.Body = t + "…（截断）"
	}
	if task.Diff != nil {
		p.Diff = task.Diff.Text
	}
	b, err := json.Marshal(p)
	return p, string(b), err
}

// parseFindings 从远端响应文本提取 findings JSON（与 builtin 同 schema）。
func parseFindings(text string) (plugin.Result, error) {
	var out struct {
		Findings []plugin.Finding `json:"findings"`
		Notes    string           `json:"notes"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(text)), &out); err != nil {
		return plugin.Result{}, fmt.Errorf("remote 响应解析失败: %w（原文 %.200s）", err, text)
	}
	for i := range out.Findings {
		if out.Findings[i].By == "" {
			out.Findings[i].By = "remote"
		}
	}
	return plugin.Result{Findings: out.Findings, Notes: out.Notes}, nil
}

// textFromContents 拼接 MCP text content 数组。
func textFromContents(texts []string) string {
	return strings.Join(texts, "\n")
}

func truncRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}
