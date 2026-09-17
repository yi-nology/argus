package adapter_remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"argus/internal/plugin"
)

// spawnMCPServer 进程内起一个 MCP server（streamable http），工具 review 回显 findings JSON。
func spawnMCPServer(t *testing.T, fail bool) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("review", mcp.WithDescription("审查工具")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			payload, _ := req.GetArguments()["payload"].(string)
			if !strings.Contains(payload, `"owner"`) {
				return mcp.NewToolResultError("payload 缺少 owner"), nil
			}
			if fail {
				return mcp.NewToolResultError("远端内部错误"), nil
			}
			return mcp.NewToolResultText(`{"findings":[{"file":"a.go","line":3,"severity":"medium","comment":"x"}],"notes":"ok"}`), nil
		})
	srv := server.NewStreamableHTTPServer(s)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func remoteTask() plugin.Task {
	return plugin.Task{
		PR: plugin.PRContext{
			Platform: "gitea", Owner: "demo-user", Repo: "demo", Number: "7",
			Title: "t", Body: "b", HeadSHA: "abc",
		},
	}
}

func newRemote(t *testing.T, protocol plugin.Protocol, endpoint, tool string) *Remote {
	return newRemoteAuth(t, protocol, endpoint, tool, "")
}

func newRemoteAuth(t *testing.T, protocol plugin.Protocol, endpoint, tool, authToken string) *Remote {
	t.Helper()
	m := plugin.AgentManifest{Name: "review-service", Skills: []string{plugin.SkillCodeReview}, Timeout: 5 * time.Second}
	r, err := New(m, protocol, endpoint, tool, authToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestRemoteMCPInvoke 真实协议往返：payload 进、findings 出。
func TestRemoteMCPInvoke(t *testing.T) {
	ts := spawnMCPServer(t, false)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "review")
	res, err := r.Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].File != "a.go" || res.Findings[0].Severity != "medium" {
		t.Fatalf("findings 不符: %+v", res.Findings)
	}
	if res.Notes != "ok" {
		t.Fatalf("notes 不符: %q", res.Notes)
	}
}

// TestRemoteMCPToolError 远端工具报错 → 返回错误（Merger 部分失败容忍路径）。
func TestRemoteMCPToolError(t *testing.T) {
	ts := spawnMCPServer(t, true)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("远端工具报错应上抛")
	}
}

// TestRemoteMCPEndpointDown endpoint 不可达 → 报错不 panic。
func TestRemoteMCPEndpointDown(t *testing.T) {
	r := newRemote(t, plugin.ProtocolMCP, "http://127.0.0.1:1/mcp", "review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("不可达应上抛")
	}
}

// spawnAuthMCPServer 带 Bearer 共享密钥门的 MCP server（401 语义与 ocrshim 一致）。
func spawnAuthMCPServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("review", mcp.WithDescription("审查工具")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"findings":[],"notes":"ok"}`), nil
		})
	srv := server.NewStreamableHTTPServer(s)
	var handler http.Handler = srv
	if token != "" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			srv.ServeHTTP(w, r)
		})
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestRemoteMCPAuthHeader 配置 AuthToken → 请求携带 Bearer 头通过对端门；
// 错误 token 被对端 401 → Invoke 上抛（Merger 部分失败容忍路径）。
func TestRemoteMCPAuthHeader(t *testing.T) {
	ts := spawnAuthMCPServer(t, "s3cret")
	res, err := newRemoteAuth(t, plugin.ProtocolMCP, ts.URL, "review", "s3cret").
		Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatalf("正确 token 应通过: %v", err)
	}
	if res.Notes != "ok" {
		t.Fatalf("notes 不符: %q", res.Notes)
	}
	if _, err := newRemoteAuth(t, plugin.ProtocolMCP, ts.URL, "review", "wrong").
		Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("错误 token 应被对端 401 拒绝并上抛")
	}
}
