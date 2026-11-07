// repomap.go —— MCP get_repo_map（v5.6.2，图谱可视化 D 项）：仓库级目录依赖
// 图只读工具。与 get_impact 同模式：core 未实现 RepoMapper 时跳过注册，
// tools/list 不出现死工具。数据面=浅克隆+TTL 缓存+BuildRepoMap（service）。

package mcpserver

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RepoMapper 可选扩展：仓库代码地图数据面。*service.Service 实现。
type RepoMapper interface {
	RepoMapView(ctx context.Context, platform, owner, repo string) (string, bool)
}

// NewRepoMapTool 注册 get_repo_map（New 内调用）。
func NewRepoMapTool(s *server.MCPServer, core Core) {
	rm, ok := core.(RepoMapper)
	if !ok {
		return
	}
	s.AddTool(repoMapTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo")
		if rerr != nil {
			return rerr, nil
		}
		data, ok := rm.RepoMapView(ctx, vals[0], vals[1], vals[2])
		if !ok {
			return mcp.NewToolResultText("仓库代码地图不可用：" + data + "（生成中请稍后重试；平台凭据缺失/字符非法也会落到这里）。"), nil
		}
		return mcp.NewToolResultText(data), nil
	})
}

func repoMapTool() mcp.Tool {
	return mcp.NewTool("get_repo_map",
		mcp.WithDescription("查询某仓库的整仓目录依赖地图（v5.6.2）：Go 目录=包 import 聚合、shell source 装载聚合、SQL 表血缘跨目录聚合，返回 nodes/edges/notes 结构化 JSON——架构梳理/新人上手/评审前掌握仓库地形用。按需浅克隆（首查约数十秒，缓存 30 分钟），只读。交互 HTML 见 GET /repos/{owner}/{repo}/map?platform=…（🔒 Bearer）"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间（可含子群路径）")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
	)
}
