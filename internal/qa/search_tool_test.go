package qa

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// v3.91.0：search_code 工具构造——绑定真实目录可检索；v3.112.0 起直接测
// 生产构造器 newSearchCodeTool（v3.111.0 的 searchCodeToolFor 平行壳已清偿，
// 不留只有测试在调的生产函数——v3.97.0 先例）。
func TestSearchCodeTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package app\n\nfunc limitUnbounded() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Log: testNopLogger{}}

	st := e.newSearchCodeTool(func(context.Context) string { return dir })
	if st == nil {
		t.Fatal("目录就绪应返回工具")
	}
	info, err := st.Info(context.Background())
	if err != nil || info.Name != "search_code" {
		t.Fatalf("工具名不符: %v %v", info, err)
	}

	// 真实执行：模式命中临时目录内容，产出数据区围栏文本。
	inv, ok := st.(tool.InvokableTool)
	if !ok {
		t.Fatal("search_code 应实现 InvokableTool")
	}
	res, err := inv.InvokableRun(context.Background(), `{"pattern":"limitUnbounded"}`)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res, "limitUnbounded") || !strings.Contains(res, "search_code 检索结果") {
		t.Errorf("检索结果应含命中与围栏标记:\n%s", res)
	}

	// 目录不可用（fetch 空串）：返回工具降级文案而非 error（ReAct 面自然收窄）。
	stDown := e.newSearchCodeTool(func(context.Context) string { return "" })
	resDown, err := stDown.(tool.InvokableTool).InvokableRun(context.Background(), `{"pattern":"x"}`)
	if err != nil || !strings.Contains(resDown, "工作副本不可用") {
		t.Errorf("目录不可用应返回降级文案: err=%v res=%s", err, resDown)
	}
}

// SearchCode MCP 数据面（v3.121.0）：门控路径——未启用（WC nil / CodeSearch
// false）必须返回 error 而非空结果（MCP 侧据此转工具错误）。启用路径依赖真
// SDK/工作副本池，目录解析与检索语义已由 TestSearchCodeTool 与 workcopyDir
// 覆盖，此处不重复打桩。
func TestSearchCodeDisabled(t *testing.T) {
	e := &Engine{Log: testNopLogger{}}
	if _, err := e.SearchCode(context.Background(), "gitea", "acme", "app", "7", "x", "", false); err == nil {
		t.Fatal("WC 未配置应返回 error")
	}
	e2 := &Engine{Log: testNopLogger{}, CodeSearch: true}
	if _, err := e2.SearchCode(context.Background(), "gitea", "acme", "app", "7", "x", "", false); err == nil {
		t.Fatal("CodeSearch 开但 WC nil 仍应 error")
	}
}
