package toolpool

import (
	"context"
	"testing"

	"github.com/oekit/logx"

	"argus/internal/plugin"
)

func TestSpecsFromDecls(t *testing.T) {
	specs := SpecsFromDecls([]plugin.ToolSpecDecl{
		{Server: "docs", Allow: []string{"search_docs", "get_page"}},
		{Server: "lint"},
	})
	if len(specs) != 2 {
		t.Fatalf("应 2 条 spec，得到 %d", len(specs))
	}
	if specs[0].MCP != "docs" || len(specs[0].Allow) != 2 {
		t.Fatalf("spec[0] 不符: %+v", specs[0])
	}
	if specs[1].MCP != "lint" || specs[1].Allow != nil {
		t.Fatalf("spec[1] 不符: %+v", specs[1])
	}
}

func TestNewRealPoolEmpty(t *testing.T) {
	if p := NewRealPool(nil, logx.NewSlogLogger("test")); p != nil {
		t.Fatal("空声明应返回 nil")
	}
}

func TestNewRealPoolInvalidDeclSkipped(t *testing.T) {
	p := NewRealPool([]plugin.MCPServerDecl{
		{Name: "bad"},                        // command/url 均空 → 跳过
		{Name: "ok", URL: "http://mcp.test"}, // 有效
	}, logx.NewSlogLogger("test"))
	if p == nil {
		t.Fatal("含有效声明应返回非 nil")
	}
	servers := p.Servers()
	if len(servers) != 1 || servers[0] != "ok" {
		t.Fatalf("无效声明应被跳过，得到 %v", servers)
	}
	p.Close()
}

func TestCallMetaRoundtrip(t *testing.T) {
	ctx := context.Background()
	if got := CallMeta(ctx); got != nil {
		t.Fatal("无注入的 ctx 应返回 nil（metaClient 对空不注入 _meta）")
	}
	meta := map[string]any{"task_id": "t1", "repo": "o/r"}
	ctx = WithCallMeta(ctx, meta)
	got := CallMeta(ctx)
	if got == nil || got["task_id"] != "t1" || got["repo"] != "o/r" {
		t.Fatalf("CallMeta 取回值不符: %v", got)
	}
}

func TestNewRealPoolInvalidProbeTimeout(t *testing.T) {
	// probeTimeout 解析失败不致命——落回 agentkit 缺省 5s
	p := NewRealPool([]plugin.MCPServerDecl{
		{Name: "ok", URL: "http://mcp.test", ProbeTimeout: "not-a-duration"},
	}, logx.NewSlogLogger("test"))
	if p == nil {
		t.Fatal("应返回非 nil")
	}
	p.Close()
}
