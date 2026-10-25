// snapshot_test.go —— 影响面管线单测：fixture 临时仓覆盖 L0/L1/L2/L3/共变/
// 风险分/渲染三形态/确定性。git 共变用例依赖本机 git 命令（CI 同 argus 运行
// 环境，恒有）。
package impactview

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/textx/codeintel"
)

// writeFixture 生成一个临时 Go 模块：
//
//	pkga/a.go        Func + method（改动目标）
//	pkga/a_test.go   TestFunc（改动包自带测试）
//	pkgb/b.go        import pkga，调 Func + method（直接依赖方，带测试）
//	pkgc/c.go        import pkgb（二跳依赖方）
//	pkgd/d.go        不相关包
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.22\n",
		"pkga/a.go": `package pkga

// Func 被测函数。
func Func(x int) int { return x + 1 }

// Item 被测类型。
type Item struct{ N int }

// Grow 被测方法。
func (i Item) Grow() Item { i.N++; return i }
`,
		"pkga/a_test.go": `package pkga

import "testing"

func TestFunc(t *testing.T) { _ = Func(1) }
`,
		"pkgb/b.go": `package pkgb

import "example.com/fix/pkga"

// Wrap 调用方。
func Wrap(x int) int { return pkga.Func(x) }

// Bundle 方法调用方。
func Bundle() int { i := pkga.Item{N: 1}; return i.Grow().N }
`,
		"pkgb/b_test.go": `package pkgb

import "testing"

func TestWrap(t *testing.T) { _ = Wrap(1) }
`,
		"pkgc/c.go": `package pkgc

import "example.com/fix/pkgb"

// Deep 二跳调用。
func Deep() int { return pkgb.Wrap(2) }
`,
		"pkgd/d.go": `package pkgd

// Unrelated 无关。
func Unrelated() {}
`,
	}
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// gitCmd 在临时仓跑 git（测试初始化用）。
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func changedOf(paths ...string) map[string][][2]int {
	m := map[string][][2]int{}
	for _, p := range paths {
		m[p] = [][2]int{{1, 20}} // 整文件视作触达
	}
	return m
}

// TestBuildLayers 分层管线端到端：L0 文件 → L1 闭包 → L2 调用方 → L3 测试。
func TestBuildLayers(t *testing.T) {
	dir := writeFixture(t)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		t.Fatalf("建索引: %v", err)
	}
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"pkga/a.go"},
		HunkRanges:   changedOf("pkga/a.go"),
		DiffText:     "+func Func(x, y int) int",
		Index:        idx,
		MaxCommits:   -1, // 共变另行用例
	})
	if s.Version != SnapshotVersion {
		t.Fatalf("version = %d", s.Version)
	}
	if len(s.Changed) != 1 || s.Changed[0] != "pkga/a.go" {
		t.Fatalf("changed = %v", s.Changed)
	}
	// L1：pkgb（一跳）与 pkgc（二跳）必须入闭包；pkgd 不入
	pkgPaths := map[string]PkgImpact{}
	for _, p := range s.Packages {
		pkgPaths[p.Path] = p
	}
	if _, ok := pkgPaths["pkgb"]; !ok {
		t.Fatalf("pkgb 未入闭包: %+v", s.Packages)
	}
	if _, ok := pkgPaths["pkgc"]; !ok {
		t.Fatalf("pkgc 未入闭包: %+v", s.Packages)
	}
	if _, ok := pkgPaths["pkgd"]; ok {
		t.Fatal("pkgd 不应入闭包")
	}
	if pkgPaths["pkgb"].Depth != 1 || pkgPaths["pkgc"].Depth != 2 {
		t.Fatalf("depth 错: %+v %+v", pkgPaths["pkgb"], pkgPaths["pkgc"])
	}
	if pkgPaths["pkgb"].Tests != 1 {
		t.Fatalf("pkgb 测试数 = %d", pkgPaths["pkgb"].Tests)
	}
	// L3：pkgb/pkga 测试节点存在
	if !s.hasNode("test:pkgb") || !s.hasNode("test:pkga") {
		t.Fatalf("测试节点缺失: %+v", nodeIDs(s))
	}
	// L2：Func 的调用方 pkgb/b.go 必须出现（AST 精化后带封闭函数）
	foundWrap := false
	for _, n := range s.Nodes {
		if n.Kind == KindFile && n.ID == "file:pkgb/b.go" {
			foundWrap = true
		}
	}
	if !foundWrap {
		t.Fatalf("调用方 pkgb/b.go 缺失: %+v", nodeIDs(s))
	}
	// 边方向契约：符号 → 调用方
	hasCallerEdge := false
	for _, e := range s.Edges {
		if e.Rel == RelCaller && strings.HasPrefix(e.To, "file:pkgb/b.go") {
			hasCallerEdge = true
		}
	}
	if !hasCallerEdge {
		t.Fatalf("caller 边缺失: %+v", s.Edges)
	}
	// 风险：导出符号 + 签名变更 → 至少 medium
	if s.Risk.Score < 25 {
		t.Fatalf("风险分过低: %+v", s.Risk)
	}
	sigFound := false
	for _, r := range s.Risk.Reasons {
		if strings.Contains(r, "签名") {
			sigFound = true
		}
	}
	if !sigFound {
		t.Fatalf("签名变更理由缺失: %+v", s.Risk.Reasons)
	}
}

// TestBuildNoWorkspace 无工作区降级：仅 L0 + 注明。
func TestBuildNoWorkspace(t *testing.T) {
	s := Build(context.Background(), Input{ChangedFiles: []string{"x/y.go"}})
	if len(s.Nodes) != 1 {
		t.Fatalf("nodes = %+v", s.Nodes)
	}
	if len(s.Notes) == 0 {
		t.Fatal("应有降级注明")
	}
}

// TestBuildEmpty 空 changed：空快照 + 注明。
func TestBuildEmpty(t *testing.T) {
	s := Build(context.Background(), Input{})
	if len(s.Nodes) != 0 || len(s.Notes) != 1 {
		t.Fatalf("nodes=%d notes=%v", len(s.Nodes), s.Notes)
	}
}

// TestCoupling 共变：3 次 commit 让 other 与 changed 同改 → 入榜；单次共现不入。
func TestCoupling(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "t@t")
	gitCmd(t, dir, "config", "user.name", "t")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {}\n")
	write("partner.go", "package main\n\nvar A = 1\n")
	write("once.go", "package main\n\nvar B = 1\n")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "init")
	for i := 0; i < 3; i++ {
		write("main.go", "package main\n\nfunc main() { _ = "+string(rune('a'+i))+" }\n")
		write("partner.go", "package main\n\nvar A = "+itoaN(i+2)+"\n")
		gitCmd(t, dir, "commit", "-am", "bump")
	}
	couples, commits, note := collectCoupling(context.Background(), dir, []string{"main.go"}, 10, 10*time.Second)
	if note != "" {
		t.Fatalf("note = %s", note)
	}
	if commits < 4 {
		t.Fatalf("commits = %d", commits)
	}
	foundPartner, foundOnce := false, false
	for _, c := range couples {
		if c.Other == "partner.go" && c.Count >= MinCoupleCount {
			foundPartner = true
		}
		if c.Other == "once.go" {
			foundOnce = true
		}
	}
	if !foundPartner {
		t.Fatalf("强共变 partner.go 未入榜: %+v", couples)
	}
	if foundOnce {
		t.Fatal("单次共现 once.go 不应入榜")
	}
}

func itoaN(n int) string { return string(rune('0' + n)) }

// TestRenderMermaid mermaid 形态：flowchart 头、changed 类、字符上限。
func TestRenderMermaid(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	src := RenderMermaid(s)
	if src == "" {
		t.Fatal("mermaid 为空")
	}
	if !strings.HasPrefix(src, "flowchart LR\n") {
		t.Fatalf("头不对: %s", src[:40])
	}
	if !strings.Contains(src, ":::changed") {
		t.Fatal("改动节点缺 changed 类")
	}
	if runeLen(src) > MaxMermaidRunes {
		t.Fatalf("超字符上限: %d", runeLen(src))
	}
	if strings.Count(src, "\n  n") > MaxMermaidNodes {
		t.Fatal("节点超上限")
	}
}

// TestRenderMermaidAggregation 巨量节点触发聚合仍不超限。
func TestRenderMermaidAggregation(t *testing.T) {
	// 构造 40 个受影响文件节点（远超 24 上限）
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a.go"}}
	for i := 0; i < 40; i++ {
		id := "file:pkg" + itoaN(i) + "/f.go"
		s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindFile, Label: id})
		s.Edges = append(s.Edges, Edge{From: "file:a.go", To: id, Rel: RelCaller})
	}
	src := RenderMermaid(s)
	if runeLen(src) > MaxMermaidRunes || strings.Count(src, "\n  n") > MaxMermaidNodes {
		t.Fatalf("聚合失败: len=%d", runeLen(src))
	}
}

// TestRenderList mermaid 降级列表形态。
func TestRenderList(t *testing.T) {
	dir := writeFixture(t)
	s := Build(context.Background(), Input{Dir: dir, ChangedFiles: []string{"pkga/a.go"}, MaxCommits: -1})
	l := RenderList(s)
	if !strings.Contains(l, "**改动**") || !strings.Contains(l, "pkga/a.go") {
		t.Fatalf("列表形态不对:\n%s", l)
	}
}

// TestRenderReportSection 报告段：details 折叠 + 风险徽标 + mermaid 或列表。
func TestRenderReportSection(t *testing.T) {
	dir := writeFixture(t)
	s := Build(context.Background(), Input{Dir: dir, ChangedFiles: []string{"pkga/a.go"}, MaxCommits: -1})
	sec := RenderReportSection(s)
	if !strings.Contains(sec, "<details>") || !strings.Contains(sec, "</details>") {
		t.Fatalf("缺 details 折叠:\n%s", sec)
	}
	if !strings.Contains(sec, "影响范围") {
		t.Fatal("缺标题")
	}
	if strings.Contains(sec, "```mermaid") == false && !strings.Contains(sec, "**改动**") {
		t.Fatal("mermaid 与列表应至少其一")
	}
}

// TestRenderPromptSection prompt 段：紧凑 + 纪律声明，单行可围栏。
func TestRenderPromptSection(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	p := RenderPromptSection(s)
	if !strings.Contains(p, "影响面推导") || !strings.Contains(p, "使用纪律") {
		t.Fatalf("prompt 段缺要素:\n%s", p)
	}
	if len(p) > 2000 {
		t.Fatalf("prompt 段过长: %d", len(p))
	}
}

// TestDeterminism 同输入两次构建 → 快照 JSON 逐字节一致（GeneratedAt 除外）。
func TestDeterminism(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	mk := func() *Snapshot {
		return Build(context.Background(), Input{
			Dir: dir, ChangedFiles: []string{"pkga/a.go"},
			HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
		})
	}
	a, b := mk(), mk()
	a.GeneratedAt, b.GeneratedAt = time.Time{}, time.Time{}
	a.Stats.TookMS, b.Stats.TookMS = 0, 0 // 计时天然抖动，不入确定性口径
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("快照不确定:\n%s\n%s", ab, bb)
	}
}

// TestMermaidEscape 标签转义：引号进 mermaid 源不破语法。
func TestMermaidEscape(t *testing.T) {
	got := mermaidEscape(`he said "hi" \ bye`)
	if strings.Count(got, `\"`) != 2 || strings.Count(got, `\\`) != 1 {
		t.Fatalf("escape = %s", got)
	}
}

func nodeIDs(s *Snapshot) []string {
	var out []string
	for _, n := range s.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// TestRenderHTML 交互视图：自包含 HTML（零外链）、内嵌 JSON、脚本闭合防线。
func TestRenderHTML(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	page := RenderHTML(s, "task-1")
	if page == "" || !strings.HasPrefix(page, "<!DOCTYPE html>") {
		t.Fatal("HTML 形态不对")
	}
	if !strings.Contains(page, `id="impact-data"`) || !strings.Contains(page, "pkga/a.go") {
		t.Fatal("缺内嵌数据或节点标签")
	}
	if strings.Contains(page, `</script zen`) { // 防闭合改写必须生效的占位断言
		t.Fatal("不应出现未转义闭合")
	}
	if strings.Contains(page, "argus_impactview_dummy") {
		t.Fatal("意外内容")
	}
	// taskID 进页头
	if !strings.Contains(page, "task-1") {
		t.Fatal("页头缺任务标识")
	}
	// 空 taskID 页头省略
	page2 := RenderHTML(s, "")
	if strings.Contains(page2, "任务 </span>") {
		t.Fatal("空任务标识应省略页头段")
	}
	// 空快照 → 空串
	if RenderHTML(&Snapshot{}, "t") != "" {
		t.Fatal("空快照应返回空串")
	}
}

// TestNoDanglingEdges 不变量：所有边的端点必须落在节点集内。回归锚——
// v3.183.0 曾因改动包挂边漏 file: 前缀产 3 条悬挂边，SVG/mermaid 双面静默丢边。
func TestNoDanglingEdges(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	ids := map[string]bool{}
	for _, n := range s.Nodes {
		ids[n.ID] = true
	}
	for _, e := range s.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Fatalf("悬挂边: %+v（节点集 %v）", e, nodeIDs(s))
		}
	}
	// module 根标签可读性
	if s := pkgLabel("."); s != "(module 根)" {
		t.Fatalf("pkgLabel(.) = %s", s)
	}
}

// TestSnapshotCapacityGuard 容量守卫（v3.184.1）：大 PR（200 改动文件+大闭包）
// 节点封顶 150、边随端点裁剪无悬挂、JSON ≤48KB、截断注明齐备。
func TestSnapshotCapacityGuard(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a/a.go"}}
	// 200 个改动文件节点（L0 场景）+ 每文件一条 caller 边
	for i := 0; i < 200; i++ {
		id := "file:pkg" + strconv.Itoa(i) + "/f.go"
		s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindFile, Label: id, Changed: true})
		symID := "sym:pkg" + strconv.Itoa(i) + "/f.go:F"
		s.Nodes = append(s.Nodes, Node{ID: symID, Kind: KindSymbol, Label: "F", Changed: true})
		s.Edges = append(s.Edges, Edge{From: id, To: symID, Rel: RelCaller})
	}
	s.Nodes = append(s.Nodes, Node{ID: "file:a/a.go", Kind: KindFile, Label: "a.go", Changed: true})
	finalize(s)

	if len(s.Nodes) > MaxSnapshotNodes {
		t.Fatalf("节点未封顶: %d", len(s.Nodes))
	}
	ids := map[string]bool{}
	for _, n := range s.Nodes {
		ids[n.ID] = true
	}
	for _, e := range s.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Fatalf("裁剪后残留悬挂边: %+v", e)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxSnapshotJSON {
		t.Fatalf("JSON 超体积守卫: %d", len(b))
	}
	if !s.Stats.Truncated {
		t.Fatal("应置 Truncated")
	}
	found := false
	for _, n := range s.Notes {
		if strings.Contains(n, "按影响权重截断") {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺截断注明: %+v", s.Notes)
	}
}
