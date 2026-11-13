// Package mcpserver L1/L2 门面：Argus as MCP Server（design §9.2 方向 B）。
// 工具注册与 streamable http 传输见 server.go（v3.1 已实装，mark3labs/mcp-go）。
// 注意：方向 A（toolpool，我们调用别人）在 internal/toolpool——两方向共享协议但分层不同，不合流。

package mcpserver

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
