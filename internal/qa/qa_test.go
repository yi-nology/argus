package qa

import (
	"strings"
	"testing"

	"argus/internal/storage"
)

// 自 service 迁入（v3.5.6 随 renderHistory 拆包）：单条回答截断到 400 rune + 省略号，
// 不应把长回答全文带回上下文。
func TestRenderHistoryCapsAndEscapes(t *testing.T) {
	var long strings.Builder
	for i := 0; i < 500; i++ {
		long.WriteRune('长')
	}
	hist := []storage.QATurn{
		{Question: "q1", Answer: long.String(), At: "2026-09-07T00:00:00Z"},
	}
	out := renderHistory(hist)
	// 单条回答截断到 400 rune + 省略号：不应把 500 字全文带回上下文
	if strings.Count(out, "长") > 401 {
		t.Fatalf("历史应截断: %d", strings.Count(out, "长"))
	}
	if !strings.Contains(out, "q1") {
		t.Fatalf("问题应保留: %q", out)
	}
}
