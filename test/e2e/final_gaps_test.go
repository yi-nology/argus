// final_gaps_test.go —— 深挖收尾批补课的三个 e2e 场景：
//
//	场景 24 编队共识（黑板互看收敛；回归 Select 漏排除 consensus 成员的双跑 bug）
//	场景 25 remote(mcp) 插件经 dispatcher 全链路（mcp-go 进程内 stub server）
//	场景 26/27 评论路由 LLM 分支（review 命中 / 低置信回落 ask）
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"argus/internal/plugin"
	"argus/internal/storage"
)

// waitCond 轮询等待（异步断言用）。
func waitCond(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// ---- 场景 24：编队共识（consensus squad）----

// correctness + security 组成 consensus 编队：R4 不应再盲审派发他们（Select 排除），
// 黑板两轮：首轮 correctness 报问题、security 空手；次轮无人补充 → 收敛。
// correctness 人设恰被调用 2 次（共识两轮）——若 Select 漏排除则是 3 次（盲审多一次）。
func TestScenario24_SquadConsensusConverges(t *testing.T) {
	const comment = "登录逻辑缺少登录失败次数限制"
	s := newStack(t, stackOpts{
		workers: 0,
		correctness: fmt.Sprintf(
			`{"findings":[{"file":"a.go","line":7,"severity":"medium","comment":%q}],"notes":""}`, comment),
		squads: []plugin.SquadDecl{{
			Name: "core-consensus", Mode: plugin.SquadModeConsensus,
			Paths: []string{"**"}, Members: []string{"correctness", "security"},
		}},
	})
	tid := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, tid, "done")
	if !strings.Contains(task.Report, comment) {
		t.Fatalf("共识成员发现应进报告:\n%s", task.Report)
	}
	if n := strings.Count(task.Report, comment); n != 1 {
		t.Fatalf("同一发现应只出现一次（去重后），got %d", n)
	}
	if calls := s.llm.r4calls; calls != 2 {
		t.Fatalf("consensus 成员应恰执行 2 轮（实际 %d；3=Select 漏排除的双跑回归）", calls)
	}
}

// ---- 场景 25：remote(mcp) 插件全链路 ----

// newRemoteMCPStub 进程内 MCP streamable-http server：review-service mcp-server
// 编排契约 stub——create_review(wait=false) 回 review_id → get_review 直返
// success → get_review_findings 返回固定 findings（path/body 裁剪形态）。
func newRemoteMCPStub(t TestingT, findingsJSON string) *httptest.Server {
	t.Helper()
	s := mcpserver.NewMCPServer("remote-reviewer-stub", "1.0.0",
		mcpserver.WithToolCapabilities(false))
	s.AddTool(mcp.NewTool("create_review", mcp.WithDescription("创建审查")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"review_id":"rev-e2e","status":"queued"}`), nil
		})
	s.AddTool(mcp.NewTool("get_review", mcp.WithDescription("查状态")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"review_id":"rev-e2e","status":"success","summary":"e2e"}`), nil
		})
	s.AddTool(mcp.NewTool("get_review_findings", mcp.WithDescription("取 findings")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(findingsJSON), nil
		})
	return httptest.NewServer(mcpserver.NewStreamableHTTPServer(s))
}

func TestScenario25_RemoteMCPPluginReview(t *testing.T) {
	const comment = "remote 专家发现的事务遗漏"
	srv := newRemoteMCPStub(t, fmt.Sprintf(
		`{"review_id":"rev-e2e","findings":[{"path":"a.go","line":3,"severity":"medium","title":"","body":%q}],"total":1,"truncated":false}`, comment))
	t.Cleanup(srv.Close)

	s := newStack(t, stackOpts{
		workers: 0,
		extraAgents: []plugin.AgentDecl{{
			Name: "remote-reviewer", Kind: "remote", Protocol: "mcp",
			Skills:   []string{plugin.SkillCodeReview},
			Endpoint: srv.URL + "/mcp", Tool: "create_review",
			Cost: plugin.CostStandard, Timeout: "30s",
		}},
	})
	tid := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, tid, "done")
	if !strings.Contains(task.Report, comment) {
		t.Fatalf("remote 插件发现应进报告:\n%s", task.Report)
	}
	if !strings.Contains(task.Report, "remote-reviewer") {
		t.Fatalf("参与插件应含 remote-reviewer:\n%s", task.Report)
	}
}

// ---- 场景 26/27：评论路由 LLM 分支 ----

// 分类命中 review（无关键词快路径可走的措辞）→ 建复审任务 + 回执。
func TestScenario26_RouterLLMReviewBranch(t *testing.T) {
	const mark = "帮忙从入口到存储把整体设计完整核一遍"
	s := newStack(t, stackOpts{
		workers:       0,
		commentRouter: true,
		routerReply:   `{"route":"review","confidence":0.9,"reason":"要求复审"}`,
		routerMark:    mark,
	})
	ctx := context.Background()

	s.setGitLabNoteEvent("@argus-bot "+mark, "alice", false)
	code, resp := s.postWebhookOn(t, "gitlab", "gl-d1")
	if code != 200 || resp.Status != "accepted" {
		t.Fatalf("评论应被接受: %d %+v", code, resp)
	}
	waitCond(t, "应建复审任务", func() bool {
		tasks, err := s.store.ListTasks(ctx, storage.StatusPending, 50)
		return err == nil && len(tasks) == 1
	})
	_, _, _, bodies := s.fake.snapshot()
	waitCond(t, "应回执复审评论", func() bool {
		_, _, _, bodies = s.fake.snapshot()
		return len(bodies) == 1 && strings.Contains(bodies[0], "已收到重新审查请求")
	})
}

// 分类返回低置信 review → 按纪律回落 ask（Q&A 回答），不烧重审预算。
func TestScenario27_RouterLowConfidenceFallsBackToAsk(t *testing.T) {
	const mark = "帮忙从入口到存储把整体设计完整核一遍"
	s := newStack(t, stackOpts{
		workers:       0,
		commentRouter: true,
		routerReply:   `{"route":"review","confidence":0.2,"reason":"不太确定"}`,
		routerMark:    mark,
	})

	s.setGitLabNoteEvent("@argus-bot "+mark, "alice", false)
	code, resp := s.postWebhookOn(t, "gitlab", "gl-d1")
	if code != 200 || resp.Status != "accepted" {
		t.Fatalf("评论应被接受: %d %+v", code, resp)
	}
	waitCond(t, "低置信应回落 Q&A 回帖", func() bool {
		_, _, _, bodies := s.fake.snapshot()
		return len(bodies) == 1
	})
	_, _, _, bodies := s.fake.snapshot()
	if strings.Contains(bodies[0], "已收到重新审查请求") {
		t.Fatalf("低置信 review 不应触发重审: %s", bodies[0])
	}
	if !strings.HasPrefix(bodies[0], "> ") {
		t.Fatalf("应走 Q&A 引用回帖形态: %q", bodies[0][:min(60, len(bodies[0]))])
	}
	tasks, err := s.store.ListTasks(context.Background(), storage.StatusPending, 50)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("回落 ask 不应建任务: n=%d err=%v", len(tasks), err)
	}
}

var _ = http.StatusOK // keep import if unused in future edits
