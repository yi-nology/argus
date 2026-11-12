package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oekit/logx"

	"github.com/oagentkit/textx/codeintel"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/semsearch"
)

// v3.15.0 P0-1：CodeGraph 非 nil 时 R3/R4 工具表挂 get_symbol/get_references，
// nil 时不挂（既有行为不变）。
func TestR3ToolsCodegraph(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package app\n\nfunc Handler() error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := codeintel.NewIndex(dir, 10)
	if err != nil {
		t.Fatal(err)
	}

	names := func(w *Workflow) []string {
		tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}

	w := &Workflow{
		CodeGraph:        idx,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}
	with := names(w)
	hasSym, hasRefs := false, false
	for _, n := range with {
		switch n {
		case "get_symbol":
			hasSym = true
		case "get_references":
			hasRefs = true
		}
	}
	if !hasSym || !hasRefs {
		t.Fatalf("工具表缺 codegraph 工具: %v", with)
	}

	// nil CodeGraph：不挂（既有行为）
	without := names(&Workflow{
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	})
	for _, n := range without {
		if n == "get_symbol" || n == "get_references" {
			t.Fatalf("nil CodeGraph 不应挂 %s: %v", n, without)
		}
	}
}

// v5.11.0：Graph（消歧符号图，纯图模式）非 nil 时 R3/R4 工具表挂
// get_trace/get_symbol_context（与 CodeGraph 四件套独立判空——CodeGraph 缺席
// 同样挂载），nil 时不挂。工具执行断言：链渲染同款视觉（—calls→/-called_by→ +
// file:line）、全景渲染（定义/调用方/被调/候选合并行）、降级文案（查无结果是
// 提示文本非 error，边界注明按 Precise 动态）。fixture 仿 semsearch 图测试：
// t.TempDir 写 Go 文件 → semsearch.NewIndex(nil)+Refresh → Workflow{Graph: ix}。
func TestR3ToolsGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module wfgraph\n\ngo 1.22\n")
	writeFile("a.go", `package app

import "wfgraph/util"

func Alpha() {
	Beta()
}

func Beta() {
	Gamma()
	util.Gamma()
}
`)
	writeFile("b.go", "package app\n\nfunc Gamma() {}\n")
	writeFile("util/util.go", "package util\n\nfunc Gamma() {}\n")
	// 同名多候选夹具：两目录各一个 New（裸名 New 本身不是节点键 → resolve 出两
	// 候选，邻接合并面；根包裸名 Gamma 是节点键，resolve 精确单候选，测不了合并）。
	writeFile("svc/helper.go", "package svc\n\nfunc New() {\n\tBoot()\n}\n\nfunc Boot() {}\n")
	writeFile("admin/admin.go", "package admin\n\nfunc New() {\n\tInit()\n}\n\nfunc Init() {}\n")
	gx := semsearch.NewIndex(nil)
	if err := gx.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	names := func(w *Workflow) []string {
		tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}
	contains := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}

	// 挂载：Graph 非 nil（CodeGraph 缺席）→ 两工具进表。
	with := names(&Workflow{
		Graph:            gx,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	})
	if !contains(with, "get_trace") || !contains(with, "get_symbol_context") {
		t.Fatalf("Graph 非 nil 应挂 get_trace/get_symbol_context: %v", with)
	}
	// nil Graph：不挂（既有行为不变）。
	for _, n := range names(&Workflow{
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}) {
		if n == "get_trace" || n == "get_symbol_context" {
			t.Fatalf("nil Graph 不应挂 %s", n)
		}
	}

	// 工具执行面。
	w := &Workflow{
		Graph:            gx,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}
	tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.BaseTool{}
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil {
			byName[info.Name] = tl
		}
	}
	run := func(name, argsJSON string) string {
		t.Helper()
		tl, ok := byName[name]
		if !ok {
			t.Fatalf("工具 %s 未挂载", name)
		}
		raw, err := tl.(tool.InvokableTool).InvokableRun(context.Background(), argsJSON)
		if err != nil {
			t.Fatalf("%s 查询类失败应回提示文本非 error: %v", name, err)
		}
		var out symOut
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("%s 输出非 JSON: %v\n%s", name, err, raw)
		}
		return out.Result
	}

	// get_trace 命中：链渲染同款视觉（每步 file:line + 边向箭头）+ 数据区围栏
	// + 词法级边界注明（未配 augmenter → Precise=false）。
	tr := run("get_trace", `{"from":"Alpha","to":"Gamma"}`)
	for _, want := range []string{"get_trace 调用链（数据）", "—calls→", "Alpha (a.go:5)", "Beta (a.go:9)", "词法级边", "get_file 核实"} {
		if !strings.Contains(tr, want) {
			t.Errorf("get_trace 渲染缺 %q:\n%s", want, tr)
		}
	}
	// 逆向链（无向 BFS）标 called_by。
	trRev := run("get_trace", `{"from":"Gamma","to":"Alpha"}`)
	if !strings.Contains(trRev, "—called_by→") {
		t.Errorf("逆向链应标 called_by:\n%s", trRev)
	}
	// 未找到：提示文本非 error，边界注明仍在。
	trMiss := run("get_trace", `{"from":"Alpha","to":"NoSuch"}`)
	if !strings.Contains(trMiss, "未找到 Alpha → NoSuch") || !strings.Contains(trMiss, "词法级边") {
		t.Errorf("get_trace 未找到降级文案不符:\n%s", trMiss)
	}

	// get_symbol_context 命中：定义 + 调用方/被调全景（单候选无合并行）。
	sc := run("get_symbol_context", `{"name":"Beta"}`)
	for _, want := range []string{
		"get_symbol_context 图上下文（数据）", "定义：Beta (a.go:9)",
		"调用方（≤8）：Alpha", "被调（≤8）：Gamma", "词法级边",
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("get_symbol_context 渲染缺 %q:\n%s", want, sc)
		}
	}
	if strings.Contains(sc, "邻接已合并") {
		t.Errorf("单候选不应出候选合并行:\n%s", sc)
	}
	// 同名多候选（裸名 New 两处：svc/admin 目录）→ 合并行 + 邻接合并。
	scMulti := run("get_symbol_context", `{"name":"New"}`)
	if !strings.Contains(scMulti, "同名候选 2 处，邻接已合并") {
		t.Errorf("裸名 New 两候选应出合并行:\n%s", scMulti)
	}
	// v6.0.0：SymbolContext in/out 带定义位置（渲染「符号 (file:line)」——
	// renderGraphNeighbors 既定视觉，此前 loc 恒缺省只显符号名）。
	if !strings.Contains(scMulti, "被调（≤8）：admin.Init (admin/admin.go:7)、svc.Boot (svc/helper.go:7)") {
		t.Errorf("多候选邻接应合并被调:\n%s", scMulti)
	}
	// 未解析：提示文本非 error。
	scMiss := run("get_symbol_context", `{"name":"NoSuch"}`)
	if !strings.Contains(scMiss, "未在符号图内解析到符号") {
		t.Errorf("get_symbol_context 未找到降级文案不符:\n%s", scMiss)
	}
}

// wcStubEmbedder 词袋向量化 stub（v6.1.0，仿 internal/semsearch stubEmbedder，
// requirement 包内本地定义——测试依赖不外溢）：词表三词正交三维、词内子串计数，
// 「纯 db 文本」与查询「db」相似度 1.0、「db+http 混合文本」约 0.7，排序可预期，
// 不碰网络。
type wcStubEmbedder struct{}

var wcStubVocab = []string{"db", "http", "parse"}

func (s *wcStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v := make([]float32, len(wcStubVocab))
		lower := strings.ToLower(t)
		for i, w := range wcStubVocab {
			v[i] = float32(strings.Count(lower, w))
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *wcStubEmbedder) Dim() int { return len(wcStubVocab) }

// v6.1.0：Graph Searchable（embedder 非空）时 R3/R4 工具表挂 search_sem（审查
// 面，与 MCP 面同名不同执行面），纯图模式（NewIndex(nil)）不挂——get_trace/
// get_symbol_context 只看 Graph 非 nil，照常挂载（独立判空）。执行断言：命中
// 渲染（序号+path:line+symbol+score+600 rune 截断+数据区围栏）、符号命中附
// 调用邻接行（调用方/被调分组）、无命中提示文本非 error、topK 钳制（缺省 5/
// 上界 10/透传小值）。
func TestR3ToolsSearchSem(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module wfsem\n\ngo 1.22\n")
	// QueryDB 纯 db 词袋（余弦 1.0 稳居首条），调用方 Handle / 被调 SaveDB
	// 齐备——首条命中即出「调用方/被调」双组邻接行。
	writeFile("db.go", `package app

// QueryDB 从 DB 读用户。
func QueryDB() {
	SaveDB()
}
`)
	writeFile("store.go", "package app\n\n// SaveDB 把用户写回 DB 存储（HTTP 层调用）。\nfunc SaveDB() {}\n")
	writeFile("api.go", "package app\n\n// Handle HTTP 入口，查 DB。\nfunc Handle() {\n\tQueryDB()\n}\n")
	// 凑 chunk 数（topK 上界钳制观察需要 >10 个含 db chunk）；文本混入 http 压低
	// 余弦，不与 QueryDB 争首条。
	for i := range 12 {
		writeFile(fmt.Sprintf("pad/p%02d.go", i),
			fmt.Sprintf("package pad\n\n// DBPad%02d 缓存 HTTP 会话。\nfunc DBPad%02d() {}\n", i, i))
	}
	stub := &wcStubEmbedder{}
	gx := semsearch.NewIndex(stub)
	if err := gx.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !gx.Searchable() {
		t.Fatalf("NewIndex(stub)+Refresh 后应 Searchable=true")
	}

	names := func(w *Workflow) []string {
		tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}
	contains := func(list []string, want string) bool {
		for _, n := range list {
			if n == want {
				return true
			}
		}
		return false
	}
	w := &Workflow{
		Graph:            gx,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}
	if !contains(names(w), "search_sem") {
		t.Fatalf("Searchable Graph 应挂 search_sem: %v", names(w))
	}
	tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sem tool.BaseTool
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil && info.Name == "search_sem" {
			sem = tl
			break
		}
	}
	run := func(argsJSON string) string {
		t.Helper()
		raw, err := sem.(tool.InvokableTool).InvokableRun(context.Background(), argsJSON)
		if err != nil {
			t.Fatalf("search_sem 查询类失败应回提示文本非 error: %v", err)
		}
		var out symOut
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("search_sem 输出非 JSON: %v\n%s", err, raw)
		}
		return out.Result
	}

	// 命中渲染：数据区围栏 + 首条 path:line+symbol+score + 邻接行（调用方/被调
	// 分组，根目录键无 dir 前缀）。QueryDB 纯词袋余弦 1.0 稳居 [1]。
	hit := run(`{"query":"db","topK":3}`)
	for _, want := range []string{
		"search_sem 检索结果（数据）", "命中 3 条", "[1] db.go:3 QueryDB score=1.000",
		"引用前须 get_file 核实", "邻接：调用方 Handle；被调 SaveDB",
	} {
		if !strings.Contains(hit, want) {
			t.Errorf("search_sem 命中渲染缺 %q:\n%s", want, hit)
		}
	}

	// topK 钳制：缺省 5（chunk 15 个 > 5，钳制生效才可能 5 条）、上界 10、
	// 小值透传。
	if got := run(`{"query":"db"}`); !strings.Contains(got, "命中 5 条") {
		t.Errorf("topK 缺省应钳 5:\n%s", got)
	}
	if got := run(`{"query":"db","topK":99}`); !strings.Contains(got, "命中 10 条") {
		t.Errorf("topK=99 应钳上界 10:\n%s", got)
	}
	if got := run(`{"query":"db","topK":2}`); !strings.Contains(got, "命中 2 条") {
		t.Errorf("topK=2 应透传:\n%s", got)
	}

	// 空索引（可检索但无内容）：无命中是正常查询结果——提示文本非 error。
	empty := semsearch.NewIndex(stub)
	if err := empty.Refresh(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	emptyHit := run2(&Workflow{Graph: empty, Log: logx.Nop(),
		GetFileMaxChars: 1000, GetIssueMaxChars: 1000, GetDiffMaxChars: 1000}, `{"query":"db"}`)
	if !strings.Contains(emptyHit, "语义检索无命中") {
		t.Errorf("空索引应回无命中提示文本:\n%s", emptyHit)
	}

	// 纯图模式（NewIndex(nil)）：search_sem 不挂（挂了必然报「语义检索未启用」
	// 的死工具），get_trace/get_symbol_context 照挂（只看 Graph 非 nil）。
	pure := semsearch.NewIndex(nil)
	if err := pure.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if pure.Searchable() {
		t.Fatalf("NewIndex(nil) 应 Searchable=false")
	}
	pureNames := names(&Workflow{Graph: pure, Log: logx.Nop(),
		GetFileMaxChars: 1000, GetIssueMaxChars: 1000, GetDiffMaxChars: 1000})
	if contains(pureNames, "search_sem") {
		t.Errorf("纯图模式不应挂 search_sem: %v", pureNames)
	}
	if !contains(pureNames, "get_trace") || !contains(pureNames, "get_symbol_context") {
		t.Errorf("纯图模式应仍挂 get_trace/get_symbol_context: %v", pureNames)
	}
}

// run2 独立 Workflow 的 search_sem 执行（空索引降级断言用；与上方 run 分开是
// 因为工具闭包绑定各自 Workflow）。
func run2(w *Workflow, argsJSON string) string {
	tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
	if err != nil {
		return "r3Tools 失败: " + err.Error()
	}
	for _, tl := range tools {
		if info, err := tl.Info(context.Background()); err == nil && info.Name == "search_sem" {
			raw, rerr := tl.(tool.InvokableTool).InvokableRun(context.Background(), argsJSON)
			if rerr != nil {
				return "InvokableRun 失败: " + rerr.Error()
			}
			var out symOut
			if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
				return "输出非 JSON: " + jerr.Error()
			}
			return out.Result
		}
	}
	return "search_sem 未挂载"
}
