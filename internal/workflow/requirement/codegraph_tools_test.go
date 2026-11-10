package requirement

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oekit/logx"

	"argus/internal/plugin"
	"argus/internal/semsearch"
	"github.com/oagentkit/textx/codeintel"
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
	if !strings.Contains(scMulti, "被调（≤8）：admin.Init、svc.Boot") {
		t.Errorf("多候选邻接应合并被调:\n%s", scMulti)
	}
	// 未解析：提示文本非 error。
	scMiss := run("get_symbol_context", `{"name":"NoSuch"}`)
	if !strings.Contains(scMiss, "未在符号图内解析到符号") {
		t.Errorf("get_symbol_context 未找到降级文案不符:\n%s", scMiss)
	}
}
