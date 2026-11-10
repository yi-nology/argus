package adapter_remote

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/plugin"
)

// fakePool 记录 Ensure/Release 调用（*workcopy.Pool 结构性满足 WorkcopyPool）。
type fakePool struct {
	dir      string
	key      workcopy.WorktreeKey
	ensured  int
	released int
}

func (f *fakePool) Ensure(_ context.Context, k workcopy.WorktreeKey) (string, error) {
	f.key = k
	f.ensured++
	return f.dir, nil
}

func (f *fakePool) Release(workcopy.WorktreeKey) { f.released++ }

// needs:[working-copy] 的 remote 插件应准备工作副本、把目录写进 payload.workcopy_dir
// （sidecar 形态：OCR shim 在该目录执行），调用结束后释放。
func TestRemoteMCPWorkingCopyPayload(t *testing.T) {
	var gotPayload string
	s := server.NewMCPServer("test-reviewer", "1.0")
	s.AddTool(mcp.NewTool("review"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		gotPayload, _ = req.GetArguments()["payload"].(string)
		return mcp.NewToolResultText(`{"findings":[],"notes":"ok"}`), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(ts.Close)

	pool := &fakePool{dir: "/tmp/wc-dir"}
	m := plugin.AgentManifest{Name: "ocr", Skills: []string{plugin.SkillCodeReview}, Timeout: 5 * time.Second}
	m.Needs.WorkingCopy = true
	r, err := New(m, plugin.ProtocolMCP, ts.URL, "review", "", pool)
	if err != nil {
		t.Fatal(err)
	}
	task := plugin.Task{PR: plugin.PRContext{
		Platform: "gitea", Owner: "demo-user", Repo: "demo", Number: "7",
		HeadSHA: "abc", DefaultBranch: "main",
	}}
	if _, err := r.Invoke(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	if pool.ensured != 1 || pool.released != 1 {
		t.Fatalf("Ensure/Release 应各一次，got %d/%d", pool.ensured, pool.released)
	}
	if pool.key.HeadSHA != "abc" || pool.key.DefaultBranch != "main" {
		t.Fatalf("Ensure 的 key 不符: %+v", pool.key)
	}
	var p struct {
		WorkcopyDir string `json:"workcopy_dir"`
		HeadSHA     string `json:"head_sha"`
	}
	if err := json.Unmarshal([]byte(gotPayload), &p); err != nil {
		t.Fatalf("payload 非法 JSON: %v", err)
	}
	if p.WorkcopyDir != "/tmp/wc-dir" {
		t.Fatalf("payload.workcopy_dir 不符: %q", p.WorkcopyDir)
	}
	if p.HeadSHA != "abc" {
		t.Fatalf("payload.head_sha 不符: %q", p.HeadSHA)
	}
}

// 声明 working-copy 但未接池 → 构造期报错（bootstrap 启动期即停用并注明）。
func TestRemoteWorkingCopyNeedsPool(t *testing.T) {
	m := plugin.AgentManifest{Name: "ocr"}
	m.Needs.WorkingCopy = true
	if _, err := New(m, plugin.ProtocolMCP, "http://127.0.0.1:1/mcp", "review", "", nil); err == nil {
		t.Fatal("声明 working-copy 而无池应构造报错")
	}
}
