// Package adapter_remote remote 形态适配器（design §3.2/§3.5）。
// v3.1 实装协议子集：mcp（mark3labs/mcp-go streamable http client）与
// a2a（Agent Card 发现 → message/send → tasks/get 轮询 artifact）。
// 返回契约与 builtin/cli 同一 findings schema（{"findings":[{file,line,severity,
// comment}...],"notes"}）：a2a 发结构化任务 payload 收 findings JSON；mcp 走
// review-service mcp-server 编排契约（create_review 平铺参数同步等 →
// get_review_findings 取明细，见 mcp.go）。
package adapter_remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/textutil"
	"git.example.com/enjoydream/agentkit/workcopy"

	"argus/internal/plugin"
)

// payloadBodyMaxChars remote payload 里 PR 描述的截断上限。
// diff 不在此截断（diffprep 已按模型窗口清洗），描述截断只防恶意刷。
const payloadBodyMaxChars = 24_000

// WorkcopyPool remote 插件按需的工作副本能力（*workcopy.Pool 结构性满足）。
// needs:[working-copy] 的 remote 插件（如经 ocrshim 调 OCR）在 Invoke 内
// Ensure/Release——与 cli/acpx 适配器同模式，目录经 payload 下发给远端。
type WorkcopyPool interface {
	Ensure(ctx context.Context, key workcopy.WorktreeKey) (string, error)
	Release(key workcopy.WorktreeKey)
}

// Remote 远端 agent 插件。endpoint/tool 只在 plugins.yaml 声明（§3.4 红线），
// 仓库 .reviewagents.yaml 仅能启用/禁用/排除（与 cli 插件同路径）。
type Remote struct {
	manifest plugin.AgentManifest
	Endpoint string
	Tool     string // mcp 协议：create_review 工具名（review-service mcp-server 契约；缺省 review）
	// AuthToken Bearer 共享密钥（空 = 不带鉴权头；与对端 auth_token_env 配对，
	// 如 ocrshim 的 OCRSHIM_TOKEN）。非 loopback 的 sidecar endpoint（compose
	// 跨容器）网络隔离不足以限定"只有 argus"，建议配置。
	AuthToken string
	wc        WorkcopyPool
}

// New 按 plugins.yaml 声明构造；protocol 仅接受 a2a|mcp。
// 声明 needs:[working-copy] 时 wc 必须就位（否则启动期报错停用）。
func New(m plugin.AgentManifest, protocol plugin.Protocol, endpoint, tool, authToken string, wc WorkcopyPool) (*Remote, error) {
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
	if m.Needs.WorkingCopy && wc == nil {
		return nil, fmt.Errorf("remote 插件 %s 声明 working-copy 但未接入工作副本服务", m.Name)
	}
	return &Remote{manifest: m, Endpoint: endpoint, Tool: tool, AuthToken: authToken, wc: wc}, nil
}

func (r *Remote) Manifest() plugin.AgentManifest { return r.manifest }

// Invoke 按协议分发。远端失败返回错误即触发 Merger 部分失败容忍（降级注明）。
// needs:[working-copy] 时先准备工作副本（浅克隆 + checkout PR head），
// 目录经 payload.workcopy_dir 下发，Invoke 结束释放。
func (r *Remote) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.manifest.Timeout)
	defer cancel()
	dir := ""
	if r.manifest.Needs.WorkingCopy {
		key := plugin.PRToKey(task.PR)
		var err error
		if dir, err = r.wc.Ensure(ctx, key); err != nil {
			return plugin.Result{}, fmt.Errorf("准备工作副本失败: %w", err)
		}
		defer r.wc.Release(key)
	}
	switch r.manifest.Protocol {
	case plugin.ProtocolMCP:
		return r.invokeMCP(ctx, task, dir)
	case plugin.ProtocolA2A:
		return r.invokeA2A(ctx, task, dir)
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
	// WorkcopyDir 工作副本目录（needs:[working-copy] 时下发；远端 sidecar 在此执行）。
	WorkcopyDir string `json:"workcopy_dir,omitempty"`
}

func buildPayload(task plugin.Task, workcopyDir string) (*remotePayload, string, error) {
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
		ChangeSummary: task.Artifacts[plugin.ArtifactChangeSummary],
	}
	p.Body = textutil.TruncNote(p.Body, payloadBodyMaxChars, "截断")
	p.WorkcopyDir = workcopyDir
	if task.Diff != nil {
		p.Diff = task.Diff.Text
	}
	b, err := json.Marshal(p)
	return p, string(b), err
}

// parseFindings 从远端响应文本提取 findings JSON（与 builtin 同 schema）。
func parseFindings(text string) (plugin.Result, error) {
	out, err := plugin.DecodeFindingsJSON(text)
	if err != nil {
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
