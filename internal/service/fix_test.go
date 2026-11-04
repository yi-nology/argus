package service

import (
	"argus/internal/commentcmd"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/tools/acpx"
)

// initGitRepo 构造带初始提交的本地 git 仓（修复沙箱克隆源）。
func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

// fakeFixAgent 可编程 acpx agent：fn 在 agent 工作目录里落地修复动作。
type fakeFixAgent struct {
	sandbox bool
	fn      func(workDir string)
}

func (f *fakeFixAgent) Name() string                  { return "fake" }
func (f *fakeFixAgent) Capabilities() acpx.Capability { return acpx.Capability{Sandbox: f.sandbox} }
func (f *fakeFixAgent) Run(_ context.Context, req acpx.RunRequest) (*acpx.RunResult, error) {
	f.fn(req.WorkDir)
	return &acpx.RunResult{Text: "```json\n{\"fixed\": true, \"summary\": \"已修复越界检查\", \"files\": [\"a.go\"]}\n```"}, nil
}

func TestExtractFix(t *testing.T) {
	ok := map[string]string{
		"fix fp:ab12cd34":       "ab12cd34",
		"fix fp:AB12CD34":       "ab12cd34",
		"fix   fp:ab12cd34  ":   "ab12cd34",
		"fix fp:ab12cd34 extra": "ab12cd34",
	}
	for q, want := range ok {
		if fp, got := commentcmd.ExtractFix(q); !got || fp != want {
			t.Fatalf("commentcmd.ExtractFix(%q) = %q,%v; want %q", q, fp, got, want)
		}
	}
	bad := []string{
		"fix", "fix please", "fixer fp:ab12cd34", "fixfp:ab12cd34",
		"fix fp:ab1", "fix fp:zzzzzz", "fix fp:", "refix fp:ab12cd34",
	}
	for _, q := range bad {
		if _, got := commentcmd.ExtractFix(q); got {
			t.Fatalf("commentcmd.ExtractFix(%q) 不应命中", q)
		}
	}
}

func TestRunFixSandboxHappy(t *testing.T) {
	src := initGitRepo(t, map[string]string{
		"a.go": "package a\n\nfunc Get(p string) []byte { return load(p) }\n",
	})
	reg := acpx.NewRegistry()
	reg.Register(&fakeFixAgent{sandbox: true, fn: func(workDir string) {
		p := filepath.Join(workDir, "a.go")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("agent 工作目录缺 a.go: %v", err)
			return
		}
		fixed := strings.Replace(string(b), "load(p)", "load(p, 4096)", 1)
		_ = os.WriteFile(p, []byte(fixed), 0o644)
		_ = os.WriteFile(filepath.Join(workDir, "verify.marker"), []byte("ok"), 0o644)
	}})

	res, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir:    src,
		AgentName: "fake",
		Registry:  reg,
		Verify:    VerifyConfig{Cmd: "test -f {dir}/verify.marker", Timeout: 10 * time.Second},
		Finding:   fixFinding{File: "a.go", Line: 3, Severity: "high", Comment: "未限制 page 上限"},
		Timeout:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fixed || res.Summary != "已修复越界检查" || len(res.Files) != 1 {
		t.Fatalf("envelope 解析不符: %+v", res)
	}
	if !res.Verified {
		t.Fatalf("验证应通过: %s", res.VerifyOut)
	}
	if !strings.Contains(res.Diff, "load(p, 4096)") {
		t.Fatalf("diff 缺修复内容:\n%s", res.Diff)
	}
	if res.DiffStat == "" {
		t.Fatal("diff --stat 不应为空")
	}
}

func TestRunFixSandboxVerifyFail(t *testing.T) {
	src := initGitRepo(t, map[string]string{"a.go": "package a\n"})
	reg := acpx.NewRegistry()
	reg.Register(&fakeFixAgent{sandbox: true, fn: func(workDir string) {
		_ = os.WriteFile(filepath.Join(workDir, "a.go"), []byte("package a\n// changed\n"), 0o644)
	}})
	res, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir:    src,
		AgentName: "fake",
		Registry:  reg,
		Verify:    VerifyConfig{Cmd: "false", Timeout: 10 * time.Second},
		Finding:   fixFinding{File: "a.go", Line: 1, Comment: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified {
		t.Fatal("验证应失败")
	}
	if res.VerifyOut == "" {
		t.Fatal("失败验证应留输出摘录")
	}
}

func TestRunFixSandboxGuards(t *testing.T) {
	src := initGitRepo(t, map[string]string{"a.go": "package a\n"})

	// 无沙箱能力的 agent：fail-closed 拒绝
	regNoSb := acpx.NewRegistry()
	regNoSb.Register(&fakeFixAgent{sandbox: false})
	if _, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir: src, AgentName: "fake", Registry: regNoSb,
		Finding: fixFinding{File: "a.go", Line: 1},
	}); err == nil || !strings.Contains(err.Error(), "不支持沙箱") {
		t.Fatalf("无沙箱能力应拒绝: %v", err)
	}

	// 未知 agent（NewRegistry 空注册表仍含缺省 11 家，用确定不存在的名字与空注册
	// 表语义等价——直接以 Get 未命中路径覆盖）
	if _, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir: src, AgentName: "nope", Registry: acpx.NewRegistry(),
		Finding: fixFinding{File: "a.go", Line: 1},
	}); err == nil || !strings.Contains(err.Error(), "未知 agent") {
		t.Fatalf("未知 agent 应报错: %v", err)
	}

	// 克隆源缺失
	reg := acpx.NewRegistry()
	reg.Register(&fakeFixAgent{sandbox: true})
	if _, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir: "", AgentName: "fake", Registry: reg,
		Finding: fixFinding{File: "a.go", Line: 1},
	}); err == nil || !strings.Contains(err.Error(), "工作副本") {
		t.Fatalf("源缺失应报错: %v", err)
	}

	// 路径穿越护栏
	if _, err := runFixSandbox(context.Background(), fixRunConfig{
		SrcDir: src, AgentName: "fake", Registry: reg,
		Finding: fixFinding{File: "../escape", Line: 1},
	}); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("目录穿越应拒绝: %v", err)
	}
}

func TestParseFixEnvelope(t *testing.T) {
	env := parseFixEnvelope("说明文字\n```json\n{\"fixed\": true, \"summary\": \"s\", \"files\": [\"a.go\"]}\n```")
	if !env.Fixed || env.Summary != "s" {
		t.Fatalf("围栏 JSON 解析失败: %+v", env)
	}
	env = parseFixEnvelope("完全不是 JSON 的输出")
	if env.Fixed || env.Summary == "" {
		t.Fatalf("垃圾输出应回退 summary: %+v", env)
	}
}

func TestRenderFixReply(t *testing.T) {
	res := &fixResult{
		AgentName: "fake", Took: 3 * time.Second, Fixed: true,
		Summary: "修好", Files: []string{"a.go"},
		DiffStat: " a.go | 1 +", Diff: "+fixed line",
		Verified: true,
	}
	out := renderFixReply("ab12cd34ff", fixFinding{File: "a.go", Line: 3, Severity: "high", Comment: "x"}, res, "d1")
	for _, want := range []string{"修复结果", "fp:ab12cd34", "✅ 通过", "```diff", "+fixed line", "未推送任何分支", "argus:fix delivery=d1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("回帖缺 %q:\n%s", want, out)
		}
	}
}

func TestSafeRelPath(t *testing.T) {
	for _, p := range []string{"a.go", "internal/x/y.go"} {
		if err := safeRelPath(p); err != nil {
			t.Fatalf("合法路径 %s 不应报错: %v", p, err)
		}
	}
	for _, p := range []string{"", "/abs/path", "../escape", "a/../../escape"} {
		if err := safeRelPath(p); err == nil {
			t.Fatalf("非法路径 %s 应报错", p)
		}
	}
}
