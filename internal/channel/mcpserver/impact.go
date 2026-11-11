// impact.go —— MCP get_impact（v3.183.0）：改动影响面快照只读工具。
// blast radius 一手结构化数据（nodes/edges/packages/risk/coupling + 预渲染
// mermaid），外部 AI/IDE 直取直渲染，不必解析报告 markdown。
package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ImpactViewer 可选扩展（v3.183.0）：get_impact 的数据面。*service.Service 实现
// （无任务/域外/无快照返回 false，工具侧给可读回执不报错——copilot 同纪律）。
type ImpactViewer interface {
	// ImpactView 任务影响面快照（map 形态，附 task_id/task_status）。
	ImpactView(ctx context.Context, taskID string) (map[string]any, bool)
}

// NewImpactTool 注册 get_impact（New 内调用；core 未实现 ImpactViewer 时跳过
// 注册，tools/list 不出现死工具）。
func NewImpactTool(s *server.MCPServer, core Core) {
	iv, ok := core.(ImpactViewer)
	if !ok {
		return
	}
	s.AddTool(impactTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "task_id")
		if rerr != nil {
			return rerr, nil
		}
		data, ok := iv.ImpactView(ctx, vals[0])
		if !ok {
			return mcp.NewToolResultText("任务不存在或尚无影响面快照（任务 running 早期 / ARGUS_IMPACTVIEW 未开启 / 非本仓任务）。"), nil
		}
		return marshalToolResult(data)
	})
}

func impactTool() mcp.Tool {
	return mcp.NewTool("get_impact",
		mcp.WithDescription("查询 Argus 审查任务的改动影响面快照（分层管线：文件层/import 反向闭包/符号调用方/测试面/历史共变 + 确定性风险分 + 函数级图 diff：Go AST 级、shell/Python/Rust/JavaScript/TypeScript 词法级）。返回 nodes/edges/packages/risk/coupling 结构化 JSON 与预渲染 mermaid，可直接评估 blast radius 或渲染成图。"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}
