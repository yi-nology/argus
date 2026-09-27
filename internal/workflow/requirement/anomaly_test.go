package requirement

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestAnomalyGuardBlocksThirdIdenticalCall(t *testing.T) {
	g := newAnomalyGuard()
	if _, blocked := g.check("get_file", `{"path":"a.go"}`); blocked {
		t.Fatal("第 1 次应放行")
	}
	if _, blocked := g.check("get_file", `{"path": "a.go"}`); blocked {
		t.Fatal("第 2 次应放行（键序差异规范化后同签名）")
	}
	nudge, blocked := g.check("get_file", `{"path":"a.go"}`)
	if !blocked {
		t.Fatal("第 3 次连续同签名应短路")
	}
	if !strings.Contains(nudge, "NOT USER INPUT") || !strings.Contains(nudge, "get_file") {
		t.Fatalf("提醒应含反伪装声明与工具名，got %q", nudge)
	}
}

func TestAnomalyGuardChainBreakResets(t *testing.T) {
	g := newAnomalyGuard()
	_, _ = g.check("get_file", `{"path":"a.go"}`)
	_, _ = g.check("get_file", `{"path":"a.go"}`)
	_, _ = g.check("get_diff", `{}`) // 链断
	if _, blocked := g.check("get_file", `{"path":"a.go"}`); blocked {
		t.Fatal("链断后同签名应重新计数（第 1 次）")
	}
	if _, blocked := g.check("get_file", `{"path":"a.go"}`); blocked {
		t.Fatal("链断后第 2 次应放行")
	}
	if _, blocked := g.check("get_file", `{"path":"a.go"}`); !blocked {
		t.Fatal("链断后第 3 次应短路")
	}
}

func TestAnomalyGuardNudgeCapReleases(t *testing.T) {
	g := newAnomalyGuard()
	blockedCount := 0
	for i := 0; i < 6; i++ {
		if _, blocked := g.check("get_symbol", `{"name":"Foo"}`); blocked {
			blockedCount++
		}
	}
	if blockedCount != anomalyMaxNudges {
		t.Fatalf("提醒封顶后应放行执行，blocked=%d want=%d", blockedCount, anomalyMaxNudges)
	}
}

func TestAnomalyGuardDifferentArgsDifferentChain(t *testing.T) {
	g := newAnomalyGuard()
	_, _ = g.check("get_file", `{"path":"a.go"}`)
	_, _ = g.check("get_file", `{"path":"b.go"}`)
	if _, blocked := g.check("get_file", `{"path":"a.go"}`); blocked {
		t.Fatal("不同参数属不同签名，应放行")
	}
}

func TestCanonicalArgsKeyOrderInvariant(t *testing.T) {
	if canonicalArgs(`{"b":2,"a":1}`) != canonicalArgs(`{"a": 1, "b": 2}`) {
		t.Fatal("键序不同的等价 JSON 应规范化为同一签名")
	}
	if canonicalArgs(`not-json`) != `not-json` {
		t.Fatal("非法 JSON 应原样返回")
	}
}

// fakeNamedTool 仅实现 BaseTool（Info）——验证排序 helper。
type fakeNamedTool struct{ name string }

func (f *fakeNamedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}

func TestSortToolsByNameDeterministic(t *testing.T) {
	tools := []tool.BaseTool{
		&fakeNamedTool{name: "zget"}, &fakeNamedTool{name: "aget"}, &fakeNamedTool{name: "mget"},
	}
	sortToolsByName(context.Background(), tools)
	if tools[0].(*fakeNamedTool).name != "aget" || tools[2].(*fakeNamedTool).name != "zget" {
		t.Fatal("应按名升序")
	}
}
