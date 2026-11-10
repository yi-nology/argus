// funcdiff_test.go —— 函数/方法级图 diff 单测：git 双提交 fixture（base→head）
// 覆盖签名变更/调用边增减/函数增删/触达过滤/base 不可达降级。
package impactview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// funcDiffFixture 两提交仓：base 有 Func(x int)/Old()/helper；head 改签名、
// 换调用（Old→helper.Wrap）、删 Old、增 New。
func funcDiffFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, body string) {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	write("go.mod", "module example.com/fd\n\ngo 1.22\n")
	write("a.go", `package fd

// Func base 版（单参数，调 Old）。
func Func(x int) int {
	return Old(x)
}

// Old base 版遗留。
func Old(x int) int { return x }

// Keep base 版不动。
func Keep() {}
`)
	write("helper.go", `package fd

// Wrap 头版新增（head 侧调用目标）。
func Wrap(x int) int { return x * 2 }
`)
	git("add", ".")
	git("commit", "-m", "base")
	// head：改签名+换调用、删 Old、增 New
	write("a.go", `package fd

// Func head 版（双参数，改调 Wrap）。
func Func(x, y int) int {
	return Wrap(x) + y
}

// New head 版新增。
func New() int { return Wrap(1) }

// Keep base 版不动。
func Keep() {}
`)
	git("commit", "-am", "head")
	return dir
}

// TestDiffFunctionsCore 核心语义：modified（签名+调用边增减）/added/removed/
// 未触达不动函数不报。
func TestDiffFunctionsCore(t *testing.T) {
	dir := funcDiffFixture(t)
	ranges := map[string][][2]int{"a.go": {{1, 14}}}
	fds, notes := diffFunctions(context.Background(), dir, "HEAD~1", []string{"a.go"}, ranges, false)
	if len(notes) > 0 {
		t.Fatalf("意外 notes: %v", notes)
	}
	byStatus := map[string][]FuncDiff{}
	for _, fd := range fds {
		byStatus[fd.Status] = append(byStatus[fd.Status], fd)
	}
	if len(byStatus[FuncModified]) != 1 || byStatus[FuncModified][0].Name != "Func" {
		t.Fatalf("modified 应恰为 Func: %+v", fds)
	}
	fd := byStatus[FuncModified][0]
	if fd.SigSame {
		t.Fatal("Func 签名应判定为变更")
	}
	if len(fd.CallsAdded) != 1 || fd.CallsAdded[0] != "Wrap" {
		t.Fatalf("新增调用 = %v", fd.CallsAdded)
	}
	if len(fd.CallsRemoved) != 1 || fd.CallsRemoved[0] != "Old" {
		t.Fatalf("断开调用 = %v", fd.CallsRemoved)
	}
	added := map[string]bool{}
	for _, f := range byStatus[FuncAdded] {
		added[f.Name] = true
	}
	if !added["New"] {
		t.Fatalf("New 应为 added: %+v", fds)
	}
	removed := map[string]bool{}
	for _, f := range byStatus[FuncRemoved] {
		removed[f.Name] = true
	}
	if !removed["Old"] || removed["Keep"] {
		t.Fatalf("removed 集不对: %v", removed)
	}
}

// TestDiffFunctionsUntrackedNotReported hunk 区间未覆盖的函数不报（防噪声）。
func TestDiffFunctionsUntrackedNotReported(t *testing.T) {
	dir := funcDiffFixture(t)
	// 区间只覆盖 Func（a.go 头部）
	fds, _ := diffFunctions(context.Background(), dir, "HEAD~1",
		[]string{"a.go"}, map[string][][2]int{"a.go": {{4, 8}}}, false)
	for _, fd := range fds {
		if fd.Name == "New" && fd.Status == FuncAdded {
			// New 在 head 的 14-16 行附近，区间 {4,8} 未触达 → 不应报
			t.Fatalf("未触达函数泄漏: %+v", fds)
		}
	}
}

// TestDiffFunctionsNoBaseRef base 参照缺失/不可达：整层跳过并注明。
func TestDiffFunctionsNoBaseRef(t *testing.T) {
	dir := funcDiffFixture(t)
	fds, notes := diffFunctions(context.Background(), dir, "", []string{"a.go"}, nil, false)
	if fds != nil || len(notes) != 1 {
		t.Fatalf("空 ref 应跳过: %v %v", fds, notes)
	}
	fds, notes = diffFunctions(context.Background(), dir, "origin/no-such-branch", []string{"a.go"}, nil, false)
	if fds != nil || len(notes) != 1 || !strings.Contains(notes[0], "不可达") {
		t.Fatalf("不可达 ref 应跳过并注明: %v %v", fds, notes)
	}
}

// TestBuildWithFuncDiff Build 端到端：BaseRef 接线后快照带 func_diffs。
func TestBuildWithFuncDiff(t *testing.T) {
	dir := funcDiffFixture(t)
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"a.go"},
		HunkRanges:   map[string][][2]int{"a.go": {{1, 20}}},
		BaseRef:      "HEAD~1",
		MaxCommits:   -1,
	})
	if len(s.FuncDiffs) == 0 {
		t.Fatalf("快照缺 func_diffs: %+v", s.Stats)
	}
	if s.Stats.FuncDiffs != len(s.FuncDiffs) {
		t.Fatal("Stats.FuncDiffs 口径失真")
	}
	// 渲染三态都吃到
	if !strings.Contains(RenderReportSection(s), "函数级变更") {
		t.Fatal("报告段缺函数级区块")
	}
	if !strings.Contains(RenderPromptSection(s), "函数级变更") {
		t.Fatal("prompt 段缺函数级行")
	}
	if page := RenderHTMLFull(s, "t", ViewOpts{}); !strings.Contains(page, "函数级图 diff") {
		t.Fatal("HTML 缺函数级卡片区块")
	}
}

// TestFuncDiffMermaid 图 diff mermaid：状态类与 ± 边形态。
func TestFuncDiffMermaid(t *testing.T) {
	s := &Snapshot{FuncDiffs: []FuncDiff{
		{Name: "Func", Status: FuncModified, CallsAdded: []string{"Wrap"}, CallsRemoved: []string{"Old"}},
		{Name: "New", Status: FuncAdded},
	}}
	src := RenderFuncDiffMermaid(s)
	if !strings.Contains(src, "fdmod") || !strings.Contains(src, "fdadd") {
		t.Fatalf("缺状态类: %s", src)
	}
	if !strings.Contains(src, "-->|+|") || !strings.Contains(src, "-.->|−|") {
		t.Fatalf("缺 ± 边形态: %s", src)
	}
	if RenderFuncDiffMermaid(&Snapshot{}) != "" {
		t.Fatal("空快照应返回空")
	}
}
