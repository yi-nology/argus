// Package mcpserver L1/L2 门面：Argus as MCP Server（design §9.2 方向 B，v3.1 实装）。
// 把 Argus 的审查能力以 MCP 工具暴露给任意 MCP 客户端（review-service、agent 工具链等）：
//
//	review_pr               发起审查（可选等待终态，返回 verdict 与完整报告）
//	requirement_compliance  同 review_pr，kind 固定为需求复核
//	list_agents             插件清单/仓库配置视图
//	get_report              按 taskId 取报告
//
// 传输：mark3labs/mcp-go streamable http，经 Hertz 桥接挂在 /mcp（GET/POST/DELETE），
// 鉴权与 🔒 路由一致（Bearer）。与 toolpool（方向 A：我们调用别人）分层不同，不合流。
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	api "argus/biz/model/api"
)

// Core Argus 能力面（由 *service.Service 实现，无 hertz 依赖）。
type Core interface {
	CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
		artifacts map[string]string) (*api.CreateTaskResponse, int)
	GetTask(ctx context.Context, taskID string) (*api.GetTaskResponse, int)
	ListAgents(ctx context.Context, platform, owner, repo string) *api.ListAgentsResponse
}

// New 构造 MCPServer 并注册四工具。
func New(core Core, version string) *server.MCPServer {
	s := server.NewMCPServer("argus", version,
		server.WithToolCapabilities(false),
	)
	s.AddTool(reviewPrTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kind := argString(req, "kind", "requirement_compliance")
		return runReview(ctx, core, req, kind)
	})
	s.AddTool(requirementComplianceTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runReview(ctx, core, req, "requirement_compliance")
	})
	s.AddTool(listAgentsTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data := core.ListAgents(ctx,
			argString(req, "platform", ""), argString(req, "owner", ""), argString(req, "repo", ""))
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	})
	s.AddTool(getReportTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := req.RequireString("task_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		t, code := core.GetTask(ctx, taskID)
		if code != http.StatusOK {
			return mcp.NewToolResultError(fmt.Sprintf("查询失败（HTTP %d）", code)), nil
		}
		out := map[string]any{
			"task_id": t.TaskId, "status": t.Status,
		}
		if t.Verdict != nil {
			out["verdict"] = *t.Verdict
		}
		if t.Report != nil {
			out["report"] = *t.Report
		}
		if t.FailReason != nil {
			out["fail_reason"] = *t.FailReason
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})
	return s
}

// ---------- 工具定义 ----------

func reviewPrTool() mcp.Tool {
	return mcp.NewTool("review_pr",
		mcp.WithDescription("Argus 审查指定 PR：需求复核（R1→R5）+ 专家插件意见合并，回帖报告到 PR。wait=true 时阻塞至终态并返回结论与报告"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("kind", mcp.Description("审查类型：requirement_compliance（默认）| code_review | visual_check"),
			mcp.WithStringEnumItems([]string{"requirement_compliance", "code_review", "visual_check"})),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func requirementComplianceTool() mcp.Tool {
	return mcp.NewTool("requirement_compliance",
		mcp.WithDescription("Argus 需求复核：解析需求 issue/描述 → 逐条判定验收清单完成度（含 scope creep）→ 确定性裁决。等价 review_pr(kind=requirement_compliance)"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("列出 Argus 全部审查插件（名称/形态/技能/生效状态；platform+owner+repo 三参全传时附带该仓库 .reviewagents.yaml 配置视图与信任边界告警）"),
		mcp.WithString("platform", mcp.Description("可选：平台")),
		mcp.WithString("owner", mcp.Description("可选：仓库所属空间")),
		mcp.WithString("repo", mcp.Description("可选：仓库名")),
	)
}

func getReportTool() mcp.Tool {
	return mcp.NewTool("get_report",
		mcp.WithDescription("按 taskId 获取 Argus 审查结果：状态/verdict/完整报告 markdown"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}

// ---------- 工具执行 ----------

// runReview 建任务 →（可选）轮询至终态 → 结构化结论。
func runReview(ctx context.Context, core Core, req mcp.CallToolRequest, kind string) (*mcp.CallToolResult, error) {
	platform, err := req.RequireString("platform")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	owner, err := req.RequireString("owner")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	repo, err := req.RequireString("repo")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	number, err := req.RequireString("number")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	wait := true
	if b, ok := req.GetArguments()["wait"].(bool); ok {
		wait = b
	}
	timeout := 180 * time.Second
	if f, ok := req.GetArguments()["timeout_seconds"].(float64); ok && f > 0 {
		timeout = time.Duration(f * float64(time.Second))
	}
	// 上界 clamp：无上界的 wait 会变成不可回收的轮询协程（客户端断连也要到点才停），
	// 并发挂起会挤占单连接 SQLite。需要更长等待请用 wait=false + get_report 轮询。
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}

	created, code := core.CreateTaskDirect(ctx, kind, platform, owner, repo, number, nil)
	if code != http.StatusOK {
		return mcp.NewToolResultError(fmt.Sprintf("创建任务失败（HTTP %d），检查平台是否接入/参数是否合法", code)), nil
	}
	taskID := created.TaskId
	if !wait {
		b, _ := json.Marshal(map[string]any{"task_id": taskID, "status": created.Status})
		return mcp.NewToolResultText(string(b)), nil
	}

	deadline := time.Now().Add(timeout)
	for {
		t, code := core.GetTask(ctx, taskID)
		if code != http.StatusOK {
			return mcp.NewToolResultError(fmt.Sprintf("查询任务失败（HTTP %d）", code)), nil
		}
		switch t.Status {
		case "done":
			out := map[string]any{
				"task_id": t.TaskId, "status": t.Status,
			}
			if t.Verdict != nil {
				out["verdict"] = *t.Verdict
			}
			if t.Report != nil {
				out["report"] = *t.Report
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		case "failed":
			reason := ""
			if t.FailReason != nil {
				reason = *t.FailReason
			}
			return mcp.NewToolResultError("审查失败: " + reason), nil
		case "stale":
			out := map[string]any{
				"task_id": t.TaskId, "status": t.Status,
				"note": "审查完成但已有更新的提交，报告未回帖（staleness）",
			}
			if t.Report != nil {
				out["report"] = *t.Report
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		}
		if time.Now().After(deadline) {
			b, _ := json.Marshal(map[string]any{
				"task_id": taskID, "status": t.Status,
				"note": "等待超时，任务仍在执行；稍后用 get_report 查询",
			})
			return mcp.NewToolResultText(string(b)), nil
		}
		select {
		case <-ctx.Done():
			return mcp.NewToolResultError("调用方取消"), nil
		case <-time.After(2 * time.Second):
		}
	}
}

// maxWaitTimeout review_pr 等待终态的时长上界。
const maxWaitTimeout = 10 * time.Minute

// argString 取可选字符串参数。
func argString(req mcp.CallToolRequest, key, def string) string {
	if v, ok := req.GetArguments()[key].(string); ok && v != "" {
		return v
	}
	return def
}

// ---------- Hertz 桥接 ----------

// HertzBridge 把 mcp-go 的 streamable http handler 桥接为 hertz handler。
// 桥接保留响应头（Mcp-Session-Id 等）与状态码；GET 长连流不支持服务端推送刷新
// （工具调用为 POST 一问一答，不受影响）。
func HertzBridge(mcpSrv *server.MCPServer) app.HandlerFunc {
	inner := server.NewStreamableHTTPServer(mcpSrv)
	return func(ctx context.Context, c *app.RequestContext) {
		req, err := http.NewRequest(string(c.Method()), string(c.URI().RequestURI()), bytes.NewReader(c.Request.Body()))
		if err != nil {
			c.SetStatusCode(http.StatusBadRequest)
			return
		}
		// 传播 hertz 请求的 ctx：客户端断连/超时能取消 inner handler（runReview 轮询随之退出），
		// 否则 ctx 恒为 background，取消永不传播。
		req = req.WithContext(ctx)
		req.URL.Scheme = "http"
		req.URL.Host = string(c.Host())
		c.Request.Header.VisitAll(func(k, v []byte) {
			req.Header.Add(string(k), string(v))
		})
		w := &bridgeWriter{c: c, header: http.Header{}}
		inner.ServeHTTP(w, req)
		for k, vs := range w.header {
			for _, v := range vs {
				c.Header(k, v)
			}
		}
	}
}

// bridgeWriter net/http ResponseWriter → hertz RequestContext 桥。
type bridgeWriter struct {
	c           *app.RequestContext
	header      http.Header
	wroteHeader bool
}

func (w *bridgeWriter) Header() http.Header { return w.header }

func (w *bridgeWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.c.Write(p)
}

func (w *bridgeWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.c.SetStatusCode(code)
	}
}

// Flush 流式刷新占位：工具调用为 POST 一问一答，无需真实刷新。
func (w *bridgeWriter) Flush() {}
