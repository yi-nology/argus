// callers_graph_test.go —— v6.0.0 L2 调用方升级单测：消歧符号图优先、逐符号
// 回退。夹具两目录同名 New()（svc/other 各一）+ 各自独立调用方——词法路径
// （裸名匹配）的跨目录同名误报与图路径（NodeKey 消歧）的结果在此对拍；
// 部分回退场景用 >maxFileChunks 的巨型文件模拟「符号不在图内（预算截断）」。
package impactview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/semsearch"

	"github.com/oagentkit/textx/codeintel"
)

// writeNamesakeFixture 生成同名符号夹具：
//
//	svc/svc.go     type S + func New() *S（改动目标，New 在第 5 行）
//	other/other.go type O + func New() *O（同名异目录）
//	svc/use.go     func UseSvc() *S { return New() }（svc.New 真实调用方）
//	other/use.go   func UseOther() *O { return New() }（other.New 调用方——词法误报源）
func writeNamesakeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.22\n",
		"svc/svc.go": `package svc

type S struct{}

func New() *S { return &S{} }
`,
		"other/other.go": `package other

type O struct{}

func New() *O { return &O{} }
`,
		"svc/use.go": `package svc

func UseSvc() *S { return New() }
`,
		"other/use.go": `package other

func UseOther() *O { return New() }
`,
	})
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeBigFile 在 dir 下生成 svc/big.go：>maxFileChunks（64）个 decl，排尾的
// Legacy/UseLegacy 被 semsearch 单文件 chunk 上限挤出图外（codeintel 无此上限
// 照常索引）——「符号不在图内」的真实来源。返回 Legacy 的定义行号。
func writeBigFile(t *testing.T, dir string) (body string, legacyLine int) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package svc\n\n")
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&b, "func F%02d() {}\n\n", i)
	}
	b.WriteString("func Legacy() {}\n\nfunc UseLegacy() { Legacy() }\n")
	body = b.String()
	writeFiles(t, dir, map[string]string{"svc/big.go": body})
	legacyLine = 1 + strings.Count(body[:strings.Index(body, "func Legacy()")], "\n")
	return body, legacyLine
}

// callerEdges 收集 Rel=caller 的边（快照 finalize 已按 (From,To,Rel) 排序去重）。
func callerEdges(s *Snapshot) []Edge {
	var out []Edge
	for _, e := range s.Edges {
		if e.Rel == RelCaller {
			out = append(out, e)
		}
	}
	return out
}

func hasCallerEdgeTo(s *Snapshot, file string) bool {
	for _, e := range callerEdges(s) {
		if e.To == "file:"+file {
			return true
		}
	}
	return false
}

func notesContain(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// TestCollectSymbolsGraphDisambiguation 图路径消歧：HunkRanges 触达 svc 的
// New → caller 边只来自 svc 的真实调用方；other 的调用方（词法裸名会误报的
// 形态）不得混入。全图路径落 CallerSource=graph_lexical + Notes 注明。
func TestCollectSymbolsGraphDisambiguation(t *testing.T) {
	dir := writeNamesakeFixture(t)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		t.Fatalf("建 codeintel 索引: %v", err)
	}
	gx := semsearch.NewIndex(nil) // 纯图模式（无 augmenter → 词法邻接档）
	if err := gx.Refresh(context.Background(), dir); err != nil {
		t.Fatalf("建消歧符号图: %v", err)
	}
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"svc/svc.go"},
		HunkRanges:   map[string][][2]int{"svc/svc.go": {{5, 5}}},
		Index:        idx,
		Graph:        gx,
		MaxCommits:   -1,
	})
	if s.Stats.CallerSource != CallerSrcGraphLexical {
		t.Fatalf("caller_source = %q, want %q", s.Stats.CallerSource, CallerSrcGraphLexical)
	}
	if !hasCallerEdgeTo(s, "svc/use.go") {
		t.Fatalf("svc.New 真实调用方 svc/use.go 缺失: %+v", callerEdges(s))
	}
	if hasCallerEdgeTo(s, "other/use.go") {
		t.Fatalf("other 的调用方混入 svc.New: %+v", callerEdges(s))
	}
	if s.hasNode("file:other/use.go") {
		t.Fatalf("other/use.go 不应成为调用方节点: %+v", nodeIDs(s))
	}
	// Detail 带消歧后的调用方名（displayOf 剥目录前缀，对齐 codeintel 形态）
	for _, n := range s.Nodes {
		if n.ID == "file:svc/use.go" && n.Detail != "调用方：UseSvc" {
			t.Fatalf("调用方节点 Detail = %q, want 调用方：UseSvc", n.Detail)
		}
	}
	// 全图路径 Notes 注明（词法档——纯图 Index 无精确边钩子）
	if !notesContain(s.Notes, "消歧符号图") || !notesContain(s.Notes, "词法级") {
		t.Fatalf("缺全图路径注明: %+v", s.Notes)
	}
	if notesContain(s.Notes, "回退词法") {
		t.Fatalf("全图路径不应有回退注明: %+v", s.Notes)
	}
}

// TestCollectSymbolsNilGraphCodeintel Graph=nil 走既有 codeintel 路径：行为
// 不变（裸名匹配的既定边界——两目录调用方都进），CallerSource=codeintel、
// 无图注明。
func TestCollectSymbolsNilGraphCodeintel(t *testing.T) {
	dir := writeNamesakeFixture(t)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		t.Fatalf("建 codeintel 索引: %v", err)
	}
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"svc/svc.go"},
		HunkRanges:   map[string][][2]int{"svc/svc.go": {{5, 5}}},
		Index:        idx,
		MaxCommits:   -1,
	})
	if s.Stats.CallerSource != CallerSrcCodeintel {
		t.Fatalf("caller_source = %q, want %q", s.Stats.CallerSource, CallerSrcCodeintel)
	}
	if !hasCallerEdgeTo(s, "svc/use.go") || !hasCallerEdgeTo(s, "other/use.go") {
		t.Fatalf("词法路径两目录调用方都应进（既有行为不变）: %+v", callerEdges(s))
	}
	if notesContain(s.Notes, "消歧符号图") || notesContain(s.Notes, "回退词法") {
		t.Fatalf("codeintel 路径不应有图相关注明: %+v", s.Notes)
	}
}

// TestCollectSymbolsPartialFallback 部分回退：svc.New 在图内（图路径），
// Legacy 被单文件 chunk 上限挤出图外（ok=false → 逐符号回退 codeintel 接住，
// 不整层废弃）。混合档落 CallerSource=mixed + 条数注明。
func TestCollectSymbolsPartialFallback(t *testing.T) {
	dir := writeNamesakeFixture(t)
	_, legacyLine := writeBigFile(t, dir)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		t.Fatalf("建 codeintel 索引: %v", err)
	}
	gx := semsearch.NewIndex(nil)
	if err := gx.Refresh(context.Background(), dir); err != nil {
		t.Fatalf("建消歧符号图: %v", err)
	}
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"svc/svc.go", "svc/big.go"},
		HunkRanges: map[string][][2]int{
			"svc/svc.go": {{5, 5}},
			"svc/big.go": {{legacyLine, legacyLine}},
		},
		Index:      idx,
		Graph:      gx,
		MaxCommits: -1,
	})
	if s.Stats.CallerSource != CallerSrcMixed {
		t.Fatalf("caller_source = %q, want %q", s.Stats.CallerSource, CallerSrcMixed)
	}
	// 图路径符号照常消歧
	if !hasCallerEdgeTo(s, "svc/use.go") || hasCallerEdgeTo(s, "other/use.go") {
		t.Fatalf("svc.New 图路径消歧失效: %+v", callerEdges(s))
	}
	// 回退符号由 codeintel 接住（UseLegacy 调用位）
	if !hasCallerEdgeTo(s, "svc/big.go") {
		t.Fatalf("Legacy 回退 codeintel 后调用方缺失: %+v", callerEdges(s))
	}
	if !notesContain(s.Notes, "1 个触达符号经图解析，1 个回退词法") {
		t.Fatalf("缺混合档条数注明: %+v", s.Notes)
	}
}

// TestClassifyCallerSource 来源档位分类纯函数：四档全分支钉死。
func TestClassifyCallerSource(t *testing.T) {
	cases := []struct {
		name          string
		graphN, fallN int
		precise       bool
		want          string
	}{
		{"全图精确", 2, 0, true, CallerSrcGraphPrecise},
		{"全图词法", 2, 0, false, CallerSrcGraphLexical},
		{"全回退", 0, 3, false, CallerSrcCodeintel},
		{"混合", 1, 1, false, CallerSrcMixed},
		{"混合精确也归混合", 1, 2, true, CallerSrcMixed},
	}
	for _, c := range cases {
		if got := classifyCallerSource(c.graphN, c.fallN, c.precise); got != c.want {
			t.Errorf("%s: classifyCallerSource(%d,%d,%v) = %q, want %q",
				c.name, c.graphN, c.fallN, c.precise, got, c.want)
		}
	}
}

// TestDisplayOf 图符号键 → 展示名：剥 dir 前缀（dir 段含 "/" 一并剥）；
// 根目录键无前缀原样返回；键与文件目录不齐（防御形态）原样返回。
func TestDisplayOf(t *testing.T) {
	cases := []struct{ path, symbol, want string }{
		{"svc/use.go", "svc.UseSvc", "UseSvc"},
		{"main.go", "Main", "Main"},
		{"internal/app/x.go", "internal/app.Store.GetUser", "Store.GetUser"},
		{"internal/app/x.go", "other.Key", "other.Key"}, // 前缀不齐不误剥
		{"a/b/c/x.go", "a/b/c.T.M", "T.M"},
	}
	for _, c := range cases {
		if got := displayOf(c.path, c.symbol); got != c.want {
			t.Errorf("displayOf(%q,%q) = %q, want %q", c.path, c.symbol, got, c.want)
		}
	}
}
