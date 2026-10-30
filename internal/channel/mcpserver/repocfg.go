// repocfg.go —— MCP get_repo_config（v3.187.0）：服务侧按仓配置只读工具。
// 返回某仓当前生效的预算/上限参数（repo_settings 覆盖 + 全局默认合并视图），
// 外部 AI/运维核查「这个仓按什么参数在跑」用。写入走 HTTP admin API
// （PUT /repo-settings/...），MCP 面刻意只读——配置变更是信任面操作。
package mcpserver

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RepoConfigViewer 可选扩展（v3.187.0）：get_repo_config 的数据面。
// *service.Service 实现（RepoSettingsView 合并视图）。
type RepoConfigViewer interface {
	// RepoSettingsView 单仓配置视图（settings+effective；身份非法时带 error 键）。
	RepoSettingsView(ctx context.Context, platform, owner, repo string) (map[string]any, int)
}

// NewRepoConfigTool 注册 get_repo_config（New 内调用；core 未实现
// RepoConfigViewer 时跳过注册，tools/list 不出现死工具）。
func NewRepoConfigTool(s *server.MCPServer, core Core) {
	rc, ok := core.(RepoConfigViewer)
	if !ok {
		return
	}
	s.AddTool(repoConfigTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo")
		if rerr != nil {
			return rerr, nil
		}
		data, code := rc.RepoSettingsView(ctx, vals[0], vals[1], vals[2])
		if code != http.StatusOK {
			return mcp.NewToolResultError(viewErrOf(data, "按仓配置查询失败")), nil
		}
		return marshalToolResult(data)
	})
}

func repoConfigTool() mcp.Tool {
	return mcp.NewTool("get_repo_config",
		mcp.WithDescription("查询某仓库的 Argus 按仓配置与生效参数：monthly_token_budget（月度预算闸）/task_token_budget（单任务预算）/max_diff_chars（diff 上限），返回按仓覆盖集 + 全局默认合并后的 effective 视图——核查某仓实际参数、诊断预算拦截（failed 且提示「预算已耗尽」）前先查这里。只读；写入须走 HTTP admin API（PUT/DELETE /repo-settings/{platform}/{owner}/{repo}，admin 档 token）"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
	)
}

// viewErrOf service 错误 map 的文案提取（data["error"]，缺省落 def）。
func viewErrOf(data map[string]any, def string) string {
	if msg, ok := data["error"].(string); ok && msg != "" {
		return msg
	}
	return def
}
