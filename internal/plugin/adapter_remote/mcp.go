// mcp.go remote 插件的 MCP client（design §3.5 方向 B 消费侧）。
// streamable http 传输；每任务建短连接（Initialize→CallTool→Close），
// 不做连接池——插件调用频率低，复用池化反而引入跨任务状态（§8 反过度设计）。
//
// mcp 协议按 manifest.Needs.WorkingCopy 分流两条 wire 契约（2026-09-30 联调
// 实锤，均为生产形态，缺一不可）：
//   - working-copy（ocrshim sidecar）：argus 备工作副本，单 payload 参数下发
//     workcopy_dir，对端目录内执行 OCR 同步回 findings JSON（旧契约，shim 无
//     平台凭证自拉 diff 的能力，必须由 argus 喂目录）。
//   - 非 working-copy（review-service mcp-server）：对端按 provider/repo/
//     head_sha 自拉 PR，create_review（wait 同步等）→ get_review_findings
//     工具编排，findings 走独立工具。
//
// 分流键取 needs 而非 tool 名：需要工作副本的对端必然消费 payload.workcopy_dir，
// 这是两形态的语义分界，tool 名只是配置巧合。
package adapter_remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"git.example.com/enjoydream/agentkit/logredact"

	"argus/internal/plugin"
)

// mcpSyncWaitSec create_review 同步等待秒数（服务端接受 [10,600]，600 为其上限）。
// review-service 单 MR 审查数分钟内完成；插件 manifest timeout 应不小于此值，
// 否则任务级 ctx 先取消、服务端 review 留在 DB running。
const mcpSyncWaitSec = 600

func (r *Remote) invokeMCP(ctx context.Context, task plugin.Task, workcopyDir string) (plugin.Result, error) {
	opts := []transport.StreamableHTTPCOption{}
	if r.AuthToken != "" {
		opts = append(opts, transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + r.AuthToken,
		}))
	}
	c, err := client.NewStreamableHttpClient(r.Endpoint, opts...)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("MCP client 构造失败: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Start(ctx); err != nil {
		return plugin.Result{}, fmt.Errorf("MCP 连接失败（%s）: %w", r.Endpoint, err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "argus", Version: "1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return plugin.Result{}, fmt.Errorf("MCP 初始化失败: %w", err)
	}

	// working-copy sidecar（ocrshim）：旧 payload 契约，目录即入参。
	if r.manifest.Needs.WorkingCopy {
		_, payload, err := buildPayload(task, workcopyDir)
		if err != nil {
			return plugin.Result{}, fmt.Errorf("构造 payload 失败: %w", err)
		}
		joined, err := r.mcpCall(ctx, c, r.Tool, map[string]any{"payload": payload})
		if err != nil {
			return plugin.Result{}, err
		}
		return parseFindings(joined)
	}

	// mcp-server 编排契约：平铺参数创建审查，findings 走独立工具。
	created, err := r.mcpCreateReview(ctx, c, task)
	if err != nil {
		return plugin.Result{}, err
	}
	if created.ReviewID == "" {
		return plugin.Result{}, fmt.Errorf("MCP create_review 响应缺 review_id: %.200s", logredact.Redact(created.raw))
	}
	raw, err := r.mcpCall(ctx, c, "get_review_findings", map[string]any{"review_id": created.ReviewID})
	if err != nil {
		return plugin.Result{}, err
	}
	return mapMCPFindings(raw, created.Summary)
}

// mcpCreated create_review 同步响应的 argus 消费字段（服务端 JSONResult 子集：
// 不含 findings 数组，只有 findings_count——明细必须再调 get_review_findings）。
type mcpCreated struct {
	ReviewID string `json:"review_id"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	raw      string
}

// mcpCreateReview 平铺 reviewRequestSchema 参数创建审查。GitLab 的 MR 编号与
// GitHub/Gitea 系的 PR 编号是两套语义，服务端按平台取对应字段，故按平台分流。
// wait=true + timeout_seconds：服务端同步等审查完成再返回；其 wait 超时
// （wait_timeout，review 仍在后台跑）按插件失败上抛——降级走 Merger 部分失败
// 容忍 + failed-retry 下轮重试，不在此自建轮询状态机。
func (r *Remote) mcpCreateReview(ctx context.Context, c *client.Client, task plugin.Task) (mcpCreated, error) {
	args := map[string]any{
		"provider":        strings.ToLower(task.PR.Platform),
		"repo":            task.PR.Owner + "/" + task.PR.Repo,
		"head_sha":        task.PR.HeadSHA,
		"wait":            true,
		"timeout_seconds": mcpSyncWaitSec,
	}
	if task.PR.Number != "" {
		if strings.EqualFold(task.PR.Platform, "gitlab") {
			args["merge_request_iid"] = task.PR.Number
		} else {
			args["pull_request_id"] = task.PR.Number
		}
	}
	if task.PR.Author != "" {
		args["author"] = task.PR.Author
	}
	raw, err := r.mcpCall(ctx, c, r.Tool, args)
	if err != nil {
		return mcpCreated{}, err
	}
	var out mcpCreated
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return mcpCreated{}, fmt.Errorf("create_review 响应非 JSON: %w（原文 %.200s）", err, logredact.Redact(raw))
	}
	out.raw = raw
	return out, nil
}

// mcpFinding get_review_findings 单条裁剪 finding（服务端 trimFindingForLLM 形态）。
type mcpFinding struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion"`
}

// mapMCPFindings 服务端 findings JSON → 与 builtin/cli 同 schema 的 domain.Finding。
// Title/Body 拼回单条 comment（Title 与 Body 相同时不重拼）；Summary 作
// plugin.Result.Notes（服务端 findings 响应无独立 notes 字段）。
func mapMCPFindings(raw, summary string) (plugin.Result, error) {
	var resp struct {
		Findings []mcpFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return plugin.Result{}, fmt.Errorf("get_review_findings 响应非 JSON: %w（原文 %.200s）", err, logredact.Redact(raw))
	}
	out := plugin.Result{Notes: summary}
	for _, f := range resp.Findings {
		comment := f.Body
		if f.Title != "" && f.Title != f.Body {
			comment = f.Title + "\n\n" + f.Body
		}
		out.Findings = append(out.Findings, plugin.Finding{
			File:         f.Path,
			Line:         f.Line,
			Severity:     f.Severity,
			Comment:      comment,
			By:           "remote",
			SuggestedFix: f.Suggestion,
		})
	}
	return out, nil
}

// mcpCall 单次工具调用：拼接 text content，服务端工具错误（IsError）上抛为
// 插件失败。错误文本可能含对端回显的带凭证 URL，出日志/报告前统一 Redact。
func (r *Remote) mcpCall(ctx context.Context, c *client.Client, name string, args map[string]any) (string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("MCP 工具 %s 调用失败: %w", name, err)
	}
	var texts []string
	for _, content := range res.Content {
		if tc, ok := mcp.AsTextContent(content); ok {
			texts = append(texts, tc.Text)
		}
	}
	joined := textFromContents(texts)
	if res.IsError {
		return "", fmt.Errorf("MCP 工具 %s 返回错误: %.200s", name, logredact.Redact(joined))
	}
	return joined, nil
}
