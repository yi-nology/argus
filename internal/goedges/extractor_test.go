// extractor_test.go —— goedges 精确边抽取面：纯 stdlib 依赖的可编译 fixture
// （t.TempDir 内自建 module fix，保证离线/无 GOPROXY 沙箱不触网）钉死——
// 具体方法调用边（接收者基名精确）、跨包调用剥前缀、跨包同名函数不混
// （store.New vs other.New）、caller 键与 NodeKey 契约一致（期望集一律经
// semsearch.NodeKey 构造，键格式单源）、函数字面量归外层、泛型接收者基名、
// 接口方法调用解析到接口方法声明（抽取层如实产键，消费侧存在性过滤丢弃）、
// stdlib 调用剪除、破损仓 error、超上限 error、缺 go.mod error。
// 集成面（Manager 注入/失败闩/存在性过滤）由 internal/semsearch 桩测试覆盖，
// 本文件不重复。
package goedges

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"argus/internal/semsearch"
)

// fixture 文件集（module fix；全部 stdlib 依赖）。
var fixtureFiles = map[string]string{
	"go.mod": `module fix

go 1.21
`,
	"root.go": `package fix

import "fix/store"

// Bootstrap 根目录声明（NodeKey dir 段空串钉死用）。
func Bootstrap() *store.Item { return store.New("root") }
`,
	"store/store.go": `package store

// Item 演示实体。
type Item struct{ Name string }

// Tag 跨包方法调用目标。
func (it *Item) Tag() string { return "item:" + it.Name }

// New 构造器（与 other.New 同名——跨包同名消歧钉死用）。
func New(name string) *Item { return &Item{Name: name} }

// List 泛型演示（接收者 *List[T]，基名剥到 List）。
type List[T any] struct{ items []T }

// Push 泛型接收者方法；append 为内建（typeutil.Callee 返回非 Func，剪除）。
func (l *List[T]) Push(v T) { l.items = append(l.items, v) }
`,
	"other/other.go": `package other

// Box 演示实体（与 store 无关，仅同名 New 消歧用）。
type Box struct{}

// Wrap 跨包方法调用目标。
func (b *Box) Wrap() string { return "box" }

// New 构造器（与 store.New 同名）。
func New() *Box { return &Box{} }
`,
	"iface/iface.go": `package iface

// Tagger 演示接口：Tag 声明是接口元素而非 FuncDecl——抽取层对接口方法
// 调用如实产键 NodeKey("iface","Tagger","Tag")，该键非 chunk 符号节点，
// 由 semsearch 侧存在性过滤丢弃（接口分派不可见的边界钉死用）。
type Tagger interface {
	Tag() string
}
`,
	"app/app.go": `package app

import (
	"fmt"

	"fix/iface"
	"fix/other"
	"fix/store"
)

// Svc 演示服务。
type Svc struct{ it *store.Item }

// Run 全调用形态集：跨包函数、跨包同名函数、跨包方法、同包方法。
func (s *Svc) Run() string {
	it := store.New("x")
	bx := other.New()
	_ = bx.Wrap()
	return s.describe(it)
}

// describe 同包方法调用（Run→describe 边 + describe→Item.Tag 边）。
func (s *Svc) describe(it *store.Item) string { return it.Tag() }

// Use 接口方法调用：types 级解析到接口方法声明（边界，见 iface 包注释）。
func Use(t iface.Tagger) string { return t.Tag() }

// GenericDemo 泛型接收者调用：两端基名剥到 List。
func GenericDemo() *store.List[string] {
	l := &store.List[string]{}
	l.Push("x")
	return l
}

// LitDemo 函数字面量：字面量内调用归外层 FuncDecl（边界钉死用）。
func LitDemo() func() string {
	return func() string { return store.New("lit").Tag() }
}

// Helper stdlib 调用（fmt.Sprintf）：剪除不建边（期望集不含即断言）。
func Helper() string { return fmt.Sprintf("%d", 1) }
`,
}

// writeFixture 落盘完整可编译模块，返回工作副本根目录。
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, src := range fixtureFiles {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEdges_Precise 主面：期望集一律经 semsearch.NodeKey 构造（键契约单源），
// 与 Edges 输出全等比对——多一条（stdlib/内建泄漏）少一条（解析漏抽）皆红。
func TestEdges_Precise(t *testing.T) {
	dir := writeFixture(t)
	edges, err := New().Edges(context.Background(), dir)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	want := [][2]string{
		// 根目录声明：dir 段空串。
		{semsearch.NodeKey("", "", "Bootstrap"), semsearch.NodeKey("store", "", "New")},
		// 泛型接收者：两端基名剥到 List。
		{semsearch.NodeKey("app", "", "GenericDemo"), semsearch.NodeKey("store", "List", "Push")},
		// 函数字面量归外层 LitDemo（字面量内 store.New + Tag 两边）。
		{semsearch.NodeKey("app", "", "LitDemo"), semsearch.NodeKey("store", "Item", "Tag")},
		{semsearch.NodeKey("app", "", "LitDemo"), semsearch.NodeKey("store", "", "New")},
		// 跨包同名函数不混：store.New 与 other.New 各自精确。
		{semsearch.NodeKey("app", "Svc", "Run"), semsearch.NodeKey("app", "Svc", "describe")},
		{semsearch.NodeKey("app", "Svc", "Run"), semsearch.NodeKey("other", "Box", "Wrap")},
		{semsearch.NodeKey("app", "Svc", "Run"), semsearch.NodeKey("other", "", "New")},
		{semsearch.NodeKey("app", "Svc", "Run"), semsearch.NodeKey("store", "", "New")},
		{semsearch.NodeKey("app", "Svc", "describe"), semsearch.NodeKey("store", "Item", "Tag")},
		// 接口方法调用解析到接口方法声明（消费侧存在性过滤丢弃，见包文档）。
		{semsearch.NodeKey("app", "", "Use"), semsearch.NodeKey("iface", "Tagger", "Tag")},
	}
	// 与 Edges 同款排序后全等比对。
	sort.Slice(want, func(i, j int) bool {
		if want[i][0] != want[j][0] {
			return want[i][0] < want[j][0]
		}
		return want[i][1] < want[j][1]
	})
	if len(edges) != len(want) {
		t.Fatalf("边数 = %d, want %d；got=%v", len(edges), len(want), edges)
	}
	for i, e := range edges {
		if e != want[i] {
			t.Errorf("edge[%d] = %v, want %v", i, e, want[i])
		}
	}
	// caller 键格式与 NodeKey 一致的显式抽查（根目录无前缀形态）。
	if got := edges[0][0]; got != semsearch.NodeKey("", "", "Bootstrap") || got != "Bootstrap" {
		t.Errorf("根目录 caller 键 = %q, want NodeKey(\"\",\"\",\"Bootstrap\")=%q", got, "Bootstrap")
	}
}

// TestEdges_BrokenRepo 破损仓（语法错）：任何仓内包带 Errors → error
// （装配侧降级词法边的触发面）。
func TestEdges_BrokenRepo(t *testing.T) {
	dir := writeFixture(t)
	broken := "package app\n\nfunc (Broken() string { return\n" // 语法错
	if err := os.WriteFile(filepath.Join(dir, "app", "app.go"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Edges(context.Background(), dir); err == nil {
		t.Fatal("破损仓 Edges 未返回 error（期望降级词法）")
	}
}

// TestEdges_FileCap 超上限：fixture 造不了 3000 文件，经 MaxFiles 字段注入
// 小值验证超限路径（DefaultMaxFiles=3000 的成本闸语义）。
func TestEdges_FileCap(t *testing.T) {
	dir := writeFixture(t)
	x := New()
	x.MaxFiles = 1 // fixture 有 6 个 Go 文件
	if _, err := x.Edges(context.Background(), dir); err == nil {
		t.Fatal("超上限 Edges 未返回 error")
	}
}

// TestEdges_MissingGoMod 缺 go.mod：module 声明不可得 → error（工作副本
// 非 Go module 形态，降级词法）。
func TestEdges_MissingGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Edges(context.Background(), dir); err == nil {
		t.Fatal("缺 go.mod Edges 未返回 error")
	}
}
