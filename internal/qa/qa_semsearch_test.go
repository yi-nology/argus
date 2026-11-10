package qa

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"argus/internal/semsearch"
)

// TestSearchSem_Guards 未启用/空参守卫（不触网络；nil 底座与关开关同型）。
func TestSearchSem_Guards(t *testing.T) {
	e := &Engine{}
	_, err := e.SearchSem(context.Background(), "gitea", "acme", "app", "1", "认证", 5)
	if err == nil || !strings.Contains(err.Error(), "search_sem 未启用") {
		t.Errorf("nil 底座应报未启用，got %v", err)
	}
	e2 := &Engine{SemSearchOn: true} // 开关开着但底座 nil（装配缺失）→ 同报未启用
	if _, err := e2.SearchSem(context.Background(), "gitea", "acme", "app", "1", "x", 5); err == nil {
		t.Error("底座 nil 应报错")
	}
}

// TestRenderSemNeighbors 邻接行渲染（v5.9.0）：调用方/被调分组、单侧封顶、
// 空定义位置只显符号名、空组省略与全空返空。
func TestRenderSemNeighbors(t *testing.T) {
	mixed := []semsearch.SymRef{
		{Symbol: "Beta", Path: "b.go", Line: 34, Rel: semRelCallee},
		{Symbol: "Alpha", Path: "a.go", Line: 12, Rel: semRelCaller},
		{Symbol: "Gamma", Rel: semRelCallee}, // 无定义位置：只显符号名
	}
	if got, want := renderSemNeighbors(mixed, 3),
		"调用方 Alpha(a.go:12)；被调 Beta(b.go:34)、Gamma"; got != want {
		t.Errorf("分组渲染 = %q, want %q", got, want)
	}
	var many []semsearch.SymRef // 每侧 4 条，cap=2 应各取前 2（合计 ≤2×cap）
	for i := 0; i < 4; i++ {
		many = append(many,
			semsearch.SymRef{Symbol: fmt.Sprintf("c%d", i), Rel: semRelCaller},
			semsearch.SymRef{Symbol: fmt.Sprintf("d%d", i), Rel: semRelCallee})
	}
	if got, want := renderSemNeighbors(many, 2), "调用方 c0、c1；被调 d0、d1"; got != want {
		t.Errorf("封顶渲染 = %q, want %q", got, want)
	}
	if got, want := renderSemNeighbors(mixed[1:2], 3), "调用方 Alpha(a.go:12)"; got != want {
		t.Errorf("空被调组省略 = %q, want %q", got, want)
	}
	if got, want := renderSemNeighbors(mixed[:1], 3), "被调 Beta(b.go:34)"; got != want {
		t.Errorf("空调用方组省略 = %q, want %q", got, want)
	}
	if got := renderSemNeighbors(nil, 3); got != "" {
		t.Errorf("空邻接应返回空串，got %q", got)
	}
}

// TestTrace_SymContext_Guards get_trace/get_symbol_context 未启用守卫（不触
// 网络；nil 底座与关开关同型，TestSearchSem_Guards 同款两态）。service 层是
// repoAllowedFor 门 + s.qa() 委派薄层，不另做单测。
func TestTrace_SymContext_Guards(t *testing.T) {
	e := &Engine{}
	if _, err := e.Trace(context.Background(), "gitea", "acme", "app", "1", "A", "B"); err == nil || !strings.Contains(err.Error(), "get_trace 未启用") {
		t.Errorf("nil 底座 Trace 应报未启用，got %v", err)
	}
	if _, err := e.SymContext(context.Background(), "gitea", "acme", "app", "1", "A"); err == nil || !strings.Contains(err.Error(), "get_symbol_context 未启用") {
		t.Errorf("nil 底座 SymContext 应报未启用，got %v", err)
	}
	e2 := &Engine{SemSearchOn: true} // 开关开着但底座 nil（装配缺失）→ 同报未启用
	if _, err := e2.Trace(context.Background(), "gitea", "acme", "app", "1", "A", "B"); err == nil || !strings.Contains(err.Error(), "get_trace 未启用") {
		t.Errorf("底座 nil Trace 应报未启用，got %v", err)
	}
	if _, err := e2.SymContext(context.Background(), "gitea", "acme", "app", "1", "A"); err == nil || !strings.Contains(err.Error(), "get_symbol_context 未启用") {
		t.Errorf("底座 nil SymContext 应报未启用，got %v", err)
	}
}

// TestEdgeQualityNote 图面边界措辞随精确边模式切换（v5.10.0）：precise=go/types
// 类型解析级（接口分派不可见），否则维持词法级；get_trace/get_symbol_context
// 围栏与提示文本共用。
func TestEdgeQualityNote(t *testing.T) {
	if got, want := edgeQualityNote(true), "类型解析级边（go/types；接口分派不可见）"; got != want {
		t.Errorf("precise=true 措辞不符 = %q, want %q", got, want)
	}
	if got, want := edgeQualityNote(false), "词法级边"; got != want {
		t.Errorf("precise=false 措辞不符 = %q, want %q", got, want)
	}
}
