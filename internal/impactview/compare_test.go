package impactview

import (
	"strings"
	"testing"
)

func mkSnap(nodes []Node, edges []Edge, fds []FuncDiff, score int) *Snapshot {
	return &Snapshot{Version: SnapshotVersion, Nodes: nodes, Edges: edges,
		FuncDiffs: fds, Changed: []string{"a.go"},
		Risk: Risk{Level: RiskMedium, Score: score}}
}

func TestCompareSnapshotsDiff(t *testing.T) {
	prev := mkSnap([]Node{
		{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true},
		{ID: "file:old.go", Kind: KindFile, Label: "old.go"},
	}, []Edge{{From: "file:a.go", To: "file:old.go", Rel: RelSource}}, []FuncDiff{
		{File: "a.go", Name: "Old", Status: FuncModified},
	}, 30)
	cur := mkSnap([]Node{
		{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true},
		{ID: "file:new.go", Kind: KindFile, Label: "new.go"},
	}, []Edge{{From: "file:a.go", To: "file:new.go", Rel: RelSource}}, []FuncDiff{
		{File: "a.go", Name: "New", Status: FuncAdded},
	}, 42)
	ic := CompareSnapshots(prev, cur)
	if ic == nil {
		t.Fatal("对比不应为 nil")
	}
	if len(ic.AddedNodes) != 1 || ic.AddedNodes[0].ID != "file:new.go" {
		t.Fatalf("应检出新增节点: %+v", ic.AddedNodes)
	}
	if len(ic.RemovedNodes) != 1 || ic.RemovedNodes[0].ID != "file:old.go" {
		t.Fatalf("应检出移除节点: %+v", ic.RemovedNodes)
	}
	if len(ic.NewFuncDiffs) != 1 || ic.NewFuncDiffs[0] != "a.go:New" {
		t.Fatalf("应检出新增函数级变更: %+v", ic.NewFuncDiffs)
	}
	if len(ic.ResolvedFuncDiffs) != 1 || ic.ResolvedFuncDiffs[0] != "a.go:Old" {
		t.Fatalf("应检出已恢复函数级变更: %+v", ic.ResolvedFuncDiffs)
	}
	if ic.RiskBefore.Score != 30 || ic.RiskAfter.Score != 42 {
		t.Fatalf("风险前后值不对: %+v", ic)
	}
}

func TestCompareSnapshotsNilPrev(t *testing.T) {
	if CompareSnapshots(nil, mkSnap(nil, nil, nil, 10)) != nil {
		t.Fatal("prev=nil 应返回 nil(无对比面)")
	}
}

func TestRenderHTMLCompareSummary(t *testing.T) {
	prev := mkSnap([]Node{{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true}}, nil, nil, 20)
	cur := mkSnap([]Node{
		{ID: "file:a.go", Kind: KindFile, Label: "a.go", Changed: true},
		{ID: "pkg:b", Kind: KindPackage, Label: "b"},
	}, []Edge{{From: "file:a.go", To: "pkg:b", Rel: RelImport}}, []FuncDiff{{File: "a.go", Name: "N", Status: FuncAdded}}, 45)
	cmp := CompareSnapshots(prev, cur)
	html := RenderHTMLFull(cur, "t-cur", ViewOpts{Compare: cmp})
	if !strings.Contains(html, "对比上一轮") || !strings.Contains(html, "20→45") {
		t.Fatal("应含演化摘要与风险前后值")
	}
	if !strings.Contains(html, "新增波及 1") {
		t.Fatal("应含新增波及明细")
	}
	// nil cmp:无摘要块
	if html2 := RenderHTMLFull(cur, "t2", ViewOpts{}); strings.Contains(html2, "对比上一轮") {
		t.Fatal("无对比面不应渲染摘要")
	}
}
