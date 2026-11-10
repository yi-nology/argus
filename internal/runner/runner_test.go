package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oekit/concurrency/ratelimit"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
)

// stubChatModel 最小 BaseChatModel 桩（装配测试不发真实请求）。
type stubChatModel struct{}

func (stubChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, context.Canceled
}

func (stubChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// buildWorkflow 无 LLM.Generator 时的 Client 回退装配：全局限速器、上下文窗口
// 接线就位（深挖 P0 限速器修复的接线回归）；无 R3 覆盖时 R3ChatModel 留空交
// runner 侧按 Fallback 再包。
func TestBuildWorkflowClientWiring(t *testing.T) {
	r := &Runner{
		Cfg:        &config.Config{LLM: config.LLMConfig{ContextToken: 32000}},
		Model:      stubChatModel{},
		ModelName:  "stub",
		LLMLimiter: ratelimit.NewTokenBucketFloat(5, 5),
		Log:        logx.Nop(),
	}
	wf := r.buildWorkflow(nil)
	c, ok := wf.LLM.(*llm.Client)
	if !ok {
		t.Fatalf("无 Generator 时应装配 Client, got %T", wf.LLM)
	}
	if c.Limiter == nil {
		t.Fatal("全局限速器应透传 Client")
	}
	if c.ContextTokens != 32000 {
		t.Fatalf("上下文窗口应接线, got %d", c.ContextTokens)
	}
	if wf.R3ChatModel != nil {
		t.Fatal("无 R3 覆盖且无 Generator 时 R3ChatModel 应为 nil")
	}
}

// StageLLMs 非空 → 缺省链经 StageRouter 装配（P6 阶段路由，R1 命中 R1a 前缀匹配）。
func TestBuildWorkflowStageRouter(t *testing.T) {
	gen := llm.NewClient(stubChatModel{}, "stub", nil)
	r := &Runner{
		Cfg:       &config.Config{LLM: config.LLMConfig{ContextToken: 32000}},
		LLM:       gen,
		StageLLMs: map[string]llm.Generator{"R1": gen},
		Log:       logx.Nop(),
	}
	wf := r.buildWorkflow(nil)
	if _, ok := wf.LLM.(*llm.StageRouter); !ok {
		t.Fatalf("配置 StageLLMs 应装配 StageRouter, got %T", wf.LLM)
	}
}

type stubSkills struct{ calls int }

func (s *stubSkills) Resolve(_ context.Context, ref skill.Ref) (*skill.Skill, error) {
	s.calls++
	return &skill.Skill{Name: ref.Name, Content: "方法论正文", Checksum: "deadbeef"}, nil
}

// skillText 惰性解析 + 10 分钟缓存（§9.1：正文带 checksum 头防内容漂移难排查）。
func TestSkillTextCache(t *testing.T) {
	sp := &stubSkills{}
	r := &Runner{
		SkillProvider: sp,
		SkillRefs:     []string{"methodology@v1"},
		Cfg:           &config.Config{},
		Log:           logx.Nop(),
	}
	_ = r.skillText(context.Background())
	_ = r.skillText(context.Background())
	if sp.calls != 1 {
		t.Fatalf("缓存窗口内应只解析一次, calls=%d", sp.calls)
	}
	if !strings.Contains(r.skillText(context.Background()), "checksum=deadbeef") {
		t.Fatalf("正文应带 checksum 头: %q", r.skillCache)
	}
	if sp.calls != 1 {
		t.Fatalf("第三次读取仍应命中缓存, calls=%d", sp.calls)
	}
}
