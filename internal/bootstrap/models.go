package bootstrap

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"golang.org/x/time/rate"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/config"
	"git.example.com/enjoydream/agentkit/llm"
)

// Models 装配期一次解析完成的模型栈。原散在 main.go 的 ~270 行 if/else 收敛于此：
// 主模型 + 全局降级链（OPENAI_FALLBACK_*）+ P6 阶段覆盖（ARGUS_MODEL_{R1,R2,R3,R35,
// REVIEWER,QA,ROUTER}）。不变量：
//   - Generator 路径（workflow 生成 / Reviewer 覆盖链）走 Resilient（主备）或 Client；
//   - 所有 BaseChatModel 直调出口（ReAct/单轮直调）统一包 FailoverModel——
//     Resilient.RawModel 只返回主模型，ReAct 旁路靠 Failover 补齐主备切换；
//   - 阶段覆盖继承主 BaseURL/APIKey；fallback 模型自身不做二级覆盖。
//
// 字段 nil 语义：Ready()==false 时全部为 nil（仅 cli 插件可用）；其余覆盖类字段
//（R3/Reflect/QA/Router/ReviewerGen/StageLLMs）nil = 未覆盖，消费方按各自回退链
// 取缺省（见 runner.buildWorkflow / service.routerChatModel）。
type Models struct {
	// React 主模型 BaseChatModel 出口（已包 Failover；无降级配置时即原始 chatModel）。
	// runner.Model / service.Model / registry Deps.Model 均取本字段。
	React model.BaseChatModel
	// Generate 主生成链（Resilient 多模型降级或单模型 Client）；workflow 每任务经
	// BudgetInjector 注入预算后使用。
	Generate llm.Generator
	// Limiter 全局 LLM 限速器（ARGUS_LLM_RATE_LIMIT > 0 时非 nil；runner 透传）。
	Limiter *rate.Limiter
	// Fallback 全局备模型 BaseChatModel（nil = 未配置降级）。runner 对 R3 旁路
	//（R3Model 为 nil 时）用 Fallback+FallbackName 再包一层 Failover。
	Fallback     model.BaseChatModel
	FallbackName string

	// StageLLMs R1/R2 阶段覆盖生成链（经 llm.StageRouter 注册进 workflow）。
	StageLLMs map[string]llm.Generator
	// R3 ReAct 专用覆盖（ARGUS_MODEL_R3；已包 Failover）。
	R3 model.BaseChatModel
	// Reflect R3.5 报告反思覆盖（ARGUS_MODEL_R35；已包 Failover）。
	Reflect model.BaseChatModel
	// ReviewerGen/ReviewerName R4 Reviewer 覆盖链（ARGUS_MODEL_REVIEWER）。
	ReviewerGen  llm.Generator
	ReviewerName string
	// ReviewerReact Reviewer 的 ReAct 出口：有覆盖时取覆盖链 RawModel 再包 Failover，
	// 否则与 React 同源。
	ReviewerReact model.BaseChatModel
	// QA Q&A 专用覆盖（ARGUS_MODEL_QA；已包 Failover）。
	QA model.BaseChatModel
	// Router 评论意图分类覆盖（ARGUS_MODEL_ROUTER；已包 Failover）。
	Router model.BaseChatModel
}

// Ready LLM 是否已配置（false = OPENAI_* 缺失，builtin 插件与 Q&A 不可用）。
func (m *Models) Ready() bool { return m.React != nil }

// BuildModels 解析全部模型角色。构造失败返回 error（调用方决定终止方式）；
// 未配置 OPENAI_* 不是错误——返回空栈并告警（仅 cli 插件形态）。
func BuildModels(ctx context.Context, cfg *config.Config, log logx.Logger) (*Models, error) {
	m := &Models{}
	if cfg.LLM.BaseURL == "" || cfg.LLM.APIKey == "" || cfg.LLM.Model == "" {
		log.Warn("argus.boot.no_llm", "hint", "OPENAI_* 未配置，builtin 插件不可用（仅 cli 插件可工作）")
		return m, nil
	}

	// ---- LLM 全局限速器（token bucket；ARGUS_LLM_RATE_LIMIT > 0 时启用）----
	// v3.5.9 起覆盖主链 Client/Resilient（历史缺口：此前只到达非降级路径的阶段
	// 覆盖 Client）。ReAct RawModel 旁路不经 Client/Resilient，仍不受限（agentkit 侧限制）。
	if cfg.LLMRateLimit > 0 {
		m.Limiter = rate.NewLimiter(rate.Limit(cfg.LLMRateLimit), cfg.LLMRateBurst)
		log.Info("argus.boot.llm_rate_limiter", "rps", cfg.LLMRateLimit, "burst", cfg.LLMRateBurst)
	}

	maxTokens := cfg.MaxTokensParam()
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		Timeout: cfg.LLM.Timeout,
		// 负值 = 不下发 max_tokens（agentkit 语义，推理模型兼容）
		MaxTokens: ptrNonNeg(maxTokens),
	})
	if err != nil {
		return nil, fmt.Errorf("构造 LLM 客户端失败: %w", err)
	}
	m.React = chatModel

	// 全局降级链：配置 OPENAI_FALLBACK_* 四项后启用 Resilient 多模型降级。
	var fallbackProvider llm.Provider // 阶段覆盖构建各自降级链时复用
	fallbackReady := cfg.LLM.FallbackBaseURL != "" && cfg.LLM.FallbackAPIKey != "" && cfg.LLM.FallbackModel != ""
	if !fallbackReady {
		c := llm.NewClient(chatModel, cfg.LLM.Model, nil)
		c.ContextTokens = cfg.LLM.ContextToken
		c.MaxOutputTokens = cfg.MaxTokensResolved()
		c.Limiter = m.Limiter
		m.Generate = c
	} else {
		primary, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
			ContextTokens: cfg.LLM.ContextToken, MaxOutputTokens: cfg.MaxTokensParam(),
			Timeout: cfg.LLM.Timeout,
		})
		if e != nil {
			return nil, fmt.Errorf("构造主 LLM Provider 失败: %w", e)
		}
		fb, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
			BaseURL: cfg.LLM.FallbackBaseURL, APIKey: cfg.LLM.FallbackAPIKey, Model: cfg.LLM.FallbackModel,
			ContextTokens: cfg.LLM.FallbackContextToken,
		})
		if e != nil {
			return nil, fmt.Errorf("构造降级 LLM Provider 失败: %w", e)
		}
		fallbackProvider = fb
		resilient, e := llm.NewResilient(llm.NewFallbackChain(primary, fallbackProvider), llm.ResilientConfig{
			RetriesPerModel: cfg.LLM.LLMResilienceRetries,
			BaseDelay:       cfg.LLM.LLMResilienceBaseDelay,
		})
		if e != nil {
			return nil, fmt.Errorf("构造 Resilient 失败: %w", e)
		}
		// 降级事件接结构化日志（运维观测主/备切换频率）
		resilient.OnFallback = func(from, to, stage, reason string) {
			log.Warn("argus.llm.fallback", "from", from, "to", to,
				"stage", stage, "reason", reason)
		}
		resilient.Limiter = m.Limiter
		m.Generate = resilient

		// ReAct 主路径（ADK ChatModelAgent 直调 BaseChatModel）绕过 Resilient——
		// FailoverModel 装饰补齐主备切换（agentkit v0.9.4；Resilient.RawModel 只返回主模型）
		fallbackBM, e := openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL: cfg.LLM.FallbackBaseURL,
			APIKey:  cfg.LLM.FallbackAPIKey,
			Model:   cfg.LLM.FallbackModel,
			Timeout: cfg.LLM.Timeout,
		})
		if e != nil {
			return nil, fmt.Errorf("构造降级 BaseChatModel 失败: %w", e)
		}
		m.Fallback = fallbackBM
		m.FallbackName = cfg.LLM.FallbackModel

		log.Info("argus.boot.llm_resilient", "chain", resilient.String(),
			"primary_ctx", cfg.LLM.ContextToken, "fallback_ctx", cfg.LLM.FallbackContextToken)
		log.Info("argus.boot.llm_failover", "react", "enabled", "fallback_model", cfg.LLM.FallbackModel)
	}
	log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model, "base_url", cfg.LLM.BaseURL,
		"context_tokens", cfg.LLM.ContextToken, "max_tokens", maxTokens,
		"diff_chars", cfg.MaxDiffCharsResolved(), "task_budget", cfg.EffectiveTaskTokenBudget())

	// ---- P6 分阶段模型路由 ----
	buildStageModel := func(so *config.StageOverride) (model.BaseChatModel, error) {
		return openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			MaxTokens: ptrNonNeg(cfg.MaxTokensParam()), Timeout: cfg.LLM.Timeout,
		})
	}
	buildStageGen := func(so *config.StageOverride) (llm.Generator, error) {
		p, err := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			ContextTokens: so.ContextTokens, MaxOutputTokens: cfg.MaxTokensParam(),
			Timeout: cfg.LLM.Timeout,
		})
		if err != nil {
			return nil, err
		}
		return stageGenerator(p, fallbackProvider, so, cfg, m.Limiter)
	}
	for stage, so := range map[string]*config.StageOverride{
		"R1": cfg.StageModelR1, "R2": cfg.StageModelR2,
	} {
		if so == nil {
			continue
		}
		g, err := buildStageGen(so)
		if err != nil {
			return nil, fmt.Errorf("构造 %s 阶段模型失败: %w", stage, err)
		}
		if m.StageLLMs == nil {
			m.StageLLMs = map[string]llm.Generator{}
		}
		m.StageLLMs[stage] = g
		log.Info("argus.boot.stage_model", "stage", stage, "model", so.Model)
	}
	if so := cfg.StageModelR3; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return nil, fmt.Errorf("构造 R3 模型失败: %w", err)
		}
		m.R3 = mm
		log.Info("argus.boot.stage_model", "stage", "R3(ReAct)", "model", so.Model)
	}
	if so := cfg.StageModelR35; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return nil, fmt.Errorf("构造 R3.5 模型失败: %w", err)
		}
		m.Reflect = mm
		log.Info("argus.boot.stage_model", "stage", "R35(reflection)", "model", so.Model)
	}
	if so := cfg.StageModelRouter; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return nil, fmt.Errorf("构造 Router 模型失败: %w", err)
		}
		m.Router = mm
		log.Info("argus.boot.stage_model", "stage", "router", "model", so.Model)
	}
	if so := cfg.StageModelReviewer; so != nil {
		g, err := buildStageGen(so)
		if err != nil {
			return nil, fmt.Errorf("构造 Reviewer 模型失败: %w", err)
		}
		m.ReviewerGen = g
		m.ReviewerName = so.Model
		log.Info("argus.boot.stage_model", "stage", "reviewer", "model", so.Model)
	}
	if so := cfg.StageModelQA; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return nil, fmt.Errorf("构造 QA 模型失败: %w", err)
		}
		m.QA = mm
		log.Info("argus.boot.stage_model", "stage", "qa", "model", so.Model)
	}

	// Failover 包装：仅配置了全局降级时生效；每个直调出口按各自 stage 名标注切换日志。
	if m.Fallback != nil {
		wrap := wrapReactFailoverFn(cfg, m.Fallback, log)
		m.React = wrap(m.React, "chat")
		if m.R3 != nil {
			m.R3 = wrap(m.R3, "R3")
		}
		if m.QA != nil {
			m.QA = wrap(m.QA, "qa")
		}
		if m.Router != nil {
			m.Router = wrap(m.Router, "router")
		}
		if m.Reflect != nil {
			m.Reflect = wrap(m.Reflect, "R35")
		}
		// Reviewer ReAct 出口：有阶段覆盖时用覆盖链 RawModel（需 Failover 包）；
		// 无覆盖或覆盖链无 RawModel 时与主 React 出口同源（已包 Failover）。
		if m.ReviewerGen != nil {
			if rm, ok := m.ReviewerGen.(interface{ RawModel() model.BaseChatModel }); ok {
				if raw := rm.RawModel(); raw != nil {
					m.ReviewerReact = wrap(raw, "reviewer")
				}
			}
		}
	}
	if m.ReviewerReact == nil {
		m.ReviewerReact = m.React
	}
	return m, nil
}

// stageGenerator 阶段覆盖生成链：有全局降级 → Resilient 主备链（与主链共用
// fallback Provider）；否则单模型 Client（挂全局限速器）。
func stageGenerator(p llm.Provider, fallback llm.Provider, so *config.StageOverride,
	cfg *config.Config, limiter *rate.Limiter) (llm.Generator, error) {
	if fallback != nil {
		r, err := llm.NewResilient(llm.NewFallbackChain(p, fallback), llm.ResilientConfig{
			RetriesPerModel: cfg.LLM.LLMResilienceRetries, BaseDelay: cfg.LLM.LLMResilienceBaseDelay,
		})
		if err != nil {
			return nil, err
		}
		r.Limiter = limiter
		return r, nil
	}
	c := llm.NewClient(p.Model(), so.Model, nil)
	c.ContextTokens = so.ContextTokens
	if c.ContextTokens <= 0 {
		c.ContextTokens = cfg.LLM.ContextToken
	}
	c.MaxOutputTokens = cfg.MaxTokensResolved()
	c.Limiter = limiter
	return c, nil
}

// wrapReactFailoverFn ReAct/直调出口的 FailoverModel 包装器（stage 名进切换日志）。
func wrapReactFailoverFn(cfg *config.Config, fallbackBM model.BaseChatModel,
	log logx.Logger) func(model.BaseChatModel, string) model.BaseChatModel {
	return func(primary model.BaseChatModel, stage string) model.BaseChatModel {
		if primary == nil {
			return nil
		}
		fm := llm.NewFailoverModel(primary, fallbackBM, cfg.LLM.Model, cfg.LLM.FallbackModel)
		fm.OnFailover = func(from, to, reason string) {
			log.Warn("argus.llm.failover", "from", from, "to", to,
				"stage", stage, "reason", reason)
		}
		return fm
	}
}

// ptrNonNeg 正数取指针；0/负数返回 nil（不下发该参数）。
func ptrNonNeg(n int) *int {
	if n > 0 {
		t := n
		return &t
	}
	return nil
}
