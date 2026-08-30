// Package mcpserver L1/L2 扩展点：Argus as MCP Server（design §9.2 方向 B）。
// v3.0 只做路由占位（真实工具 requirement_compliance/review_pr/list_agents/get_report
// 照抄 review-service internal/mcp 范式，v3.1 用 mark3labs/mcp-go 实装）。
// 注意：方向 A（toolpool，我们调用别人）在本包之外——两方向共享协议但分层不同，不合流。
package mcpserver

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// StubHandler MCP streamable-http 占位：始终 501，报文声明 v3.1 实装。
func StubHandler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.JSON(501, map[string]any{
			"code":    501,
			"message": "Argus as MCP Server 在 v3.1 实装（占位路由，见 design §9.2 方向 B）",
			"data":    nil,
		})
	}
}

// AgentCardPlaceholder A2A Server 发现文档占位（/.well-known/agent.json，v3.1 实装）。
// 与 OCR sidecar 化后的"远程 OCR"共用同一个 a2a 接口位（design §2）。
func AgentCardPlaceholder(name, version string) map[string]any {
	return map[string]any{
		"name":         name,
		"version":      version,
		"description":  "Argus —— 可插拔多 Agent 审查平台（A2A Server 形态 v3.1 预留）",
		"url":          "",
		"capabilities": map[string]any{"streaming": false, "pushNotifications": false},
		"stub":         true,
	}
}
