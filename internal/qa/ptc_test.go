package qa

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/goagent-kernel/ptc"
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
`, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 16, MaxBytes: ptcMaxBytes, AllowUnmanaged: true})
	if !ok.OK || ok.Value == "" {
		t.Fatalf("预算内程序应成功：%+v", ok)
	}
	if calls != 5 {
		t.Fatalf("实际调用 = %d, want 5", calls)
	}
	over := ptc.Run(context.Background(), `
for (let i = 0; i < 99; i++) { await host.probe('{}'); }
return 1;
`, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 8, MaxBytes: ptcMaxBytes, AllowUnmanaged: true})
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
	}}, ptc.Options{Timeout: ptcTimeout, MaxCalls: 2, MaxBytes: 4096, AllowUnmanaged: true})
	if !res.OK || strings.Contains(res.Value, "function") {
		t.Fatalf("fetch 应不可用：OK=%v value=%q", res.OK, res.Value)
	}
}

func TestProgramFixable(t *testing.T) {
	cases := []struct {
		res  ptc.Result
		want bool
	}{
		{ptc.Result{OK: true, Value: `{"hits":1}`}, false},
		{ptc.Result{OK: true, Value: "null"}, true},    // 忘写 return
		{ptc.Result{ErrorKind: "syntax"}, true},        // 语法错可修
		{ptc.Result{ErrorKind: "runtime"}, true},       // JS 异常可修
		{ptc.Result{ErrorKind: "bad_result"}, true},    // return 不可序列化可修
		{ptc.Result{ErrorKind: "budget_calls"}, false}, // 预算类：方案太重不重试
		{ptc.Result{ErrorKind: "budget_bytes"}, false},
		{ptc.Result{ErrorKind: "deadline"}, false},
		{ptc.Result{ErrorKind: "stage_denied"}, false}, // argus 未接 Stage 环，异常态不重试
	}
	for _, c := range cases {
		if got := programFixable(c.res); got != c.want {
			t.Fatalf("programFixable(%+v) = %v, want %v", c.res, got, c.want)
		}
	}
}

func TestPtcFeedbackViaHarnessDescribe(t *testing.T) {
	// 诊断渲染已收编 harness（Result.Describe 单源，argus 不再手写渲染）。
	// 本侧钉的是回喂契约：喂给修复轮的文本必须是模型可行动的——点得出行列、
	// 给得出建议（渲染形态本身由 harness 侧用例钉）。
	// OK-but-null：诊断文本须点明缺 return（模型侧可行动）
	s := (ptc.Result{OK: true, Value: "null"}).Describe()
	if !strings.Contains(s, "return") {
		t.Fatalf("null 诊断应点明缺 return：%q", s)
	}
	// 带位置与建议的失败：位置行列 + 建议逐条可读
	s2 := (ptc.Result{
		ErrorKind: "syntax", Error: "Unexpected token",
		Location:    ptc.Position{Line: 3, Column: 7},
		Suggestions: []string{"检查第 3 行的括号配对"},
	}).Describe()
	if !strings.Contains(s2, "第 3 行第 7 列") || !strings.Contains(s2, "检查第 3 行的括号配对") {
		t.Fatalf("诊断面回喂不完整：%q", s2)
	}
}

// TestPtcRepairLoopEndToEnd 修复回喂端到端：坏程序（语法错）→ 诊断回喂 →
// 修正程序预算内成功。模拟 ptcAsk 内层循环的两段执行（不经 LLM——用真实
// ptc.Run 验证回喂产物确实是可执行程序）。
func TestPtcRepairLoopEndToEnd(t *testing.T) {
	calls := 0
	b := []ptc.Binding{{
		Name: "probe",
		Call: func(context.Context, string) (string, error) {
			calls++
			return `{"i":` + string(rune('0'+calls)) + `}`, nil
		},
	}}
	// 两类可修复形态都验：(a) 真语法错（未闭合大括号）；(b) 合法但缺 return
	//（ptc 契约 OK/Value="null"——忘写 return 的静默 bug，programFixable 特例）
	broken := "const r = JSON.parse(await host.probe('{}'));\nconst leak = {" // 语法错：未闭合
	first := ptc.Run(context.Background(), broken, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 4, MaxBytes: 4096, AllowUnmanaged: true})
	if first.OK || first.ErrorKind != "syntax" {
		t.Fatalf("坏程序应 syntax 拒：%+v", first)
	}
	if !programFixable(first) {
		t.Fatalf("语法错应可修复：%+v", first)
	}
	noReturn := "const r = JSON.parse(await host.probe('{}'));" // 合法但无 return
	nr := ptc.Run(context.Background(), noReturn, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 4, MaxBytes: 4096, AllowUnmanaged: true})
	if !nr.OK || nr.Value != "null" || !programFixable(nr) {
		t.Fatalf("缺 return 应 OK/null 且可修复：OK=%v value=%q fixable=%v", nr.OK, nr.Value, programFixable(nr))
	}
	// 回喂修复产物（模型侧理想输出形态）
	fixed := "const r = JSON.parse(await host.probe('{}'));\nreturn {got: r.i};"
	second := ptc.Run(context.Background(), fixed, b, ptc.Options{Timeout: ptcTimeout, MaxCalls: 4, MaxBytes: 4096, AllowUnmanaged: true})
	if !second.OK || second.Value == "null" {
		t.Fatalf("修复后应成功：%+v", second)
	}
}
