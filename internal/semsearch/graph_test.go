// graph_test.go —— 图面（v5.9.0 起）行为测试：建图与 stdlib 剪枝、路径与跳数
// 钳制、单符号上下文与同名多候选合并、命中附邻接、Manager 数据面
// （Trace/SymbolContext）；v5.10.0 增：NodeKey 键契约、跨包同名 import 限定
// 消歧与裸调用同目录优先、同接收者兄弟方法（跨文件）、qualified 目标缺失
// bare 回退、dot-import 回退、精确边替换词法（EdgeAugmenter 桩：成功/签名
// 门控/失败闩）与 SymbolContext 候选数。夹具取真实可编译形态（module +
// 多目录 + go.mod；go.mod 不在 codeExts 不进索引，仅作 module 声明供 import
// 剥前缀），embedding 复用 semsearch_test.go 的 stubEmbedder。
package semsearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
// Alpha→Beta→Gamma→Delta 调用链（a.go/b.go 根目录文件，键无 dir 前缀），Beta
// 另经 util.Gamma 跨包调用（import 剥模块前缀 qualified 消歧——与根包同名
// Gamma 是两个独立节点），Delta 调 fmt.Println（stdlib 剪枝面），z.go 孤立
// 符号（不可达面）。
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
	Gamma()
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

// TestNodeKey 节点键契约：dir 限定 / 根目录无前缀 / 接收者段。
func TestNodeKey(t *testing.T) {
	cases := []struct{ dir, recv, name, want string }{
		{"", "", "Alpha", "Alpha"},
		{"", "Store", "GetUser", "Store.GetUser"},
		{"internal/app", "", "New", "internal/app.New"},
		{"internal/app", "Store", "GetUser", "internal/app.Store.GetUser"},
	}
	for _, c := range cases {
		if got := NodeKey(c.dir, c.recv, c.name); got != c.want {
			t.Errorf("NodeKey(%q,%q,%q) = %q, want %q", c.dir, c.recv, c.name, got, c.want)
		}
	}
	if got := dirOf("a.go"); got != "" {
		t.Errorf("dirOf 根目录 = %q, want 空串", got)
	}
	if got := dirOf("internal/app/store.go"); got != "internal/app" {
		t.Errorf("dirOf = %q, want internal/app", got)
	}
}

// TestGraph_Build 建图：调用链同目录裸调用解析、跨包 import 限定消歧（同名
// Gamma 双节点互不污染）、stdlib 剪枝不出边、命中邻接可读。
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
	// 定义位置随链输出；链上的 Gamma 是 b.go 的根包符号（同目录裸调用）。
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
	// 消歧：Beta 的被调是根包 Gamma（同目录裸调用优先）+ util.Gamma（import
	// 限定 qualified）——两个同名独立节点，互不假边污染。
	if len(betaOut) != 2 || betaOut[0] != "Gamma" || betaOut[1] != "util.Gamma" {
		t.Errorf("Beta 被调 = %v, want [Gamma util.Gamma]（同目录优先 + import 限定）", betaOut)
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

// TestGraph_SameNameDisambiguation 跨包同名消歧（v5.10.0）：两包各一个 New，
// 包级裸调用同目录优先（另一包同名不混入），import 限定精确指向目标包节点。
func TestGraph_SameNameDisambiguation(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module disamb\n\ngo 1.22\n",
		"a/a.go": `package a

import "disamb/b"

func New() {}

func UseLocal() {
	New()
}

func UseCross() {
	b.New()
}
`,
		"b/b.go": "package b\n\nfunc New() {}\n",
	})
	if got := modulePathOf(dir); got != "disamb" {
		t.Errorf("modulePathOf = %q, want disamb", got)
	}
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	localOut := append([]string(nil), ix.sym.adj["a.UseLocal"]...)
	crossOut := append([]string(nil), ix.sym.adj["a.UseCross"]...)
	localIn := append([]string(nil), ix.sym.radj["a.New"]...)
	crossIn := append([]string(nil), ix.sym.radj["b.New"]...)
	ix.mu.Unlock()
	if len(localOut) != 1 || localOut[0] != "a.New" {
		t.Errorf("UseLocal 被调 = %v, want [a.New]（裸调用同目录优先，b.New 不混入）", localOut)
	}
	if len(crossOut) != 1 || crossOut[0] != "b.New" {
		t.Errorf("UseCross 被调 = %v, want [b.New]（import 限定消歧）", crossOut)
	}
	if len(localIn) != 1 || localIn[0] != "a.UseLocal" {
		t.Errorf("a.New 调用方 = %v, want [a.UseLocal]", localIn)
	}
	if len(crossIn) != 1 || crossIn[0] != "a.UseCross" {
		t.Errorf("b.New 调用方 = %v, want [a.UseCross]（同名节点无假边污染）", crossIn)
	}
}

// TestGraph_SiblingAndFallback 同接收者兄弟方法（跨文件同目录键）+ qualified
// 目标缺失 bare 回退 + dot-import 跨包裸调用回退（v5.10.0）。
func TestGraph_SiblingAndFallback(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod":           "module sib\n\ngo 1.22\n",
		"svc/run.go":       "package svc\n\nfunc (s *Runner) Run() {\n\ts.start()\n}\n",
		"svc/start.go":     "package svc\n\nfunc (r *Runner) start() {}\n",
		"helper/helper.go": "package helper\n\nfunc Util() {}\n",
		"dot.go": `package main

import . "sib/helper"

func UseDot() {
	Util()
}
`,
		"qual.go": `package main

import "sib/gone"

func Caller() {
	gone.Missing()
}

func Missing() {}
`,
	})
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 兄弟方法：start 定义在同包其他文件（start.go），同目录键精确命中。
	self, in, _, cands, ok := ix.SymbolContext("svc.Runner.start")
	if !ok || cands != 1 {
		t.Fatalf("svc.Runner.start 应单候选命中: ok=%v cands=%d", ok, cands)
	}
	if self.Symbol != "svc.Runner.start" || self.Path != "svc/start.go" || self.Line != 3 {
		t.Errorf("self = %+v, want svc.Runner.start svc/start.go:3", self)
	}
	if got := refSymbols(in); len(got) != 1 || got[0] != "svc.Runner.Run" {
		t.Errorf("in = %v, want [svc.Runner.Run]（同接收者兄弟方法）", got)
	}
	// qualified 目标缺失（import 了不存在的 sib/gone）→ bare 末段回退命中根包
	// 同名 Missing。
	ix.mu.Lock()
	callerOut := append([]string(nil), ix.sym.adj["Caller"]...)
	ix.mu.Unlock()
	if len(callerOut) != 1 || callerOut[0] != "Missing" {
		t.Errorf("Caller 被调 = %v, want [Missing]（qualified 缺失降 bare 回退）", callerOut)
	}
	// dot-import：跨包裸调用同目录无候选 → 全局 bare 回退保召回。
	ix.mu.Lock()
	dotOut := append([]string(nil), ix.sym.adj["UseDot"]...)
	ix.mu.Unlock()
	if len(dotOut) != 1 || dotOut[0] != "helper.Util" {
		t.Errorf("UseDot 被调 = %v, want [helper.Util]（dot-import 走全局回退）", dotOut)
	}
}

// TestGraph_SymbolContext 单符号上下文：同名多候选（异接收者方法）self 取路径
// 序首个、邻接合并、候选数；精确全名单候选；未知/stdlib 裸名 ok=false。
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
	// 裸名 Save 两候选（根目录键 Client.Save / Store.Save），词法级合并邻接
	// ——st/cl 是 var 限定（类型不可见），降裸名末段保召回。
	self, in, out, cands, ok := ix.SymbolContext("Save")
	if !ok {
		t.Fatal("Save 应命中")
	}
	if cands != 2 {
		t.Errorf("Save cands = %d, want 2", cands)
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
	// 精确全名：单候选（s.touch/c.flush 经兄弟方法规则 qualified 精确落边）。
	self2, in2, out2, cands2, ok2 := ix.SymbolContext("Store.touch")
	if !ok2 || cands2 != 1 || self2.Symbol != "Store.touch" || self2.Path != "x.go" || self2.Line != 7 {
		t.Errorf("Store.touch self = %+v cands=%d ok=%v", self2, cands2, ok2)
	}
	if len(in2) != 1 || in2[0].Symbol != "Store.Save" || in2[0].Rel != relCaller {
		t.Errorf("Store.touch in = %+v", in2)
	}
	if len(out2) != 0 {
		t.Errorf("Store.touch out = %+v, want 空", out2)
	}
	// stdlib 裸名与未知符号都不在符号表（cands=0）。
	if _, _, _, c, ok := ix.SymbolContext("Println"); ok || c != 0 {
		t.Error("stdlib 裸名 Println 不应在符号表")
	}
	if _, _, _, c, ok := ix.SymbolContext("NoSuch"); ok || c != 0 {
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
	self, in, out, cands, ok, err := m.SymbolContext(ctx, dir, "Beta")
	if err != nil || !ok {
		t.Fatalf("SymbolContext 失败: ok=%v err=%v", ok, err)
	}
	if cands != 1 {
		t.Errorf("Beta cands = %d, want 1（根目录单候选）", cands)
	}
	if self.Symbol != "Beta" || self.Path != "a.go" || self.Line != 9 {
		t.Errorf("self = %+v, want Beta a.go:9", self)
	}
	if len(in) != 1 || in[0].Symbol != "Alpha" || in[0].Rel != relCaller {
		t.Errorf("in = %+v", in)
	}
	// Beta 同包裸调 Gamma + import 限定 util.Gamma：两个被调。
	if len(out) != 2 || out[0].Symbol != "Gamma" || out[0].Rel != relCallee ||
		out[1].Symbol != "util.Gamma" || out[1].Rel != relCallee {
		t.Errorf("out = %+v, want [Gamma util.Gamma]", out)
	}
}

// stubAugmenter EdgeAugmenter 桩：可注入精确边或错误（失败闩面）。
type stubAugmenter struct {
	edges [][2]string
	err   error
	calls int
}

func (a *stubAugmenter) Edges(_ context.Context, _ string) ([][2]string, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return a.edges, nil
}

// TestPreciseEdgesReplaceLexical 精确边替换词法（v5.10.0）：成功应用后邻接
// 全量来自精确边（词法边消失）、符号表照建、签名门控（未变不重拉、变更才重拉）。
func TestPreciseEdgesReplaceLexical(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module pz\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc Caller() {\n\thelper()\n}\n\nfunc helper() {}\n",
		"b/b.go": "package b\n\nfunc New() {}\n",
	})
	// 精确边含一条词法图不可能有的跨包边（a.Caller→b.New：裸调用不可跨包）。
	aug := &stubAugmenter{edges: [][2]string{{"a.Caller", "b.New"}}}
	ix := NewIndex(&stubEmbedder{})
	ix.aug = aug
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	callerOut := append([]string(nil), ix.sym.adj["a.Caller"]...)
	helperIn := append([]string(nil), ix.sym.radj["a.helper"]...)
	newIn := append([]string(nil), ix.sym.radj["b.New"]...)
	precise := ix.precise
	ix.mu.Unlock()
	if !precise {
		t.Error("精确边应用后应置位 precise")
	}
	if len(callerOut) != 1 || callerOut[0] != "b.New" {
		t.Errorf("精确模式 a.Caller 被调 = %v, want [b.New]（精确边替换词法）", callerOut)
	}
	if len(helperIn) != 0 {
		t.Errorf("词法边应被替换: a.helper 调用方 = %v, want 空", helperIn)
	}
	if len(newIn) != 1 || newIn[0] != "a.Caller" {
		t.Errorf("b.New 调用方 = %v, want [a.Caller]", newIn)
	}
	// 符号表照建（lex 面）：精确模式下定义与候选照常可查。
	if self, _, _, cands, ok := ix.SymbolContext("a.helper"); !ok || cands != 1 || self.Path != "a/a.go" {
		t.Errorf("精确模式下符号表照建: self=%+v cands=%d ok=%v", self, cands, ok)
	}
	// 签名门控：文件集未变不重拉。
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 1 {
		t.Errorf("签名未变不应再调 augmenter: calls=%d", aug.calls)
	}
	// 文件变更（size 变）→ 签名变 → 重拉精确边。
	writeFileSet(t, dir, map[string]string{
		"a/a.go": "package a\n\nfunc Caller() {\n\thelper()\n}\n\nfunc helper() {}\n\nfunc Extra() {}\n",
	})
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 2 {
		t.Errorf("签名变更应再调 augmenter: calls=%d", aug.calls)
	}
	if _, _, _, _, ok := ix.SymbolContext("a.Extra"); !ok {
		t.Error("变更后新符号应入表")
	}
}

// TestPreciseEdgesFailureLatch 精确边失败闩（v5.10.0）：augmenter 失败降级词法
// （邻接保持、precise 不置位），本 Index 生命周期内不再重试。
func TestPreciseEdgesFailureLatch(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module latch\n\ngo 1.22\n",
		"a.go":   "package app\n\nfunc Alpha() {\n\tBeta()\n}\n\nfunc Beta() {}\n",
	})
	aug := &stubAugmenter{err: errors.New("工作副本编译失败")}
	ix := NewIndex(&stubEmbedder{})
	ix.aug = aug
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	alphaOut := append([]string(nil), ix.sym.adj["Alpha"]...)
	precise := ix.precise
	latched := ix.augFailed
	ix.mu.Unlock()
	if len(alphaOut) != 1 || alphaOut[0] != "Beta" {
		t.Errorf("augmenter 失败应保持词法邻接, got %v", alphaOut)
	}
	if precise || !latched {
		t.Errorf("失败应置失败闩且不置 precise: precise=%v latched=%v", precise, latched)
	}
	// 失败闩：第二次 Refresh（文件变更与否）不再调 augmenter。
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 1 {
		t.Errorf("失败闩后不应重试: calls=%d", aug.calls)
	}
}

// TestManagerWithEdges Manager 数据面接线（v5.10.0）：WithEdges 注入后
// Precise 透出精确模式，邻接经 Search/SymbolContext 可读；未配置/失败保持
// 词法（Precise=false）。
func TestManagerWithEdges(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module mp\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc Caller() {}\n",
		"b/b.go": "package b\n\nfunc Target() {}\n",
	})
	aug := &stubAugmenter{edges: [][2]string{{"a.Caller", "b.Target"}}}
	m := NewManager(&stubEmbedder{}, WithEdges(aug))
	ctx := context.Background()
	if m.Precise(dir) {
		t.Error("未建索引目录 Precise 应为 false")
	}
	if _, err := m.Search(ctx, dir, "target", 5, 0); err != nil {
		t.Fatal(err)
	}
	if !m.Precise(dir) {
		t.Error("精确边应用后 Precise 应为 true")
	}
	self, in, out, cands, ok, err := m.SymbolContext(ctx, dir, "b.Target")
	if err != nil || !ok {
		t.Fatalf("SymbolContext: ok=%v err=%v", ok, err)
	}
	if cands != 1 || len(in) != 1 || in[0].Symbol != "a.Caller" || in[0].Rel != relCaller || len(out) != 0 {
		t.Errorf("精确边邻接: self=%+v in=%+v out=%+v cands=%d", self, in, out, cands)
	}
	hits, err := m.Search(ctx, dir, "target", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *Hit
	for i := range hits {
		if hits[i].Symbol == "b.Target" {
			target = &hits[i]
		}
	}
	if target == nil || len(target.Neighbors) != 1 ||
		target.Neighbors[0].Symbol != "a.Caller" || target.Neighbors[0].Rel != relCaller {
		t.Errorf("search 命中邻接应来自精确边: %+v", hits)
	}
	// 失败降级：另一目录 augmenter 失败 → 词法模式，Precise=false。
	failDir := t.TempDir()
	writeFileSet(t, failDir, map[string]string{
		"c/c.go": "package c\n\nfunc Solo() {}\n",
	})
	m2 := NewManager(&stubEmbedder{}, WithEdges(&stubAugmenter{err: errors.New("编译失败")}))
	if _, err := m2.Search(ctx, failDir, "solo", 5, 0); err != nil {
		t.Fatal(err)
	}
	if m2.Precise(failDir) {
		t.Error("augmenter 失败后 Precise 应为 false（降级词法）")
	}
}

// TestGraphOnlyMode 纯图模式（v5.11.0）：NewIndex(nil) 合法——Refresh 照常扫
// 文件抽 chunk/边/建图（Path/SymbolContext 正常、Size 照常、vecs 置 nil）、
// Search/Manager.Search 报「语义检索未启用」（Index.Search 单源拦截）、Precise
// 反映 augmenter 结果（nil augmenter 恒 false）。审查管线（runner→requirement）
// 用本形态接图：图查询零 embedding 依赖，不被 ARGUS_EMBEDDING_* 绑架。
func TestGraphOnlyMode(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(nil)
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	// 图查询正常：Path 最短链与 SymbolContext 同 embedder 模式。
	chain, ok := ix.Path("Alpha", "Delta", graphHopsDefault)
	if !ok {
		t.Fatal("纯图模式 Alpha→Delta 应可达")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	self, in, out, cands, ok := ix.SymbolContext("Beta")
	if !ok || cands != 1 {
		t.Fatalf("纯图模式 SymbolContext: ok=%v cands=%d", ok, cands)
	}
	if self.Symbol != "Beta" || self.Path != "a.go" || self.Line != 9 {
		t.Errorf("self = %+v, want Beta a.go:9", self)
	}
	if len(in) != 1 || in[0].Symbol != "Alpha" || in[0].Rel != relCaller ||
		len(out) != 2 || out[0].Symbol != "Gamma" || out[1].Symbol != "util.Gamma" {
		t.Errorf("in=%+v out=%+v", in, out)
	}
	// embedding 步确实跳过：落地 fileIndex 的 vecs 置 nil；Size/Truncated 照常。
	ix.mu.Lock()
	vecsNil := ix.files["a.go"] != nil && ix.files["a.go"].vecs == nil
	ix.mu.Unlock()
	if !vecsNil {
		t.Error("纯图模式 vecs 应置 nil（embedding 步跳过）")
	}
	if ix.Size() == 0 {
		t.Error("Size 照常（chunk 照常落地）")
	}
	if ix.Truncated() {
		t.Error("小夹具不应触达上限")
	}
	// Search 拦截（Index 直用）与 Manager 路径（经 Index.Search 同一拦截单源）。
	if _, err := ix.Search(ctx, "db", 3, 0); err == nil || !strings.Contains(err.Error(), "语义检索未启用") {
		t.Errorf("纯图模式 Search 应报「语义检索未启用」, got %v", err)
	}
	m := NewManager(nil)
	if _, err := m.Search(ctx, dir, "db", 3, 0); err == nil || !strings.Contains(err.Error(), "语义检索未启用") {
		t.Errorf("纯图 Manager.Search 应同款报错, got %v", err)
	}
	// Manager 图查询面照常（Trace/SymbolContext 不碰 embedder）。
	if _, found, err := m.Trace(ctx, dir, "Alpha", "Gamma"); err != nil || !found {
		t.Errorf("纯图 Manager.Trace: found=%v err=%v", found, err)
	}
	// Precise 读面：nil augmenter 恒 false；注入 augmenter 成功后 true。
	if ix.Precise() {
		t.Error("nil augmenter 时 Index.Precise 应为 false")
	}
	if m.Precise(dir) {
		t.Error("nil augmenter 时 Manager.Precise 应为 false")
	}
	aug := &stubAugmenter{edges: [][2]string{{"util.Gamma", "Delta"}}}
	m2 := NewManager(nil, WithEdges(aug))
	if _, _, err := m2.Trace(ctx, dir, "Alpha", "Gamma"); err != nil {
		t.Fatal(err)
	}
	if !m2.Precise(dir) {
		t.Error("精确边应用后 Manager.Precise 应为 true（纯图模式同款判定）")
	}
}
