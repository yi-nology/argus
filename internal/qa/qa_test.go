package qa

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"git.example.com/enjoydream/agentkit/conversation"

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
	out := conversation.Render(toConversationTurns(hist))
	// 单条回答截断到 400 rune + 省略号：不应把 500 字全文带回上下文
	if strings.Count(out, "长") > 401 {
		t.Fatalf("历史应截断: %d", strings.Count(out, "长"))
	}
	if !strings.Contains(out, "q1") {
		t.Fatalf("问题应保留: %q", out)
	}
}

// ---- v3.13.0 下沉后 argus 侧 glue：QATurn↔conversation.Turn 转换 + rollSummary 退化 ----

func TestToConversationTurns(t *testing.T) {
	got := toConversationTurns(makeTurns(2))
	if len(got) != 2 || got[0].Question != "q0" || got[1].Answer != "a" || got[1].At != "t" {
		t.Fatalf("转换字段错位: %+v", got)
	}
	if got := toConversationTurns(nil); len(got) != 0 {
		t.Fatalf("空历史应空转换: %+v", got)
	}
}

// rollSummary 模型不可用 → 保旧摘要（纯 FIFO 退化），不 panic 不阻塞。
func TestRollSummary_DegradesToFIFO(t *testing.T) {
	e := &Engine{} // Model/QAModel 均 nil：chatModel()=nil → Generate 必失败
	old := "旧摘要要点"
	got := e.rollSummary(context.Background(), "test", old,
		[]conversation.Turn{{Question: "q", Answer: "a"}})
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
