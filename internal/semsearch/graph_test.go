// graph_test.go —— 图面（v5.9.0）行为测试：建图与 stdlib 剪枝、路径与跳数钳制、
// 单符号上下文与同名多候选合并、命中附邻接、Manager 数据面（Trace/SymbolContext）。
// 夹具取真实可编译形态（module + 同包文件 + 被引用子包），embedding 复用
// semsearch_test.go 的 stubEmbedder。
package semsearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFileSet 批量落盘测试夹具（目录自动创建）。
func writeFileSet(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// graphFixture 建图测试夹具（真实可编译形态：module + 同包 + 被引用子包）：
// Alpha→Beta→Gamma→Delta 调用链（a.go/b.go），Beta 经 util.Gamma 跨包调用
// （末段归一命中 b.go 的 Gamma），Delta 调 fmt.Println（stdlib 剪枝面），z.go
// 孤立符号（不可达面）。go.mod 不在 codeExts，仅作模块声明。
func graphFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module fixture\n\ngo 1.22\n",
		"a.go": `package app

import "fixture/util"

func Alpha() {
	Beta()
}

func Beta() {
	util.Gamma()
}
`,
		"b.go": `package app

import "fmt"

func Gamma() {
	Delta()
}

func Delta() {
	fmt.Println("done")
}
`,
		"util/util.go": "package util\n\nfunc Gamma() {}\n",
		"z.go":         "package app\n\nfunc Lonely() {}\n",
	})
	return dir
}

// assertChain 校验调用链的符号序与边向序（长度不符直接 Fatal）。
func assertChain(t *testing.T, got []SymRef, wantSymbols, wantRels []string) {
	t.Helper()
	if len(got) != len(wantSymbols) {
		t.Fatalf("链长 = %d, want %d: %+v", len(got), len(wantSymbols), got)
	}
	for i, s := range wantSymbols {
		if got[i].Symbol != s {
			t.Errorf("chain[%d].Symbol = %q, want %q", i, got[i].Symbol, s)
		}
		if got[i].Rel != wantRels[i] {
			t.Errorf("chain[%d].Rel = %q, want %q", i, got[i].Rel, wantRels[i])
		}
	}
}

// refSymbols 提取符号序列（邻接断言辅助）。
func refSymbols(refs []SymRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

// TestGraph_Build 建图：调用链按末段归一解析、跨包调用命中仓库符号、stdlib
// 剪枝不出边、命中邻接可读。
func TestGraph_Build(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	chain, ok := ix.Path("Alpha", "Delta", graphHopsDefault)
	if !ok {
		t.Fatal("Alpha→Delta 应可达（3 跳）")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	// 定义位置随链输出；Gamma 落在 b.go——util.Gamma 末段归一命中的正是它
	//（同名候选按路径序取首个）。
	wantLoc := []struct {
		path string
		line int
	}{{"a.go", 5}, {"a.go", 9}, {"b.go", 5}, {"b.go", 9}}
	for i, w := range wantLoc {
		if chain[i].Path != w.path || chain[i].Line != w.line {
			t.Errorf("chain[%d] loc = %s:%d, want %s:%d", i, chain[i].Path, chain[i].Line, w.path, w.line)
		}
	}
	ix.mu.Lock()
	nDeltaCallees := len(ix.sym.adj["Delta"])
	deltaNeighbors := ix.sym.neighborsOf("Delta")
	betaOut := append([]string(nil), ix.sym.adj["Beta"]...)
	alphaNeighbors := ix.sym.neighborsOf("Alpha")
	ix.mu.Unlock()
	// 剪枝：Delta 调 fmt.Println 不入图——无仓库内被调，邻接只剩调用方 Gamma，
	// 不含 fmt.Println/Println。
	if nDeltaCallees != 0 {
		t.Errorf("Delta 被调应剪枝为空, got %d 项", nDeltaCallees)
	}
	for _, r := range deltaNeighbors {
		if r.Symbol == "fmt.Println" || r.Symbol == "Println" {
			t.Errorf("stdlib 剪枝失败: Delta 邻接含 %q", r.Symbol)
		}
	}
	if len(deltaNeighbors) != 1 || deltaNeighbors[0].Symbol != "Gamma" || deltaNeighbors[0].Rel != relCaller {
		t.Errorf("Delta 邻接 = %+v, want [{Gamma caller}]", deltaNeighbors)
	}
	// 跨包调用按末段归一：Beta 的被调是 b.go 的 Gamma。
	if len(betaOut) != 1 || betaOut[0] != "Gamma" {
		t.Errorf("Beta 被调 = %v, want [Gamma]", betaOut)
	}
	if len(alphaNeighbors) != 1 || alphaNeighbors[0].Symbol != "Beta" || alphaNeighbors[0].Rel != relCallee {
		t.Errorf("Alpha 邻接 = %+v, want [{Beta callee}]", alphaNeighbors)
	}
}

// TestGraph_Path 路径查询：跳数不足不可达、超界钳制、无向 BFS 逆向边向
// called_by、同符号单步、孤立/未知符号 false。
func TestGraph_Path(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Path("Alpha", "Delta", 2); ok {
		t.Error("maxHops=2 不应到达（链需 3 跳）")
	}
	if _, ok := ix.Path("Alpha", "Delta", 1); ok {
		t.Error("maxHops=1 不应到达")
	}
	chain, ok := ix.Path("Alpha", "Delta", 99)
	if !ok {
		t.Fatal("maxHops 超界应钳制到 graphHopsMax 并找到")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	rev, ok := ix.Path("Delta", "Alpha", graphHopsMax)
	if !ok {
		t.Fatal("逆向应可达（无向 BFS）")
	}
	assertChain(t, rev,
		[]string{"Delta", "Gamma", "Beta", "Alpha"},
		[]string{"", relCalledBy, relCalledBy, relCalledBy})
	self, ok := ix.Path("Beta", "Beta", graphHopsDefault)
	if !ok || len(self) != 1 || self[0].Symbol != "Beta" || self[0].Path != "a.go" || self[0].Rel != "" {
		t.Errorf("同符号应单步返回 self: %+v ok=%v", self, ok)
	}
	if _, ok := ix.Path("Alpha", "Lonely", graphHopsMax); ok {
		t.Error("孤立符号不可达")
	}
	if _, ok := ix.Path("Alpha", "NoSuchSymbol", graphHopsMax); ok {
		t.Error("未知符号应 false")
	}
}

// TestGraph_SymbolContext 单符号上下文：同名多候选（异接收者方法）self 取路径
// 序首个、邻接合并；精确全名单候选；未知/stdlib 裸名 ok=false。
func TestGraph_SymbolContext(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"x.go": `package x

func (s *Store) Save() {
	s.touch()
}

func (s *Store) touch() {}
`,
		"y.go": `package x

func (c *Client) Save() {
	c.flush()
}

func (c *Client) flush() {}
`,
		"callers.go": `package x

func CallerA() {
	st := &Store{}
	st.Save()
}

func CallerB() {
	cl := &Client{}
	cl.Save()
}
`,
	})
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 裸名 Save 两候选（Client.Save / Store.Save），词法级合并邻接。
	self, in, out, ok := ix.SymbolContext("Save")
	if !ok {
		t.Fatal("Save 应命中")
	}
	if self.Symbol != "Client.Save" || self.Path != "y.go" || self.Line != 3 {
		t.Errorf("self = %+v, want Client.Save y.go:3（路径序首个候选）", self)
	}
	if got := refSymbols(in); len(got) != 2 || got[0] != "CallerA" || got[1] != "CallerB" {
		t.Errorf("Save in = %v, want [CallerA CallerB]（多候选合并）", got)
	}
	for _, r := range in {
		if r.Rel != relCaller {
			t.Errorf("Save in rel = %q, want %q", r.Rel, relCaller)
		}
	}
	if got := refSymbols(out); len(got) != 2 || got[0] != "Client.flush" || got[1] != "Store.touch" {
		t.Errorf("Save out = %v, want [Client.flush Store.touch]", got)
	}
	for _, r := range out {
		if r.Rel != relCallee {
			t.Errorf("Save out rel = %q, want %q", r.Rel, relCallee)
		}
	}
	// 精确全名：单候选。
	self2, in2, out2, ok2 := ix.SymbolContext("Store.touch")
	if !ok2 || self2.Symbol != "Store.touch" || self2.Path != "x.go" || self2.Line != 7 {
		t.Errorf("Store.touch self = %+v ok=%v", self2, ok2)
	}
	if len(in2) != 1 || in2[0].Symbol != "Store.Save" || in2[0].Rel != relCaller {
		t.Errorf("Store.touch in = %+v", in2)
	}
	if len(out2) != 0 {
		t.Errorf("Store.touch out = %+v, want 空", out2)
	}
	// stdlib 裸名与未知符号都不在符号表。
	if _, _, _, ok := ix.SymbolContext("Println"); ok {
		t.Error("stdlib 裸名 Println 不应在符号表")
	}
	if _, _, _, ok := ix.SymbolContext("NoSuch"); ok {
		t.Error("未知符号应 ok=false")
	}
}

// TestSearchNeighbors 检索命中自动附邻接（图面接线）：Alpha 只带被调，Beta
// 调用方+被调合并按 Symbol 排序。
func TestSearchNeighbors(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"app.go": `package app

// Alpha 查询 DB。
func Alpha() {
	Beta()
}

func Beta() {
	ParseAll()
}

func ParseAll() {}
`,
	})
	m := NewManager(&stubEmbedder{})
	hits, err := m.Search(context.Background(), dir, "db", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("hit 数 = %d, want 3: %+v", len(hits), hits)
	}
	var alpha, beta *Hit
	for i := range hits {
		switch hits[i].Symbol {
		case "Alpha":
			alpha = &hits[i]
		case "Beta":
			beta = &hits[i]
		}
	}
	if alpha == nil || beta == nil {
		t.Fatalf("应命中 Alpha 与 Beta: %+v", hits)
	}
	if len(alpha.Neighbors) != 1 || alpha.Neighbors[0].Symbol != "Beta" || alpha.Neighbors[0].Rel != relCallee {
		t.Errorf("Alpha 邻接 = %+v, want [{Beta callee}]", alpha.Neighbors)
	}
	if len(beta.Neighbors) != 2 ||
		beta.Neighbors[0].Symbol != "Alpha" || beta.Neighbors[0].Rel != relCaller ||
		beta.Neighbors[1].Symbol != "ParseAll" || beta.Neighbors[1].Rel != relCallee {
		t.Errorf("Beta 邻接 = %+v, want [{Alpha caller} {ParseAll callee}]", beta.Neighbors)
	}
}

// TestManagerTraceAndSymbolContext Manager 数据面：Trace 最短链、二次 Trace 零
// embedding（增量零成本）、不可达 false、SymbolContext 同款目录缓存模式。
func TestManagerTraceAndSymbolContext(t *testing.T) {
	dir := graphFixture(t)
	e := &stubEmbedder{}
	m := NewManager(e)
	ctx := context.Background()
	chain, ok, err := m.Trace(ctx, dir, "Alpha", "Gamma")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Alpha→Gamma 应可达")
	}
	assertChain(t, chain, []string{"Alpha", "Beta", "Gamma"}, []string{"", relCalls, relCalls})
	callsAfterFirst := e.calls
	if _, _, err := m.Trace(ctx, dir, "Alpha", "Gamma"); err != nil {
		t.Fatal(err)
	}
	if e.calls != callsAfterFirst {
		t.Errorf("二次 Trace 不应触发 Embed: %d → %d", callsAfterFirst, e.calls)
	}
	if _, ok, _ := m.Trace(ctx, dir, "Alpha", "Lonely"); ok {
		t.Error("孤立符号不可达")
	}
	self, in, out, ok, err := m.SymbolContext(ctx, dir, "Beta")
	if err != nil || !ok {
		t.Fatalf("SymbolContext 失败: ok=%v err=%v", ok, err)
	}
	if self.Symbol != "Beta" || self.Path != "a.go" || self.Line != 9 {
		t.Errorf("self = %+v, want Beta a.go:9", self)
	}
	if len(in) != 1 || in[0].Symbol != "Alpha" || in[0].Rel != relCaller {
		t.Errorf("in = %+v", in)
	}
	if len(out) != 1 || out[0].Symbol != "Gamma" || out[0].Rel != relCallee {
		t.Errorf("out = %+v", out)
	}
}
