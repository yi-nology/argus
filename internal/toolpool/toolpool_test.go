package toolpool

import (
	"strings"
	"testing"

	"git.example.com/enjoydream/ekit/observability/logx"

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

func TestExpandEnvRef(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", "tok-123")
	log := logx.NewSlogLogger("test")

	got := expandEnvRef("Bearer ${TEST_MCP_TOKEN}", log)
	if got != "Bearer tok-123" {
		t.Fatalf("展开失败: %q", got)
	}

	// 未定义变量保留原样
	got = expandEnvRef("Bearer ${DEFINITELY_UNDEFINED_VAR_XYZ}", log)
	if !strings.Contains(got, "DEFINITELY_UNDEFINED_VAR_XYZ") {
		t.Fatalf("未定义变量应保留原样: %q", got)
	}

	// 无引用直通
	if got := expandEnvRef("plain", log); got != "plain" {
		t.Fatalf("无引用应直通: %q", got)
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
