// mcp.go remote 插件的 MCP client（design §3.5 方向 B 消费侧）。
// streamable http 传输；每任务建短连接（Initialize→CallTool→Close），
// 不做连接池——插件调用频率低，复用池化反而引入跨任务状态（§8 反过度设计）。
package adapter_remote

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"argus/internal/plugin"
)

func (r *Remote) invokeMCP(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	_, payload, err := buildPayload(task)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("构造 payload 失败: %w", err)
	}

	c, err := client.NewStreamableHttpClient(r.Endpoint)
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

	req := mcp.CallToolRequest{}
	req.Params.Name = r.Tool
	req.Params.Arguments = map[string]any{"payload": payload}
	res, err := c.CallTool(ctx, req)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("MCP 工具 %s 调用失败: %w", r.Tool, err)
	}

	var texts []string
	for _, content := range res.Content {
		if tc, ok := mcp.AsTextContent(content); ok {
			texts = append(texts, tc.Text)
		}
	}
	joined := textFromContents(texts)
	if res.IsError {
		return plugin.Result{}, fmt.Errorf("MCP 工具 %s 返回错误: %.200s", r.Tool, joined)
	}
	return parseFindings(joined)
}
