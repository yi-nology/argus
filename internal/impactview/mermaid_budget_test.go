package impactview

import "testing"

// TestTrimCallerEdgesBudget 评论图 caller 预算（v5.6.3）：总数封顶+每符号封顶，
// 非 caller 边一律不动，纯函数不污染输入切片。
func TestTrimCallerEdgesBudget(t *testing.T) {
	edges := []Edge{
		{From: "sym:a", To: "file:1", Rel: RelCaller},
		{From: "sym:a", To: "file:2", Rel: RelCaller},
		{From: "sym:a", To: "file:3", Rel: RelCaller},
		{From: "sym:a", To: "file:4", Rel: RelCaller}, // 第 4 条：每符号封顶剪
		{From: "file:x", To: "file:y", Rel: RelSource},
		{From: "file:x", To: "table:t", Rel: RelLineage},
	}
	for i := 0; i < 30; i++ {
		edges = append(edges, Edge{From: "sym:b" + string(rune('a'+i%26)), To: "file:o", Rel: RelCaller})
	}
	inLen := len(edges)
	out, trimmed := trimCallerEdgesForComment(edges)
	if len(out)+trimmed != inLen {
		t.Fatalf("修剪应守恒: out=%d trimmed=%d in=%d", len(out), trimmed, inLen)
	}
	nonCaller := 0
	caller := map[string]int{}
	for _, e := range out {
		if e.Rel != RelCaller {
			nonCaller++
			continue
		}
		caller[e.From]++
		if caller[e.From] > maxCallerPerSym {
			t.Fatalf("单符号 caller 边超 %d: %s", maxCallerPerSym, e.From)
		}
	}
	if nonCaller != 2 {
		t.Fatalf("非 caller 边必须全保留: %d", nonCaller)
	}
	if total := len(out) - nonCaller; total > maxCallerEdgesComment {
		t.Fatalf("caller 总预算超 %d: %d", maxCallerEdgesComment, total)
	}
	if len(edges) != inLen {
		t.Fatal("输入切片被污染（纯函数纪律）")
	}
}
