package impactview

import (
	"strings"
	"testing"
)

// TestRenderHTMLFuncDiffNodes v5.7.4:函数级变更一等节点融入主图(视图层合成,
// 快照与 mermaid 不受影响),状态三色与归属边随 JSON/布局/绘制同源一致。
func TestRenderHTMLFuncDiffNodes(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a.go"},
		Nodes: []Node{{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true}},
		FuncDiffs: []FuncDiff{
			{File: "a.go", Name: "F", Status: FuncModified, SigSame: true,
				Before: &FuncSnap{Signature: "func F()", Lines: 3},
				After:  &FuncSnap{Signature: "func F(x int)", Lines: 9}},
			{File: "other/ghost.go", Name: "G", Status: FuncAdded}, // 所在文件不在改动集→不合成
		},
	}
	html := RenderHTML(s, "t1")
	if !strings.Contains(html, `kind-funcdiff`) {
		t.Fatal("主图应含 funcdiff 节点")
	}
	if !strings.Contains(html, `rel-func_of`) {
		t.Fatal("应含归属边")
	}
	if !strings.Contains(html, `fd:a.go:F`) {
		t.Fatal("fd 节点应入 JSON(联动数据源)")
	}
	if strings.Contains(html, `fd:other/ghost.go:G`) {
		t.Fatal("文件不在改动集的 FuncDiff 不应合成")
	}
	// 快照不被污染(mermaid/报告面用原 snapshot)
	if len(s.Nodes) != 1 || len(s.Edges) != 0 {
		t.Fatalf("入参 snapshot 不应被修改: nodes=%d edges=%d", len(s.Nodes), len(s.Edges))
	}
	if m := RenderMermaid(s); m != "" && strings.Contains(m, "fd:a.go:F") {
		t.Fatal("mermaid 不应含合成 fd 节点")
	}
}
