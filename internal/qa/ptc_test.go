package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/agent-harness/ptc"
)

// fakeInvokableTool 最小 eino InvokableTool（ptc 绑定适配测试用）。
type fakeInvokableTool struct {
	name string
	desc string
}

func (f *fakeInvokableTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name, Desc: f.desc}, nil
}

func (f *fakeInvokableTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	return `{"echo":` + argsJSON + `}`, nil
}

func TestExtractProgram(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"```js\nreturn 1;\n```", "return 1;"},
		{"前言\n```javascript\nconst a = host.get_file('{}');\nreturn a;\n```\n后记", "const a = host.get_file('{}');\nreturn a;"},
		{"裸程序 const x = await host.search('{}'); return x;", "裸程序 const x = await host.search('{}'); return x;"},
		{"没有任何程序形态的回答", ""},
		{"```\nreturn 2;\n```", "return 2;"},
	}
	for _, c := range cases {
		if got := extractProgram(c.in); got != c.want {
			t.Fatalf("extractProgram(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPtcBindingsFromTools(t *testing.T) {
	tools := []tool.BaseTool{
		&fakeInvokableTool{name: "get_file", desc: "读文件"},
		&fakeInvokableTool{name: "search-code", desc: "检索"}, // 净化面：- → _
	}
	b := ptcBindingsFromTools(tools)
	if len(b) != 2 {
		t.Fatalf("绑定数 = %d, want 2", len(b))
	}
	if b[0].Name != "get_file" || b[1].Name != "search_code" {
		t.Fatalf("绑定名净化错位：%q %q", b[0].Name, b[1].Name)
	}
	// 绑定可直调（argsJSON 直达）
	out, err := b[0].Call(context.Background(), `{"path":"a.go"}`)
	if err != nil || !strings.Contains(out, "a.go") {
		t.Fatalf("绑定调用失败：out=%q err=%v", out, err)
	}
}

// TestPtcRunBudgetEnforced 端到端沙箱冒烟：程序内循环调绑定（含归并），
// 预算内成功；超调用预算的程序被截停（ErrorKind=budget_calls）——回退语义的依据。
func TestPtcRunBudgetEnforced(t *testing.T) {
	calls := 0
	b := []ptc.Binding{{
		Name: "probe",
		Call: func(context.Context, string) (string, error) {
			calls++
			return `{"ok":true,"i":` + string(rune('0'+calls%10)) + `}`, nil
		},
	}}
	ok := ptc.Run(context.Background(), `
const hits = [];
for (let i = 0; i < 5; i++) {
  const r = JSON.parse(await host.probe('{}'));
  if (r.ok) hits.push(r.i);
}
return {hits, n: hits.length};
`, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 16, MaxBytes: ptcMaxBytes})
	if !ok.OK || ok.Value == "" {
		t.Fatalf("预算内程序应成功：%+v", ok)
	}
	if calls != 5 {
		t.Fatalf("实际调用 = %d, want 5", calls)
	}
	over := ptc.Run(context.Background(), `
for (let i = 0; i < 99; i++) { await host.probe('{}'); }
return 1;
`, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 8, MaxBytes: ptcMaxBytes})
	if over.OK || over.ErrorKind != "budget_calls" {
		t.Fatalf("超预算应 budget_calls 截停：OK=%v kind=%q", over.OK, over.ErrorKind)
	}
}

// TestPtcRunSandboxSubset 沙箱子集纪律：fetch/require 不存在——程序面没有
// 绕开绑定直达网络/文件系统的路。
func TestPtcRunSandboxSubset(t *testing.T) {
	res := ptc.Run(context.Background(), `return typeof fetch;`, []ptc.Binding{{
		Name: "noop",
		Call: func(context.Context, string) (string, error) { return "{}", nil },
	}}, ptc.Options{Timeout: ptcTimeout, MaxCalls: 2, MaxBytes: 4096})
	if !res.OK || strings.Contains(res.Value, "function") {
		t.Fatalf("fetch 应不可用：OK=%v value=%q", res.OK, res.Value)
	}
}
