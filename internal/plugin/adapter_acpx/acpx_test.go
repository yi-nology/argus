package adapter_acpx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.example.com/enjoydream/agentkit/acpx"
	"git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"
)

// stubAgent 假 acpx agent：返回固定文本（合法 findings JSON）。
type stubAgent struct {
	name string
	text string
	err  error
	got  acpx.RunRequest
}

func (s *stubAgent) Name() string { return s.name }
func (s *stubAgent) Run(_ context.Context, req acpx.RunRequest) (*acpx.RunResult, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return &acpx.RunResult{Text: s.text}, nil
}

func newTestPlugin(agent acpx.Agent) *Plugin {
	return &Plugin{
		manifest: plugin.AgentManifest{Name: "test-acpx"},
		agent:    agent,
		log:      logx.NewSlogLogger("test"),
	}
}

func reviewTask() plugin.Task {
	return plugin.Task{
		ID:   "t1",
		Kind: "code_review",
		PR:   plugin.PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"},
		Diff: &plugin.DiffContext{Text: "diff --git a/main.go b/main.go\n+func f() {}"},
	}
}

func TestInvokeParseFindings(t *testing.T) {
	sa := &stubAgent{
		name: "stub",
		text: "审查完成。```json\n" +
			`[{"file":"main.go","line":10,"severity":"CRITICAL","comment":"空指针解引用"},` +
			`{"file":"util.go","line":0,"severity":"low","comment":"未检查错误"}]` +
			"\n```",
	}
	p := newTestPlugin(sa)

	res, err := p.Invoke(context.Background(), reviewTask())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("应解析出 2 条 finding，得到 %d", len(res.Findings))
	}
	// severity 归一化：CRITICAL → high
	if res.Findings[0].Severity != severity.High {
		t.Fatalf("severity 应归一为 high，得到 %q", res.Findings[0].Severity)
	}
	// By 标注插件名
	if res.Findings[0].By != "test-acpx" {
		t.Fatalf("By = %q", res.Findings[0].By)
	}
	// prompt 含 diff 数据区与输出契约
	if !strings.Contains(sa.got.Prompt, "diff --git") || !strings.Contains(sa.got.Prompt, "JSON") {
		t.Fatal("prompt 应含 diff 与输出契约")
	}
	// 只读沙箱
	if sa.got.Sandbox != acpx.SandboxReadonly {
		t.Fatalf("Sandbox = %q", sa.got.Sandbox)
	}
}

func TestInvokeEmptyFindings(t *testing.T) {
	sa := &stubAgent{name: "stub", text: "没有问题 []"}
	p := newTestPlugin(sa)

	res, err := p.Invoke(context.Background(), reviewTask())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("无问题应零 findings，得到 %d", len(res.Findings))
	}
}

func TestInvokeAgentError(t *testing.T) {
	p := newTestPlugin(&stubAgent{name: "stub", err: errors.New("boom")})
	if _, err := p.Invoke(context.Background(), reviewTask()); err == nil {
		t.Fatal("agent 失败应上抛")
	}
}

func TestInvokeMalformedJSON(t *testing.T) {
	// agent 输出完全不可解析 ≠ 零 findings：按失败上抛（dispatcher 记 breaker
	// 失败 + Outcome 降级注明）；合法空数组 = 真零 findings 由 TestInvokeEmptyFindings 覆盖
	p := newTestPlugin(&stubAgent{name: "stub", text: "我觉得代码写得很好"})
	if _, err := p.Invoke(context.Background(), reviewTask()); err == nil {
		t.Fatal("输出不可解析应上抛，不得假装零 findings")
	}
}

func TestNewUnknownAgent(t *testing.T) {
	if _, err := New(plugin.AgentManifest{Name: "x"}, "no-such-agent", nil,
		logx.NewSlogLogger("test")); err == nil {
		t.Fatal("未注册 agent 应报错")
	}
}

func TestNewBinaryMissing(t *testing.T) {
	// 隔离 PATH 为空目录：保证任何 agent 的 CLI 都不存在（不依赖本机安装状态）
	t.Setenv("PATH", t.TempDir())
	_, err := New(plugin.AgentManifest{Name: "x"}, "mimo", nil, logx.NewSlogLogger("test"))
	if !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("应返回 ErrBinaryMissing，得到 %v", err)
	}
}

func TestWorkcopyLifecycle(t *testing.T) {
	// 无 wc（nil）：prompt 不含工作目录提示，不 panic
	p := newTestPlugin(&stubAgent{name: "stub", text: "[]"})
	if _, err := p.Invoke(context.Background(), reviewTask()); err != nil {
		t.Fatal(err)
	}

	// 有 HeadSHA 但 wc 为 nil → 降级纯 diff 审查
	task := reviewTask()
	task.PR.HeadSHA = "abc123"
	if _, err := p.Invoke(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestPromptWithoutDiff(t *testing.T) {
	task := reviewTask()
	task.Diff = nil
	got := buildPrompt(task, false)
	if !strings.Contains(got, "diff 为空") {
		t.Fatal("无 diff 时 prompt 应有占位说明")
	}
}
