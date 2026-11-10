package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/oagentkit/llm"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
)

// fullCfg 主模型 + 全局降级 + 全部 P6 阶段覆盖（构造离线：模型/Provider 构造不发网络请求）。
func fullCfg() *config.Config {
	return &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "http://llm.test", APIKey: "k", Model: "main-model",
			Timeout: time.Second, ContextToken: 32000, MaxTokens: 4096,
			FallbackBaseURL: "http://llm.test", FallbackAPIKey: "k2", FallbackModel: "fallback-model",
			FallbackContextToken: 16000,
		},
		StageModelR1:       &config.StageOverride{Model: "r1-model"},
		StageModelR2:       &config.StageOverride{Model: "r2-model", ContextTokens: 8000},
		StageModelR3:       &config.StageOverride{Model: "r3-model"},
		StageModelR35:      &config.StageOverride{Model: "r35-model"},
		StageModelReviewer: &config.StageOverride{Model: "reviewer-model"},
		StageModelQA:       &config.StageOverride{Model: "qa-model"},
		StageModelRouter:   &config.StageOverride{Model: "router-model"},
	}
}

// v3.5.8 回归：模型栈一次解析——全角色出口非 nil，阶段覆盖与降级链就位。
func TestBuildModelsFullStack(t *testing.T) {
	log := logx.Nop()
	m, err := BuildModels(context.Background(), fullCfg(), log)
	if err != nil {
		t.Fatalf("BuildModels: %v", err)
	}
	if m.React == nil {
		t.Fatal("已配置 OPENAI_* 应 Ready")
	}
	if m.React == nil || m.Generate == nil {
		t.Fatal("主 React/Generate 出口缺失")
	}
	if m.Fallback == nil || m.FallbackName != "fallback-model" {
		t.Fatal("全局降级模型缺失")
	}
	if m.StageLLMs["R1"] == nil || m.StageLLMs["R2"] == nil {
		t.Fatalf("R1/R2 阶段覆盖缺失: %v", m.StageLLMs)
	}
	if m.R3 == nil || m.Reflect == nil || m.QA == nil || m.Router == nil {
		t.Fatal("R3/R35/QA/Router 覆盖出口缺失")
	}
	if m.ReviewerGen == nil || m.ReviewerName != "reviewer-model" {
		t.Fatal("Reviewer 覆盖链缺失")
	}
	if m.ReviewerReact == nil {
		t.Fatal("Reviewer ReAct 出口缺失")
	}
	if m.Limiter != nil {
		t.Fatal("未配置限速时 Limiter 应为 nil")
	}
}

// 未配置 OPENAI_* 不是错误：空栈 + Ready=false（仅 cli 插件形态）。
func TestBuildModelsNoLLM(t *testing.T) {
	m, err := BuildModels(context.Background(), &config.Config{}, logx.Nop())
	if err != nil {
		t.Fatalf("未配置 LLM 不应报错: %v", err)
	}
	if m.React != nil || m.Generate != nil || m.Fallback != nil || len(m.StageLLMs) != 0 {
		t.Fatal("无 LLM 配置应返回空栈")
	}
}

// 仅主模型（无降级/无覆盖）：Generate 为单模型 Client，React 不包 Failover，
// ReviewerReact 与 React 同源。
func TestBuildModelsMinimal(t *testing.T) {
	cfg := &config.Config{LLM: config.LLMConfig{
		BaseURL: "http://llm.test", APIKey: "k", Model: "main-model",
		Timeout: time.Second, ContextToken: 32000,
	}}
	m, err := BuildModels(context.Background(), cfg, logx.Nop())
	if err != nil {
		t.Fatalf("BuildModels: %v", err)
	}
	if m.React == nil || m.Generate == nil {
		t.Fatal("主模型缺失")
	}
	if m.Fallback != nil || m.StageLLMs != nil || m.R3 != nil || m.QA != nil || m.Router != nil {
		t.Fatal("未配置覆盖时不应产生覆盖出口")
	}
	if m.ReviewerReact != m.React {
		t.Fatal("无 Reviewer 覆盖时 ReviewerReact 应与主 React 同源")
	}
}

// 限速配置进 Limiter（runner 透传给非降级路径的 Client）。
func TestBuildModelsRateLimiter(t *testing.T) {
	cfg := fullCfg()
	cfg.LLMRateLimit = 5
	cfg.LLMRateBurst = 2
	m, err := BuildModels(context.Background(), cfg, logx.Nop())
	if err != nil {
		t.Fatalf("BuildModels: %v", err)
	}
	if m.Limiter == nil {
		t.Fatal("配置限速后 Limiter 应非 nil")
	}
}

// v3.5.9 回归：全局限速器必须覆盖主链（历史缺口：只到达非降级路径的阶段覆盖
// Client，主 Client/Resilient 链没挂）。
func TestBuildModelsLimiterWiring(t *testing.T) {
	log := logx.Nop()
	// 降级配置：主链与阶段覆盖链都是 Resilient
	cfg := fullCfg()
	cfg.LLMRateLimit = 5
	cfg.LLMRateBurst = 2
	m, err := BuildModels(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("BuildModels: %v", err)
	}
	r, ok := m.Generate.(*llm.Resilient)
	if !ok {
		t.Fatalf("降级配置下主链应为 Resilient: %T", m.Generate)
	}
	if r.Limiter == nil {
		t.Fatal("主 Resilient 链应挂限速器")
	}
	r1, ok := m.StageLLMs["R1"].(*llm.Resilient)
	if !ok {
		t.Fatalf("阶段覆盖链应为 Resilient: %T", m.StageLLMs["R1"])
	}
	if r1.Limiter == nil {
		t.Fatal("阶段覆盖 Resilient 应挂限速器")
	}

	// 非降级配置：主链是 Client
	cfg2 := &config.Config{
		LLM: config.LLMConfig{
			BaseURL: "http://llm.test", APIKey: "k", Model: "main-model",
			Timeout: time.Second, ContextToken: 32000,
		},
		LLMRateLimit: 5, LLMRateBurst: 2,
	}
	m2, err := BuildModels(context.Background(), cfg2, log)
	if err != nil {
		t.Fatalf("BuildModels: %v", err)
	}
	c, ok := m2.Generate.(*llm.Client)
	if !ok {
		t.Fatalf("非降级配置主链应为 Client: %T", m2.Generate)
	}
	if c.Limiter == nil {
		t.Fatal("主 Client 应挂限速器")
	}
}

func TestPtrNonNeg(t *testing.T) {
	if ptrNonNeg(0) != nil || ptrNonNeg(-1) != nil {
		t.Fatal("0/负数应返回 nil（不下发参数）")
	}
	if v := ptrNonNeg(5); v == nil || *v != 5 {
		t.Fatalf("正数应取指针: %v", v)
	}
}
