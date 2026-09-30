package adapter_remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"argus/internal/plugin"
)

func TestMain(m *testing.M) {
	mcpPollInterval = 20 * time.Millisecond // 测试收窄轮询间隔，语义不变
	os.Exit(m.Run())
}

// capturedCreateArgs 记录 create_review 收到的平铺参数（分流断言用）。
var capturedCreateArgs map[string]any

// spawnMCPServer 进程内起一个 MCP server（streamable http），按 review-service
// mcp-server 编排契约 mock：create_review(wait=false) 校验平铺参数回 queued
// review_id → get_review 前两轮 running 第三轮 success → get_review_findings
// 回裁剪 findings。fail=true 时 create_review 返回工具错误（Merger 部分失败
// 容忍路径）。
func spawnMCPServer(t *testing.T, fail bool) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("create_review", mcp.WithDescription("创建审查")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			capturedCreateArgs = args
			provider, _ := args["provider"].(string)
			repo, _ := args["repo"].(string)
			sha, _ := args["head_sha"].(string)
			if provider == "" || repo == "" || sha == "" {
				return mcp.NewToolResultError("平铺参数缺失（provider/repo/head_sha）"), nil
			}
			if args["wait"] != false {
				return mcp.NewToolResultError("argus 应走异步模式 wait=false"), nil
			}
			if fail {
				return mcp.NewToolResultError("远端内部错误"), nil
			}
			return mcp.NewToolResultText(`{"review_id":"rev-1","status":"queued"}`), nil
		})
	polls := 0
	s.AddTool(mcp.NewTool("get_review", mcp.WithDescription("查状态")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.GetArguments()["review_id"] != "rev-1" {
				return mcp.NewToolResultError("review 不存在"), nil
			}
			polls++
			st := "running"
			if polls >= 3 {
				st = "success"
			}
			return mcp.NewToolResultText(`{"review_id":"rev-1","status":"` + st + `","summary":"3 处问题"}`), nil
		})
	s.AddTool(mcp.NewTool("get_review_findings", mcp.WithDescription("取 findings")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.GetArguments()["review_id"] != "rev-1" {
				return mcp.NewToolResultError("review 不存在"), nil
			}
			return mcp.NewToolResultText(`{"review_id":"rev-1","findings":[` +
				`{"path":"a.go","line":3,"severity":"medium","title":"未用导入","body":"errors 导入无引用","suggestion":"删掉"}` +
				`],"total":1,"truncated":false}`), nil
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
			Title: "t", Body: "b", Author: "alice", HeadSHA: "abc",
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

// TestRemoteMCPInvoke 真实协议往返：create_review 平铺参数同步创建 →
// get_review_findings 取明细 → 映射回与 builtin/cli 同 schema 的 findings。
func TestRemoteMCPInvoke(t *testing.T) {
	ts := spawnMCPServer(t, false)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "create_review")
	res, err := r.Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings 条数不符: %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.File != "a.go" || f.Line != 3 || f.Severity != "medium" {
		t.Fatalf("finding 映射不符: %+v", f)
	}
	if f.Comment != "未用导入\n\nerrors 导入无引用" {
		t.Fatalf("comment 应为 title+body 拼接: %q", f.Comment)
	}
	if f.SuggestedFix != "删掉" || f.By != "remote" {
		t.Fatalf("suggested_fix/by 不符: %+v", f)
	}
	if res.Notes != "3 处问题" {
		t.Fatalf("notes 应取 create_review 的 summary: %q", res.Notes)
	}
}

// TestRemoteMCPTwoWireContracts needs:[working-copy] 分流两条 wire 契约：
// 声明 working-copy → 单 payload 参数（ocrshim sidecar）；不声明 → create_review
// 编排（review-service mcp-server）。
func TestRemoteMCPTwoWireContracts(t *testing.T) {
	var gotArgs map[string]any
	s := server.NewMCPServer("dual", "1.0")
	s.AddTool(mcp.NewTool("create_review"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gotArgs = req.GetArguments()
		if _, ok := gotArgs["payload"]; ok {
			return mcp.NewToolResultError("编排契约不应收到 payload"), nil
		}
		return mcp.NewToolResultText(`{"review_id":"rev-1","status":"queued"}`), nil
	})
	s.AddTool(mcp.NewTool("get_review"), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"review_id":"rev-1","status":"success","summary":"ok"}`), nil
	})
	s.AddTool(mcp.NewTool("get_review_findings"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.GetArguments()["review_id"] != "rev-1" {
			return mcp.NewToolResultError("review 不存在"), nil
		}
		return mcp.NewToolResultText(`{"review_id":"rev-1","findings":[],"total":0,"truncated":false}`), nil
	})
	s.AddTool(mcp.NewTool("review"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gotArgs = req.GetArguments()
		if _, ok := gotArgs["payload"]; !ok {
			return mcp.NewToolResultError("payload 契约应收到 payload"), nil
		}
		return mcp.NewToolResultText(`{"findings":[],"notes":"ok"}`), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)

	// 非 working-copy → 编排契约（provider 平铺参数）。
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "create_review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err != nil {
		t.Fatal(err)
	}
	if gotArgs["provider"] != "gitea" {
		t.Fatalf("编排契约应平铺 provider: %+v", gotArgs)
	}

	// working-copy → payload 契约（对端是 ocrshim，tool: review + payload.workcopy_dir）。
	gotArgs = nil
	m := plugin.AgentManifest{Name: "ocr", Skills: []string{plugin.SkillCodeReview}, Timeout: 5 * time.Second}
	m.Needs.WorkingCopy = true
	rwc, err := New(m, plugin.ProtocolMCP, ts.URL, "review", "", &fakePool{dir: "/tmp/wc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rwc.Invoke(context.Background(), remoteTask()); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotArgs["payload"]; !ok {
		t.Fatalf("working-copy 契约应发 payload: %+v", gotArgs)
	}
}

// TestRemoteMCPWaitFailed get_review 终态 failed → 上抛（带 review_id），
// 走 Merger 部分失败容忍 + failed-retry。
func TestRemoteMCPWaitFailed(t *testing.T) {
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("create_review"), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"review_id":"rev-x","status":"queued"}`), nil
	})
	s.AddTool(mcp.NewTool("get_review"), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"review_id":"rev-x","status":"failed","summary":"拉取 MR 失败"}`), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)

	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "create_review")
	_, err := r.Invoke(context.Background(), remoteTask())
	if err == nil {
		t.Fatal("failed 终态应上抛")
	}
	if !strings.Contains(err.Error(), "rev-x") {
		t.Fatalf("错误应带 review_id: %v", err)
	}
}

// TestRemoteMCPCreateParams PR/MR 编号按平台分流：gitea 走 pull_request_id，
// gitlab 走 merge_request_iid；provider 归一小写。
func TestRemoteMCPCreateParams(t *testing.T) {
	ts := spawnMCPServer(t, false)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "create_review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err != nil {
		t.Fatal(err)
	}
	if capturedCreateArgs["pull_request_id"] != "7" {
		t.Fatalf("gitea 应传 pull_request_id: %+v", capturedCreateArgs)
	}
	if capturedCreateArgs["author"] != "alice" || capturedCreateArgs["wait"] != false {
		t.Fatalf("author/wait 参数不符: %+v", capturedCreateArgs)
	}

	capturedCreateArgs = nil
	gitlabTask := remoteTask()
	gitlabTask.PR.Platform = "GitLab" // 服务端归一前的形态也应可调用
	if _, err := r.Invoke(context.Background(), gitlabTask); err != nil {
		t.Fatal(err)
	}
	if capturedCreateArgs["provider"] != "gitlab" {
		t.Fatalf("provider 应归一小写: %+v", capturedCreateArgs)
	}
	if capturedCreateArgs["merge_request_iid"] != "7" {
		t.Fatalf("gitlab 应传 merge_request_iid: %+v", capturedCreateArgs)
	}
}

// TestRemoteMCPToolError 远端工具报错 → 返回错误（Merger 部分失败容忍路径）。
func TestRemoteMCPToolError(t *testing.T) {
	ts := spawnMCPServer(t, true)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "create_review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("远端工具报错应上抛")
	}
}

// TestRemoteMCPEndpointDown endpoint 不可达 → 报错不 panic。
func TestRemoteMCPEndpointDown(t *testing.T) {
	r := newRemote(t, plugin.ProtocolMCP, "http://127.0.0.1:1/mcp", "create_review")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("不可达应上抛")
	}
}

// spawnAuthMCPServer 带 Bearer 共享密钥门的 MCP server（401 语义与 mcp-server 一致）。
func spawnAuthMCPServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("create_review", mcp.WithDescription("创建审查")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"review_id":"rev-1","status":"queued"}`), nil
		})
	s.AddTool(mcp.NewTool("get_review", mcp.WithDescription("查状态")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"review_id":"rev-1","status":"success","summary":"ok"}`), nil
		})
	s.AddTool(mcp.NewTool("get_review_findings", mcp.WithDescription("取 findings")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"review_id":"rev-1","findings":[],"total":0,"truncated":false}`), nil
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
	res, err := newRemoteAuth(t, plugin.ProtocolMCP, ts.URL, "create_review", "s3cret").
		Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatalf("正确 token 应通过: %v", err)
	}
	if res.Notes != "ok" {
		t.Fatalf("notes 不符: %q", res.Notes)
	}
	if _, err := newRemoteAuth(t, plugin.ProtocolMCP, ts.URL, "create_review", "wrong").
		Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("错误 token 应被对端 401 拒绝并上抛")
	}
}
