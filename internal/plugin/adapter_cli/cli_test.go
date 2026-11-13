package adapter_cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/runtime/procx"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// 干净 PR 的合法"零意见"输出（[] 或 {"comments":[]}）不再是错误——
// 此前连续 3 个干净 PR 会把 cli 插件熔断摘除 5 分钟。
func TestParseFindingsEmptyIsLegit(t *testing.T) {
	for _, in := range []string{"[]", `{"comments":[]}`, `{"data":{"comments":[]}}`} {
		findings, err := ParseFindings(in)
		if err != nil {
			t.Fatalf("%s 应为合法零结果，got err=%v", in, err)
		}
		if len(findings) != 0 {
			t.Fatalf("%s 应解析出 0 条，got %d", in, len(findings))
		}
	}
	// 结构不符/空输出仍然是错误
	if _, err := ParseFindings(""); err == nil {
		t.Fatal("空输出应报错")
	}
	if _, err := ParseFindings("not json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// plugins.yaml env 声明经 RunProcess 白名单透传的语义由 oagentkit/tools/acpx 上游测试
// 锁定（TestEnvAllowlist/TestRunProcess）；此处端到端钉一遍声明确实传到了底座。
func TestInvokeEnvWhitelistEndToEnd(t *testing.T) {
	t.Setenv("ARGUS_GITEA_TOKEN", "secret-token")
	t.Setenv("OPENAI_API_KEY", "sk-secret")

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ocr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$OPENAI_API_KEY\"; echo \"$ARGUS_GITEA_TOKEN\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, _, err := procx.Run(context.Background(), procx.RunRequest{
		Argv:    []string{script},
		Env:     []string{"OPENAI_API_KEY"}, // plugins.yaml env 声明形态
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "sk-secret") {
		t.Fatalf("声明白名单的变量应透传: %q", stdout)
	}
	if strings.Contains(stdout, "secret-token") {
		t.Fatalf("未声明的密钥变量不得继承: %q", stdout)
	}
}

// ---------- Invoke 执行链（file:// 本地 fixture，零网络） ----------

// makeFixtureRepo 本地 git fixture（照搬 workcopy 上游测试形态）：main 提交一次 +
// refs/pull/1/head 供 fetch。file:// 克隆零网络。
func makeFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-b", "main", dir)
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o644)
	gitRun("add", ".")
	gitRun("commit", "-m", "init")
	gitRun("update-ref", "refs/pull/1/head", "main")
	return dir
}

func newTestPool(t *testing.T, fixture string) *workcopy.Pool {
	t.Helper()
	p := workcopy.NewPool(t.TempDir(), logx.Nop())
	p.CredentialOf = func(string) (string, string, bool) { return "file://" + fixture, "x", true }
	return p
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-ocr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func cliTask() plugin.Task {
	return plugin.Task{PR: plugin.PRContext{
		Platform: "gitea", Owner: "o", Repo: "r", Number: "1",
		HeadSHA: "HEAD", DefaultBranch: "main",
	}}
}

func TestNewManifestTimeoutDefault(t *testing.T) {
	p := New(plugin.AgentManifest{Name: "ocr"}, nil, "full", nil, nil, nil)
	if p.Manifest().Timeout != 300*time.Second {
		t.Fatalf("缺省 Timeout = %v, want 300s", p.Manifest().Timeout)
	}
	if p.Manifest().Name != "ocr" {
		t.Fatalf("Name = %q", p.Manifest().Name)
	}
	custom := New(plugin.AgentManifest{Name: "ocr", Timeout: 5 * time.Second}, nil, "full", nil, nil, nil)
	if custom.Manifest().Timeout != 5*time.Second {
		t.Fatalf("显式 Timeout 不应被覆盖: %v", custom.Manifest().Timeout)
	}
}

func TestInvokeDelegateModeRejected(t *testing.T) {
	p := New(plugin.AgentManifest{Name: "ocr"}, []string{"true"}, "delegate", nil, nil, nil)
	if _, err := p.Invoke(context.Background(), cliTask()); err == nil ||
		!strings.Contains(err.Error(), "delegate") {
		t.Fatalf("delegate 模式应明确拒绝: %v", err)
	}
}

func TestInvokeWithoutPool(t *testing.T) {
	p := New(plugin.AgentManifest{Name: "ocr"}, []string{"true"}, "full", nil, nil, nil)
	if _, err := p.Invoke(context.Background(), cliTask()); err == nil ||
		!strings.Contains(err.Error(), "工作副本") {
		t.Fatalf("无池应报错: %v", err)
	}
}

// 端到端主路径：Ensure(file:// 克隆) → exec 假脚本 → ParseFindings → By 截断归一。
func TestInvoke_EndToEnd(t *testing.T) {
	script := writeScript(t, `echo '[{"path":"pkg/a.go","line":"12","level":"high","comment":"空指针解引用","suggested_fix":"if x == nil { return }"}]'`)
	p := New(plugin.AgentManifest{Name: "ocr", Timeout: 30 * time.Second},
		[]string{script}, "full", nil, newTestPool(t, makeFixtureRepo(t)), logx.Nop())

	res, err := p.Invoke(context.Background(), cliTask())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.File != "pkg/a.go" || f.Line != 12 || f.Severity != plugin.SeverityHigh {
		t.Fatalf("Finding 归一失真: %+v", f)
	}
	if f.By != "ocr" {
		t.Fatalf("By = %q, want ocr", f.By)
	}
	if f.SuggestedFix != "if x == nil { return }" {
		t.Fatalf("SuggestedFix = %q", f.SuggestedFix)
	}
}

// 超长 comment 截断留痕（findingCommentMaxRunes）。
func TestInvoke_TruncatesLongComment(t *testing.T) {
	script := writeScript(t, `python3 -c "import json;print(json.dumps([{'path':'a.go','line':1,'level':'low','comment':'x'*5000}]))"`)
	p := New(plugin.AgentManifest{Name: "ocr", Timeout: 30 * time.Second},
		[]string{script}, "full", nil, newTestPool(t, makeFixtureRepo(t)), logx.Nop())

	res, err := p.Invoke(context.Background(), cliTask())
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	c := res.Findings[0].Comment
	if len([]rune(c)) > 2100 {
		t.Fatalf("comment 未截断: %d runes", len([]rune(c)))
	}
	if !strings.Contains(c, "截断") {
		t.Fatalf("截断应留痕尾注: %q", c[len(c)-20:])
	}
}

// 命令失败/超时/非法输出三种失败面：错误信息带归属，不静默吞。
func TestInvoke_FailureSurfaces(t *testing.T) {
	fixture := makeFixtureRepo(t)

	fail := New(plugin.AgentManifest{Name: "ocr", Timeout: 10 * time.Second},
		[]string{writeScript(t, `echo boom >&2; exit 3`)}, "full", nil, newTestPool(t, fixture), logx.Nop())
	if _, err := fail.Invoke(context.Background(), cliTask()); err == nil ||
		!strings.Contains(err.Error(), "cli 执行失败") {
		t.Fatalf("非零退出应报执行失败: %v", err)
	}

	slow := New(plugin.AgentManifest{Name: "ocr", Timeout: 200 * time.Millisecond},
		[]string{writeScript(t, `sleep 5`)}, "full", nil, newTestPool(t, fixture), logx.Nop())
	if _, err := slow.Invoke(context.Background(), cliTask()); err == nil ||
		!strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时应报执行超时: %v", err)
	}

	bad := New(plugin.AgentManifest{Name: "ocr", Timeout: 10 * time.Second},
		[]string{writeScript(t, `echo 'not json'`)}, "full", nil, newTestPool(t, fixture), logx.Nop())
	if _, err := bad.Invoke(context.Background(), cliTask()); err == nil ||
		!strings.Contains(err.Error(), "解析") {
		t.Fatalf("非法输出应报解析失败: %v", err)
	}
}

// ---------- ParseFindings 字段变体与包装形态 ----------

func TestParseFindingsFieldVariants(t *testing.T) {
	in := `{"comments":[
		{"file":"a.go","Line":"7","severity":"WARNING","message":"msg 变体","fix":"fix 变体","evidence":"a.go:7 调用前未校验"},
		{"Path":"b.go","line_number":8.0,"level":"error","body":"body 变体","SuggestedFix":"大写变体"},
		{"Filename":"c.go","Line":"9","priority":"banana","description":"desc 变体"},
		{"no_file":"d.go","comment":"缺 file 应剔除"},
		{"file":"e.go","no_comment":"缺 comment 应剔除"},
		{"file":"f.go","comment":"非对象条目"}
	]}`
	// 最后一条塞非对象（数组元素不是 map）走 continue 分支
	in = strings.Replace(in, `{"file":"f.go","comment":"非对象条目"}`, `"plain-string"`, 1)
	fs, err := ParseFindings(in)
	if err != nil {
		t.Fatalf("ParseFindings: %v", err)
	}
	if len(fs) != 3 {
		t.Fatalf("应解析 3 条（缺 file/缺 comment/非对象剔除）: %+v", fs)
	}
	if fs[0].File != "a.go" || fs[0].Line != 7 || fs[0].Comment != "msg 变体" || fs[0].SuggestedFix != "fix 变体" {
		t.Fatalf("变体1 归一失真: %+v", fs[0])
	}
	// v6.18.0 evidence 变体：外部 CLI JSON 的证据字段透传（缺省空）
	if fs[0].Evidence != "a.go:7 调用前未校验" {
		t.Fatalf("evidence 变体未透传: %+v", fs[0])
	}
	if fs[1].File != "b.go" || fs[1].Line != 8 || fs[1].Comment != "body 变体" || fs[1].SuggestedFix != "大写变体" {
		t.Fatalf("变体2 归一失真: %+v", fs[1])
	}
	// 越界 severity 归 low
	if fs[2].Severity != plugin.SeverityLow {
		t.Fatalf("banana 越界应归 low: %q", fs[2].Severity)
	}
}

func TestParseFindingsWrapperShapesAndEmptyArrayError(t *testing.T) {
	for _, in := range []string{
		`[{"path":"a.go","line":1,"level":"low","comment":"c"}]`,
		`{"comments":[{"path":"a.go","line":1,"level":"low","comment":"c"}]}`,
		`{"findings":[{"path":"a.go","line":1,"level":"low","comment":"c"}]}`,
		`{"data":{"comments":[{"path":"a.go","line":1,"level":"low","comment":"c"}]}}`,
		`{"result":{"items":[{"path":"a.go","line":1,"level":"low","comment":"c"}]}}`,
	} {
		fs, err := ParseFindings(in)
		if err != nil || len(fs) != 1 {
			t.Fatalf("%s 应解析 1 条: fs=%+v err=%v", in, fs, err)
		}
	}
	// 有条目但无一可解析 → 错误（不是合法零意见）
	if _, err := ParseFindings(`[{"no_file":1},{"no_file":2}]`); err == nil {
		t.Fatal("无可解析条目应报错")
	}
	if _, err := ParseFindings(`{"unknown_key":[]}`); err == nil {
		t.Fatal("无数组键应报错")
	}
}
