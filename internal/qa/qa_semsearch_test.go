package qa

import (
	"context"
	"strings"
	"testing"
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
