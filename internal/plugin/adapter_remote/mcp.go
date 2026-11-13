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
//     head_sha 自拉 PR，异步编排 create_review(wait=false) → get_review 轮询
//     终态 → get_review_findings 取明细。不走 wait=true 同步等：对端 transport
//     WriteTimeout 60s（215 实测 101s 审查响应必然 EOF），长审查等不起；异步
//     短请求正是 create/get 分离契约的设计意图。
//
// 分流键取 needs 而非 tool 名：需要工作副本的对端必然消费 payload.workcopy_dir，
// 这是两形态的语义分界，tool 名只是配置巧合。

package adapter_remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/oagentkit/guardx/logredact"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// mcpPollInterval get_review 终态轮询间隔。单次查询毫秒级，10s 对分钟级审查
// 足够密；总预算由 manifest.Timeout 框住（ctx 透传）。包级 var 仅为测试可收窄。
var mcpPollInterval = 10 * time.Second

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

	// mcp-server 编排契约：平铺参数创建审查（异步），轮询终态后取 findings。
	created, err := r.mcpCreateReview(ctx, c, task)
	if err != nil {
		return plugin.Result{}, err
	}
	if created.ReviewID == "" {
		return plugin.Result{}, fmt.Errorf("MCP create_review 响应缺 review_id: %s", created.Status)
	}
	final, err := r.mcpWaitReview(ctx, c, created.ReviewID)
	if err != nil {
		return plugin.Result{}, err
	}
	raw, err := r.mcpCall(ctx, c, "get_review_findings", map[string]any{"review_id": created.ReviewID})
	if err != nil {
		return plugin.Result{}, err
	}
	return mapMCPFindings(raw, final.Summary)
}

// mcpReviewStatus 服务端 review 主状态（create.go：queued/running/success/failed，
// 另有 success_with_publish_failed=评审成功仅回写失败，findings 仍可消费）。
type mcpReviewStatus struct {
	ReviewID string `json:"review_id"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
}

// mcpCreated create_review 异步响应的 argus 消费字段（服务端返回 queued +
// review_id，等待由 argus 轮询承担）。
type mcpCreated = mcpReviewStatus

// mcpCreateReview 平铺 reviewRequestSchema 参数创建审查。GitLab 的 MR 编号与
// GitHub/Gitea 系的 PR 编号是两套语义，服务端按平台取对应字段，故按平台分流。
// wait=false：立即返回 review_id（status=queued），不占对端 transport 长请求。
func (r *Remote) mcpCreateReview(ctx context.Context, c *client.Client, task plugin.Task) (mcpCreated, error) {
	args := map[string]any{
		"provider": strings.ToLower(task.PR.Platform),
		"repo":     task.PR.Owner + "/" + task.PR.Repo,
		"head_sha": task.PR.HeadSHA,
		"wait":     false,
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
	return out, nil
}

// mcpWaitReview 轮询 get_review 到终态：success（含 success_with_publish_failed）
// 返回；failed 上抛；预算=任务级 ctx（manifest.Timeout），超时上抛并带 review_id
// 便于人工按 list_reviews 追踪——review 在对端仍在跑，不盲等也不取消。
func (r *Remote) mcpWaitReview(ctx context.Context, c *client.Client, reviewID string) (mcpReviewStatus, error) {
	ticker := time.NewTicker(mcpPollInterval)
	defer ticker.Stop()
	for {
		raw, err := r.mcpCall(ctx, c, "get_review", map[string]any{"review_id": reviewID})
		if err != nil {
			return mcpReviewStatus{}, err
		}
		var st mcpReviewStatus
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return mcpReviewStatus{}, fmt.Errorf("get_review 响应非 JSON: %w（原文 %.200s）", err, logredact.Redact(raw))
		}
		switch st.Status {
		case "success", "success_with_publish_failed":
			return st, nil
		case "failed":
			return mcpReviewStatus{}, fmt.Errorf("mcp-server review %s 失败: %.200s", reviewID, logredact.Redact(st.Summary))
		}
		select {
		case <-ctx.Done():
			return mcpReviewStatus{}, fmt.Errorf("轮询 review %s 超预算（对端仍在跑，可稍后按 list_reviews 追踪）: %w", reviewID, ctx.Err())
		case <-ticker.C:
		}
	}
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
		out.Findings = append(out.Findings, domain.Finding{
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
