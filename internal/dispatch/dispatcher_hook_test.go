package dispatch

import (
	"context"
	"sync"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// hookReg 构造 hook 测试用选中项（name + 插件实现）。
func hookReg(name string, p plugin.AgentPlugin) *Registration {
	return &Registration{
		Manifest: plugin.AgentManifest{Name: name},
		Plugin:   p,
		Enabled:  true,
	}
}

// TestRunWithHook_SuccessFires 成功产物落定触发 hook；失败/跳过不触发。
func TestRunWithHook_SuccessFires(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	okReg := hookReg("sec", &nopPlugin{manifest: plugin.AgentManifest{Name: "sec"}})
	badReg := hookReg("bad", &failPlugin{manifest: plugin.AgentManifest{Name: "bad"}})

	var mu sync.Mutex
	fired := map[string]int{}
	hook := func(o Outcome) {
		mu.Lock()
		defer mu.Unlock()
		fired[o.Name]++
	}
	sel := []Selection{{Reg: okReg}, {Reg: badReg}}
	out := d.RunWithHook(context.Background(), plugin.Task{ID: "t1"}, sel, nil, hook)
	if len(out) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(out))
	}
	mu.Lock()
	defer mu.Unlock()
	if fired["sec"] != 1 {
		t.Fatalf("成功插件 hook 应触发 1 次，got %d", fired["sec"])
	}
	if fired["bad"] != 0 {
		t.Fatalf("失败插件 hook 不应触发，got %d", fired["bad"])
	}
}

// TestRunWithHook_BestOfNFires best-of-N 聚合成功同样触发 hook（一次执行一次落定）。
func TestRunWithHook_BestOfNFires(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	r := hookReg("sec", &nopPlugin{manifest: plugin.AgentManifest{Name: "sec"}})
	r.Manifest.ReviewN = 3
	var mu sync.Mutex
	var n int
	out := d.RunWithHook(context.Background(), plugin.Task{ID: "t2"},
		[]Selection{{Reg: r}}, nil,
		func(o Outcome) { mu.Lock(); n++; mu.Unlock() })
	if len(out) != 1 || out[0].Result == nil {
		t.Fatalf("best-of-N 应聚合成功，got %+v", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("best-of-N 一次执行应只落定一次 hook，got %d", n)
	}
}

// TestRun_NilHookUnchanged Run（hook=nil）行为与此前逐字一致。
func TestRun_NilHookUnchanged(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	out := d.Run(context.Background(), plugin.Task{ID: "t3"},
		[]Selection{{Reg: hookReg("sec", &nopPlugin{manifest: plugin.AgentManifest{Name: "sec"}})}}, nil)
	if len(out) != 1 || out[0].Result == nil || out[0].Err != nil {
		t.Fatalf("Run 行为回归：%+v", out)
	}
}
