package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// v3.1 方向 B 实装回归：Argus as MCP Server——真实 MCP 客户端（mark3labs/mcp-go）
// 走 streamable http 连接 /mcp，完成 initialize 握手并调用工具。
func TestScenario16_MCPServer(t *testing.T) {
	s := newStack(t, stackOpts{workers: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// MCP 握手（带 Bearer 鉴权）
	cli, err := mcpclient.NewStreamableHttpClient(s.mcpBase,
		mcptransport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer e2e-token"}))
	if err != nil {
		t.Fatal(err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "argus-e2e", Version: "0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		t.Fatalf("MCP initialize 失败: %v", err)
	}

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil {
			t.Fatalf("工具 %s 调用失败: %v", name, err)
		}
		if len(res.Content) == 0 {
			t.Fatalf("工具 %s 无内容", name)
		}
		tc, ok := res.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("工具 %s 首内容非文本: %+v", name, res.Content[0])
		}
		return tc.Text
	}

	// ① list_agents
	agents := call("list_agents", nil)
	for _, want := range []string{"requirement-compliance", "correctness", "security"} {
		if !strings.Contains(agents, want) {
			t.Fatalf("list_agents 缺插件 %s:\n%.200s", want, agents)
		}
	}

	// ② review_pr（wait=true）：无 findings + 全 implemented → approve（R5.5）
	review := call("review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"timeout_seconds": 120.0,
	})
	if !strings.Contains(review, `"verdict": "approve"`) && !strings.Contains(review, `"verdict":"approve"`) {
		t.Fatalf("review_pr 应返回 approve:\n%.600s", review)
	}

	// ③ 从结果取 task_id → get_report
	var out struct {
		TaskID  string `json:"task_id"`
		Verdict string `json:"verdict"`
		Report  string `json:"report"`
	}
	if err := json.Unmarshal([]byte(review), &out); err != nil {
		t.Fatalf("review_pr 结果解析失败: %v\n%.300s", err, review)
	}
	report := call("get_report", map[string]any{"task_id": out.TaskID})
	if !strings.Contains(report, "Argus 需求复核报告") {
		t.Fatalf("get_report 应含报告:\n%.400s", report)
	}
	if out.Verdict != "approve" {
		t.Fatalf("verdict=%s", out.Verdict)
	}
}
