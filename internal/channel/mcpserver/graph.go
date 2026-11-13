// graph.go —— MCP get_trace / get_symbol_context（v5.9.0）：工作副本符号图
// 只读工具（数据面=semsearch 符号邻接图，词法级边）。与 get_impact 同模式：
// 两个可选扩展接口各自独立断言注册，core 未实现各自接口时跳过注册，
// tools/list 不出现死工具。

package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// GraphTracer 可选扩展（v5.9.0）：get_trace 的数据面——符号图调用链查询。
// *service.Service 实现（未启用返回 error（环境面故障）；不可达/未解析返回
// 提示文本（非 error））。
type GraphTracer interface {
	// Trace 符号图上查 from→to 调用链路（可达性与途经路径，围栏文本）。
	Trace(ctx context.Context, platform, owner, repo, number, from, to string) (string, error)
}

// SymbolContexter 可选扩展（v5.9.0）：get_symbol_context 的数据面——符号
// 图上下文（直接调用方/被调邻接）。*service.Service 实现（错误语义同上）。
type SymbolContexter interface {
	// SymContext 查某符号的邻接上下文（围栏文本）。
	SymContext(ctx context.Context, platform, owner, repo, number, symbol string) (string, error)
}

// NewGraphTools 注册 get_trace/get_symbol_context（New 内调用；core 未实现
// 各自接口时跳过对应注册，tools/list 不出现死工具——impact.go 同纪律）。
func NewGraphTools(s *server.MCPServer, core Core) {
	if gt, ok := core.(GraphTracer); ok {
		s.AddTool(traceTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "from", "to")
			if rerr != nil {
				return rerr, nil
			}
			out, err := gt.Trace(ctx, vals[0], vals[1], vals[2], vals[3], vals[4], vals[5])
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
	if sc, ok := core.(SymbolContexter); ok {
		s.AddTool(symbolContextTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "symbol")
			if rerr != nil {
				return rerr, nil
			}
			out, err := sc.SymContext(ctx, vals[0], vals[1], vals[2], vals[3], vals[4])
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
}

func traceTool() mcp.Tool {
	return mcp.NewTool("get_trace",
		mcp.WithDescription("在 PR 工作副本符号图上查询两符号间的调用链路（get_trace）：from→to 是否可达及途经函数——核查「改动会不会波及下游/上游从哪里进来」、修复前侦察影响面用；search_sem 命中附的邻接行同源（v5.9.0 图增强）。边默认词法级（同名按裸名合并）；部署启用 ARGUS_GRAPH_PRECISE 时为 go/types 类型解析级（接口分派仍不可见）——结论是线索，引用前须读代码核实。服务未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("from", mcp.Required(), mcp.Description("起点：函数/方法名或 Recv.Name 全名，同名按裸名合并解析")),
		mcp.WithString("to", mcp.Required(), mcp.Description("终点：函数/方法名或 Recv.Name 全名，同名按裸名合并解析")),
	)
}

func symbolContextTool() mcp.Tool {
	return mcp.NewTool("get_symbol_context",
		mcp.WithDescription("查询 PR 工作副本内某符号的图上下文（get_symbol_context）：直接调用方与被调邻接列表——评审意见定位「谁会受这次改动影响/这个函数依赖什么」用；search_sem 命中附的邻接行同源（v5.9.0 图增强）。边默认词法级（同名按裸名合并）；部署启用 ARGUS_GRAPH_PRECISE 时为 go/types 类型解析级（接口分派仍不可见）——结论是线索，引用前须读代码核实。服务未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("函数/方法名或 Recv.Name 全名，同名按裸名合并解析")),
	)
}
