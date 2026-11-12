package impactview

import (
	"strings"
	"testing"
)

// TestViewGraphSymbolCollapse 混合语言 PR 可读性（v6.9.1）：列 0 改动节点超
// viewGraphSymbolsCap 时 symbol 折叠进父文件节点，边重定向 file→caller 去重。
// 实测场景：27 changed symbol + 7 changed file = 34 节点列 0 在 1080p 不可读。
func TestViewGraphSymbolCollapse(t *testing.T) {
	// 构造 15 changed symbol（3 文件 × 5 symbol）+ 3 changed file = 18 > 12 cap
	s := &Snapshot{Version: SnapshotVersion,
		Changed: []string{"a.rs", "b.rs", "c.rs"},
		Nodes: []Node{
			{ID: "file:a.rs", Kind: KindFile, Label: "a.rs", Changed: true},
			{ID: "file:b.rs", Kind: KindFile, Label: "b.rs", Changed: true},
			{ID: "file:c.rs", Kind: KindFile, Label: "c.rs", Changed: true},
			{ID: "sym:a.rs:fn1", Kind: KindSymbol, Label: "fn1()", Changed: true},
			{ID: "sym:a.rs:fn2", Kind: KindSymbol, Label: "fn2()", Changed: true},
			{ID: "sym:a.rs:fn3", Kind: KindSymbol, Label: "fn3()", Changed: true},
			{ID: "sym:a.rs:fn4", Kind: KindSymbol, Label: "fn4()", Changed: true},
			{ID: "sym:a.rs:fn5", Kind: KindSymbol, Label: "fn5()", Changed: true},
			{ID: "sym:b.rs:fn1", Kind: KindSymbol, Label: "fn1()", Changed: true},
			{ID: "sym:b.rs:fn2", Kind: KindSymbol, Label: "fn2()", Changed: true},
			{ID: "sym:b.rs:fn3", Kind: KindSymbol, Label: "fn3()", Changed: true},
			{ID: "sym:b.rs:fn4", Kind: KindSymbol, Label: "fn4()", Changed: true},
			{ID: "sym:b.rs:fn5", Kind: KindSymbol, Label: "fn5()", Changed: true},
			{ID: "sym:c.rs:fn1", Kind: KindSymbol, Label: "fn1()", Changed: true},
			{ID: "sym:c.rs:fn2", Kind: KindSymbol, Label: "fn2()", Changed: true},
			{ID: "sym:c.rs:fn3", Kind: KindSymbol, Label: "fn3()", Changed: true},
			{ID: "sym:c.rs:fn4", Kind: KindSymbol, Label: "fn4()", Changed: true},
			{ID: "sym:c.rs:fn5", Kind: KindSymbol, Label: "fn5()", Changed: true},
			{ID: "file:caller1.rs", Kind: KindFile, Label: "caller1.rs"},
			{ID: "file:caller2.rs", Kind: KindFile, Label: "caller2.rs"},
		},
		Edges: []Edge{
			// a.rs 5 symbols → caller1（重定向后应合并为 1 条 file→caller1）
			{From: "sym:a.rs:fn1", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:a.rs:fn2", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:a.rs:fn3", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:a.rs:fn4", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:a.rs:fn5", To: "file:caller1.rs", Rel: RelCaller},
			// b.rs 5 symbols → caller1 + caller2
			{From: "sym:b.rs:fn1", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:b.rs:fn2", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:b.rs:fn3", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:b.rs:fn4", To: "file:caller1.rs", Rel: RelCaller},
			{From: "sym:b.rs:fn5", To: "file:caller2.rs", Rel: RelCaller},
			// c.rs 5 symbols → caller2
			{From: "sym:c.rs:fn1", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:c.rs:fn2", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:c.rs:fn3", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:c.rs:fn4", To: "file:caller2.rs", Rel: RelCaller},
			{From: "sym:c.rs:fn5", To: "file:caller2.rs", Rel: RelCaller},
		},
	}

	nodes, _, edges, _ := viewGraph(s)

	// 列 0 应从 18 节点（15 sym + 3 file）折叠到 3 file 节点
	changedCount := 0
	for _, n := range nodes {
		if n.Changed {
			changedCount++
			if n.Kind == KindSymbol {
				t.Fatalf("超载时 symbol 不应单独出现在视图: %s", n.ID)
			}
		}
	}
	if changedCount != 3 {
		t.Fatalf("折叠后列 0 应只剩 3 个 changed file 节点, got %d", changedCount)
	}

	// 文件节点 Detail 应含折叠计数
	for _, n := range nodes {
		if n.ID == "file:a.rs" && !strings.Contains(n.Detail, "5 函数被触达") {
			t.Fatalf("a.rs Detail 应含折叠计数, got %q", n.Detail)
		}
	}

	// 边应从 15 条去重到 4 条（a→c1, b→c1, b→c2, c→c2）
	if len(edges) != 4 {
		t.Fatalf("边去重后应有 4 条, got %d", len(edges))
	}
	// 所有边的 From 应是 file: 前缀（不再有 sym: 前缀）
	for _, e := range edges {
		if strings.HasPrefix(e.From, "sym:") {
			t.Fatalf("折叠后不应有 sym: 前缀的边 From: %s", e.From)
		}
	}

	// 快照不应被污染
	if len(s.Nodes) != 20 || len(s.Edges) != 15 {
		t.Fatalf("入参 snapshot 不应被修改: nodes=%d edges=%d", len(s.Nodes), len(s.Edges))
	}
}

// TestViewGraphNoCollapseUnderCap 列 0 改动节点 ≤ cap 时不折叠（小 PR 行为不回退）。
func TestViewGraphNoCollapseUnderCap(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion,
		Changed: []string{"a.go"},
		Nodes: []Node{
			{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true},
			{ID: "sym:a.go:Foo", Kind: KindSymbol, Label: "Foo()", Changed: true},
			{ID: "sym:a.go:Bar", Kind: KindSymbol, Label: "Bar()", Changed: true},
			{ID: "file:b.go", Kind: KindFile, Label: "b.go"},
		},
		Edges: []Edge{
			{From: "sym:a.go:Foo", To: "file:b.go", Rel: RelCaller},
			{From: "sym:a.go:Bar", To: "file:b.go", Rel: RelCaller},
		},
	}

	nodes, _, edges, _ := viewGraph(s)

	// 3 changed nodes (1 file + 2 sym) ≤ 12 cap → 不折叠
	symCount := 0
	for _, n := range nodes {
		if n.Kind == KindSymbol {
			symCount++
		}
	}
	if symCount != 2 {
		t.Fatalf("cap 内不应折叠 symbol, got %d symbols", symCount)
	}
	// 边保持原样（2 条 sym→caller）
	if len(edges) != 2 {
		t.Fatalf("cap 内边数不应变, got %d", len(edges))
	}
	for _, e := range edges {
		if !strings.HasPrefix(e.From, "sym:") {
			t.Fatalf("cap 内边 From 应保持 sym: 前缀, got %s", e.From)
		}
	}
}

// TestViewGraphCollapseIsolatedSymsSkipped 度=0 的孤立 symbol 不参与折叠判定
// （它们在原逻辑中就被剪掉，不该触发折叠也不需要映射）。
func TestViewGraphCollapseIsolatedSymsSkipped(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion,
		Changed: []string{"a.go"},
		Nodes: []Node{
			{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true},
			// 孤立 symbol（度=0，被剪）不应触发折叠
			{ID: "sym:a.go:Unused", Kind: KindSymbol, Label: "Unused()", Changed: true},
		},
		Edges: []Edge{},
	}

	nodes, _, _, _ := viewGraph(s)
	// 孤立 symbol 被剪，只剩 1 个 file 节点 → 不触发折叠
	if len(nodes) != 1 {
		t.Fatalf("孤立 symbol 应被剪掉, got %d nodes", len(nodes))
	}
	if nodes[0].Kind != KindFile {
		t.Fatalf("唯一节点应是 file, got %s", nodes[0].Kind)
	}
}

// TestRenderHTMLSymbolCollapse 端到端：RenderHTMLFull 输出的 SVG 中不应出现
// 被折叠的 symbol 节点，文件节点 Detail 应含 "N 函数被触达"。
func TestRenderHTMLSymbolCollapse(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion,
		Changed: []string{"a.rs", "b.rs"},
		Nodes: []Node{
			{ID: "file:a.rs", Kind: KindFile, Label: "a.rs", Changed: true},
			{ID: "file:b.rs", Kind: KindFile, Label: "b.rs", Changed: true},
			{ID: "sym:a.rs:f1", Kind: KindSymbol, Label: "f1()", Changed: true},
			{ID: "sym:a.rs:f2", Kind: KindSymbol, Label: "f2()", Changed: true},
			{ID: "sym:a.rs:f3", Kind: KindSymbol, Label: "f3()", Changed: true},
			{ID: "sym:a.rs:f4", Kind: KindSymbol, Label: "f4()", Changed: true},
			{ID: "sym:a.rs:f5", Kind: KindSymbol, Label: "f5()", Changed: true},
			{ID: "sym:a.rs:f6", Kind: KindSymbol, Label: "f6()", Changed: true},
			{ID: "sym:a.rs:f7", Kind: KindSymbol, Label: "f7()", Changed: true},
			{ID: "sym:b.rs:g1", Kind: KindSymbol, Label: "g1()", Changed: true},
			{ID: "sym:b.rs:g2", Kind: KindSymbol, Label: "g2()", Changed: true},
			{ID: "sym:b.rs:g3", Kind: KindSymbol, Label: "g3()", Changed: true},
			{ID: "sym:b.rs:g4", Kind: KindSymbol, Label: "g4()", Changed: true},
			{ID: "sym:b.rs:g5", Kind: KindSymbol, Label: "g5()", Changed: true},
			{ID: "file:c.rs", Kind: KindFile, Label: "c.rs"},
		},
		Edges: []Edge{
			{From: "sym:a.rs:f1", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f2", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f3", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f4", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f5", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f6", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:a.rs:f7", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:b.rs:g1", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:b.rs:g2", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:b.rs:g3", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:b.rs:g4", To: "file:c.rs", Rel: RelCaller},
			{From: "sym:b.rs:g5", To: "file:c.rs", Rel: RelCaller},
		},
	}

	html := RenderHTMLFull(s, "t1", ViewOpts{})

	// SVG 中不应出现被折叠的 symbol 节点（data-id 属性是 SVG 专有格式）
	if strings.Contains(html, `data-id="sym:a.rs:f1"`) {
		t.Fatal("SVG 不应出现被折叠的 symbol 节点")
	}
	// 但 JSON 数据面应保留全量（JS 搜索用）
	if !strings.Contains(html, `"sym:a.rs:f1"`) {
		t.Fatal("JSON 数据面应保留全量 symbol（JS 搜索不回退）")
	}
	// 文件节点 Detail 应含折叠计数
	if !strings.Contains(html, "7 函数被触达") {
		t.Fatal("a.rs 应显示 7 函数被触达")
	}
	if !strings.Contains(html, "5 函数被触达") {
		t.Fatal("b.rs 应显示 5 函数被触达")
	}
}
