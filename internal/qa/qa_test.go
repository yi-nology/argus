package qa

import (
	"context"
	"fmt"
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

// ---- v3.12.0 G2 滚动摘要 ----

func TestEvictedTurns(t *testing.T) {
	if got := evictedTurns(makeTurns(6)); got != nil {
		t.Fatalf("6 轮内无淘汰: %+v", got)
	}
	got := evictedTurns(makeTurns(9))
	if len(got) != 3 || got[0].Question != "q0" {
		t.Fatalf("9 轮应淘汰前 3 轮: %+v", got)
	}
}

func TestCombineHistoryText(t *testing.T) {
	out := combineHistoryText("此前确认了登录已实现", makeTurns(2))
	if !strings.Contains(out, "更早对话摘要") || !strings.Contains(out, "此前确认了登录已实现") {
		t.Fatalf("摘要应在注入文本头部: %q", out)
	}
	if !strings.Contains(out, "用户: q0") {
		t.Fatalf("verbatim 轮次应保留: %q", out)
	}
	if out := combineHistoryText("", nil); out != "" {
		t.Fatalf("空会话应空注入: %q", out)
	}
}

// rollSummary 模型不可用 → 保旧摘要（纯 FIFO 退化），不 panic 不阻塞。
func TestRollSummary_DegradesToFIFO(t *testing.T) {
	e := &Engine{} // Model/QAModel 均 nil：chatModel()=nil → Generate 必失败
	old := "旧摘要要点"
	got := e.rollSummary(context.Background(), "test", old, makeTurns(3))
	if got != old {
		t.Fatalf("失败应保旧摘要: %q", got)
	}
}

func makeTurns(n int) []storage.QATurn {
	turns := make([]storage.QATurn, 0, n)
	for i := 0; i < n; i++ {
		turns = append(turns, storage.QATurn{Question: fmt.Sprintf("q%d", i), Answer: "a", At: "t"})
	}
	return turns
}
