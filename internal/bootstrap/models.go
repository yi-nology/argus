package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/oekit/concurrency/ratelimit"

	"github.com/oekit/logx"
	"github.com/oekit/pkg/httpjson"
	"github.com/oekit/tracing/httpotel"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/wrap"
)

// tracedLLMClient LLM 出站 HTTP 客户端（v3.177.0，llm v0.15 HTTPClient +
// eino-ext HTTPClient 注入缝）：Transport 挂 oekit/tracing httpotel——LLM 调用
// 成 OTel span 导出（此前 OTel 只有 gormotel DB 查询面，LLM 主耗时面无 trace）。
// 注入的 Client 完全接管超时（两侧同一语义：设了 HTTPClient 就不再用
// config.Timeout），timeout<=0 取 provider 缺省 60s 行为同款。
//
// v3.182.0：外层再包 httpjson.RetryAfterTransport——429 的 Retry-After 头写入
// ctx sink，供 agentrun 重试环按服务端建议退避（比本地指数准）。这是**单点**捕获：
// eino ChatModel 直调出口（ReAct，无 CaptureRetryAfter 可设）与 oagentkit Provider
// 出口（httpClient 会再套一层，RetryAfterTransport 幂等——只是重复 set 同值，无害）。
// 此前只有 Provider 出口捕获、ReAct 主路径漏（agentrun 已注入 sink 但无人写入）。
func tracedLLMClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: httpjson.RetryAfterTransport{
		Base: httpotel.WrapTransport(http.DefaultTransport),
	}}
}

// Models 装配期一次解析完成的模型栈。原散在 main.go 的 ~270 行 if/else 收敛于此：
// 主模型 + 全局降级链（OPENAI_FALLBACK_*）+ P6 阶段覆盖（ARGUS_MODEL_{R1,R2,R3,R35,
// REVIEWER,QA,ROUTER}）。不变量：
//   - Generator 路径（workflow 生成 / Reviewer 覆盖链）走 Resilient（主备）或 Client；
//   - 所有 BaseChatModel 直调出口（ReAct/单轮直调）统一包 FailoverModel——
//     阶段覆盖的**裸模型**（R3Model，buildStageModel 出 openai.NewChatModel，无降级）
//     靠它补齐主备切换（此包装是其唯一 failover，勿删）；无覆盖时 r3Chat 来自
//     Resilient.RawModel（v0.10.9+ 已是 RawModelWithFailover，本身 failover 感知），
//     再包一次属无害冗余（总败时多试一次 fallback）——故统一包而非按来源分支；
//   - 阶段覆盖继承主 BaseURL/APIKey；fallback 模型自身不做二级覆盖。
//
// 字段 nil 语义：Ready()==false 时全部为 nil（仅 cli 插件可用）；其余覆盖类字段
// （R3/Reflect/QA/Router/ReviewerGen/StageLLMs）nil = 未覆盖，消费方按各自回退链
// 取缺省（见 runner.buildWorkflow / service.routerChatModel）。
type Models struct {
	// React 主模型 BaseChatModel 出口（已包 Failover；无降级配置时即原始 chatModel）。
	// runner.Model / service.Model / registry Deps.Model 均取本字段。
	React model.BaseChatModel
	// Generate 主生成链（Resilient 多模型降级或单模型 Client）；workflow 每任务经
	// BudgetInjector 注入预算后使用。
	Generate llm.Generator
	// Limiter 全局 LLM 限速器（ARGUS_LLM_RATE_LIMIT > 0 时非 nil；runner 透传）。
	Limiter ratelimit.Limiter
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
	// 覆盖 Client）。ReAct RawModel 旁路不经 Client/Resilient，仍不受限（oagentkit 侧限制）。
	if cfg.LLMRateLimit > 0 {
		m.Limiter = ratelimit.NewTokenBucketFloat(cfg.LLMRateLimit, cfg.LLMRateBurst)
		log.Info("argus.boot.llm_rate_limiter", "rps", cfg.LLMRateLimit, "burst", cfg.LLMRateBurst)
	}

	maxTokens := cfg.MaxTokensParam()
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:    cfg.LLM.BaseURL,
		APIKey:     cfg.LLM.APIKey,
		Model:      cfg.LLM.Model,
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		// 负值 = 不下发 max_tokens（oagentkit 语义，推理模型兼容）
		MaxTokens: ptrNonNeg(maxTokens),
	})
	if err != nil {
		return nil, fmt.Errorf("构造 LLM 客户端失败: %w", err)
	}
	m.React = chatModel

	// 全局降级链：配置 OPENAI_FALLBACK_* 四项后启用 Resilient 多模型降级。
	fallbackProvider, err := m.buildGenerateChain(ctx, cfg, log, chatModel)
	if err != nil {
		return nil, err
	}
	log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model, "base_url", cfg.LLM.BaseURL,
		"context_tokens", cfg.LLM.ContextToken, "max_tokens", maxTokens,
		"diff_chars", cfg.MaxDiffCharsResolved(), "task_budget", cfg.EffectiveTaskTokenBudget())

	// ---- P6 分阶段模型路由 ----
	if err := m.buildStageLLMs(ctx, cfg, log, fallbackProvider); err != nil {
		return nil, err
	}
	// Failover 包装：仅配置了全局降级时生效；每个直调出口按各自 stage 名标注切换日志。
	m.wrapFailover(cfg, log)
	// ---- 上下文窗口拟合 + 输出续写（v3.33/v3.34）----
	m.wrapFit(cfg, log)
	return m, nil
}

// buildGenerateChain 主生成链：无降级配置 → 单模型 Client；配置 OPENAI_FALLBACK_*
// → Resilient 主备链 + 降级 BaseChatModel（ReAct 旁路 Failover 用）。返回的
// fallbackProvider 供阶段覆盖构建各自降级链复用（nil = 无降级）。
func (m *Models) buildGenerateChain(ctx context.Context, cfg *config.Config, log logx.Logger,
	chatModel model.BaseChatModel) (llm.Provider, error) {
	fallbackReady := cfg.LLM.FallbackBaseURL != "" && cfg.LLM.FallbackAPIKey != "" && cfg.LLM.FallbackModel != ""
	if !fallbackReady {
		c := llm.NewClient(chatModel, cfg.LLM.Model, nil)
		c.ContextTokens = cfg.LLM.ContextToken
		c.MaxOutputTokens = cfg.MaxTokensResolved()
		// v5.5.2：无降级分支同样落 RETRIES/BASE_DELAY——此前该 env 只在 Resilient
		// 链生效，单模型 Client 恒用库默认（3 试/2s 基），429 退避够不着分钟级限流
		// 窗口（demoos 网关 per-model RPM 实测 10）。Client.MaxRetries 含首次，
		// env 语义「不含首次」，+1 对齐。
		c.MaxRetries = cfg.LLM.LLMResilienceRetries + 1
		c.BaseDelay = cfg.LLM.LLMResilienceBaseDelay
		c.Limiter = m.Limiter
		m.Generate = c
		return nil, nil
	}
	primary, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
		BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
		ContextTokens: cfg.LLM.ContextToken, MaxOutputTokens: cfg.MaxTokensParam(),
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		// v0.10.16：429 响应捕获 Retry-After，重试环按服务端建议退避（限流窗口
		// 还剩多久只有端点知道；mimo 并发限流下比本地指数曲线更准）
		CaptureRetryAfter: true,
	})
	if e != nil {
		return nil, fmt.Errorf("构造主 LLM Provider 失败: %w", e)
	}
	fb, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
		BaseURL: cfg.LLM.FallbackBaseURL, APIKey: cfg.LLM.FallbackAPIKey, Model: cfg.LLM.FallbackModel,
		ContextTokens:     cfg.LLM.FallbackContextToken,
		CaptureRetryAfter: true,
		HTTPClient:        tracedLLMClient(cfg.LLM.Timeout),
	})
	if e != nil {
		return nil, fmt.Errorf("构造降级 LLM Provider 失败: %w", e)
	}
	resilient, e := llm.NewResilient(llm.NewFallbackChain(primary, fb), llm.ResilientConfig{
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
		obs.IncrLLMFailover(stage)
	}
	resilient.Limiter = m.Limiter
	m.Generate = resilient

	// ReAct 主路径（ADK ChatModelAgent 直调 BaseChatModel）绕过 Resilient——
	// FailoverModel 装饰补齐主备切换（oagentkit；阶段覆盖裸模型无降级，靠它补；
	// 无覆盖时 RawModel 自带 failover，再包为无害冗余，见上方不变量注）。
	fallbackBM, e := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:    cfg.LLM.FallbackBaseURL,
		APIKey:     cfg.LLM.FallbackAPIKey,
		Model:      cfg.LLM.FallbackModel,
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
	})
	if e != nil {
		return nil, fmt.Errorf("构造降级 BaseChatModel 失败: %w", e)
	}
	m.Fallback = fallbackBM
	m.FallbackName = cfg.LLM.FallbackModel

	log.Info("argus.boot.llm_resilient", "chain", resilient.String(),
		"primary_ctx", cfg.LLM.ContextToken, "fallback_ctx", cfg.LLM.FallbackContextToken)
	log.Info("argus.boot.llm_failover", "react", "enabled", "fallback_model", cfg.LLM.FallbackModel)
	return fb, nil
}

// buildStageLLMs P6 阶段覆盖：R1/R2 走生成链（StageRouter 注册），R3/R35/Router/QA
// 直调模型，Reviewer 覆盖链。各覆盖继承主 BaseURL/APIKey。
func (m *Models) buildStageLLMs(ctx context.Context, cfg *config.Config, log logx.Logger,
	fallbackProvider llm.Provider) error {
	buildStageModel := func(so *config.StageOverride) (model.BaseChatModel, error) {
		// v3.33.0 ctxfit：直调阶段窗口拟合生效（此前 CONTEXT_TOKENS 对直调路径
		// 静默无效——v3.8.0 的「配置误导」告警随之解除）
		if so.ContextTokens > 0 && so.ContextTokens != cfg.LLM.ContextToken {
			log.Info("argus.boot.stage_window", "model", so.Model,
				"configured_ctx", so.ContextTokens, "main_ctx", cfg.LLM.ContextToken,
				"hint", "直调阶段窗口拟合已生效（ctxfit 两级拟合，超限才动作）")
		}
		return openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			MaxTokens: ptrNonNeg(cfg.MaxTokensParam()), Timeout: cfg.LLM.Timeout,
			HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		})
	}
	buildStageGen := func(so *config.StageOverride) (llm.Generator, error) {
		p, err := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			ContextTokens: so.ContextTokens, MaxOutputTokens: cfg.MaxTokensParam(),
			Timeout:           cfg.LLM.Timeout,
			CaptureRetryAfter: true,
			HTTPClient:        tracedLLMClient(cfg.LLM.Timeout),
		})
		if err != nil {
			return nil, err
		}
		return stageGenerator(p, fallbackProvider, so, cfg, m.Limiter)
	}
	for stage, so := range map[string]*config.StageOverride{
		plugin.LayerR1: cfg.StageModelR1, plugin.LayerR2: cfg.StageModelR2,
	} {
		if so == nil {
			continue
		}
		g, err := buildStageGen(so)
		if err != nil {
			return fmt.Errorf("构造 %s 阶段模型失败: %w", stage, err)
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
			return fmt.Errorf("构造 R3 模型失败: %w", err)
		}
		m.R3 = mm
		log.Info("argus.boot.stage_model", "stage", "R3(ReAct)", "model", so.Model)
	}
	if so := cfg.StageModelR35; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 R3.5 模型失败: %w", err)
		}
		m.Reflect = mm
		log.Info("argus.boot.stage_model", "stage", "R35(reflection)", "model", so.Model)
	}
	if so := cfg.StageModelRouter; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 Router 模型失败: %w", err)
		}
		m.Router = mm
		log.Info("argus.boot.stage_model", "stage", "router", "model", so.Model)
	}
	if so := cfg.StageModelReviewer; so != nil {
		g, err := buildStageGen(so)
		if err != nil {
			return fmt.Errorf("构造 Reviewer 模型失败: %w", err)
		}
		m.ReviewerGen = g
		m.ReviewerName = so.Model
		log.Info("argus.boot.stage_model", "stage", "reviewer", "model", so.Model)
	}
	if so := cfg.StageModelQA; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 QA 模型失败: %w", err)
		}
		m.QA = mm
		log.Info("argus.boot.stage_model", "stage", "qa", "model", so.Model)
	}
	return nil
}

// wrapFailover FailoverModel 包装：仅配置了全局降级时生效，每个直调出口按各自
// stage 名标注切换日志；ReviewerReact 兜底（=主 React 出口）不受降级配置影响。
func (m *Models) wrapFailover(cfg *config.Config, log logx.Logger) {
	if m.Fallback != nil {
		wrap := wrapReactFailoverFn(cfg, m.Fallback, log)
		m.React = wrap(m.React, "chat")
		if m.R3 != nil {
			m.R3 = wrap(m.R3, plugin.LayerR3)
		}
		if m.QA != nil {
			m.QA = wrap(m.QA, "qa")
		}
		if m.Router != nil {
			m.Router = wrap(m.Router, "router")
		}
		if m.Reflect != nil {
			m.Reflect = wrap(m.Reflect, plugin.LayerR35)
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
}

// wrapFit 直调出口统一包 Rate（限速最内层，v3.36.0）+ FitModel（输入两级拟合，
// 超限才动作、未超限零拷贝直通）+ ContinueModel（length 截断续写 ≤3 轮）。
// Generator 链已有 Client.fitInput，重叠路径双重拟合无副作用。
// 窗口取值：阶段覆盖 CONTEXT_TOKENS > 主配置 OPENAI_CONTEXT_TOKEN。
func (m *Models) wrapFit(cfg *config.Config, log logx.Logger) {
	fitOnEvent := func(stage string, res wrap.FitResult) {
		obs.IncrCtxFit(stage, string(res.Action))
		log.Warn("argus.ctxfit", "stage", stage, "action", string(res.Action),
			"before_runes", res.Before, "after_runes", res.After, "limit_runes", res.Limit,
			"cleared_tools", res.Cleared)
	}
	continueOnEvent := func(stage string, round, addedRunes int) {
		obs.IncrLLMContinued(stage)
		log.Warn("argus.llm.continued", "stage", stage, "round", round, "added_runes", addedRunes)
	}
	stageWindow := func(so *config.StageOverride) int {
		if so != nil && so.ContextTokens > 0 {
			return so.ContextTokens
		}
		return cfg.LLM.ContextToken
	}
	// 窗口配置防呆（v3.179.0 省 token 面）：FitModel 清理触发线 =(窗口-输出预留)
	// ×3/2×0.9（CJK 感知）——窗口虚高（如 flash 档配 1M）时触发线到百万级 runes，
	// 单请求上下文（工具预算合计 ~6 万 runes）永远到不了，microcompact 的「旧工具
	// 结果置占位」永不触发 → 工具结果随 ReAct 轮数线性膨胀（215 实测 role=tool
	// 累计 5000 万 runes = 上下文面 70%）。命中告警提示校正为模型真实窗口。
	const windowSanityTokens = 256_000
	if cfg.LLM.ContextToken > windowSanityTokens {
		log.Warn("argus.boot.window_suspect", "context_tokens", cfg.LLM.ContextToken,
			"hint", "窗口疑似虚高：FitModel 工具历史清理线将远超实际请求量级，"+
				"工具结果随轮数膨胀烧 token——按模型真实窗口校正 ARGUS_MODEL_CONTEXT_TOKENS")
	}
	wrapStage := func(bm model.BaseChatModel, stage string, window int) model.BaseChatModel {
		if bm == nil {
			return nil
		}
		// 限速最内层（v3.36.0）：直调旁路（ReAct/RawModel）与 Generator 链同桶，
		// ARGUS_LLM_RATE_LIMIT 此前对旁路无效的缺口闭合；Limiter nil = 直通。
		bm = wrap.NewRateModel(bm, m.Limiter)
		fm := wrap.NewFitModel(bm, stage, cfg.LLM.Model, window, cfg.MaxTokensResolved(), fitOnEvent)
		// v3.77.0 下沉后观测回调化：构成分解在装配点接 Prometheus（此前在实现内
		// 硬连 obs，SDK 不可携带 argus 指标）。
		fm.OnBreakdown = func(stage string, byRole map[string]int) {
			for role, n := range byRole {
				obs.AddCtxBreakdown(stage, role, n)
			}
		}
		// v5.5.3：重试环补在直调旁路最外层——ReAct（R3/Reviewer/R35/QA/Router）
		// 此前一发 429 即整轮阵亡（单轮侧早已有的韧性，这里对齐同参数）。
		return newRetryChatModel(wrap.NewContinueModel(fm, stage, continueOnEvent),
			cfg.LLM.LLMResilienceRetries+1, cfg.LLM.LLMResilienceBaseDelay,
			30*time.Second, stage, log)
	}
	m.React = wrapStage(m.React, "chat", cfg.LLM.ContextToken)
	if m.ReviewerGen == nil {
		m.ReviewerReact = m.React // 无 Reviewer 覆盖：与主 React 同源（含包装）
	}
	if m.R3 != nil {
		m.R3 = wrapStage(m.R3, plugin.LayerR3, stageWindow(cfg.StageModelR3))
	}
	if m.Reflect != nil {
		m.Reflect = wrapStage(m.Reflect, plugin.LayerR35, stageWindow(cfg.StageModelR35))
	}
	if m.QA != nil {
		m.QA = wrapStage(m.QA, "qa", stageWindow(cfg.StageModelQA))
	}
	if m.Router != nil {
		m.Router = wrapStage(m.Router, "router", stageWindow(cfg.StageModelRouter))
	}
	// Reviewer ReAct 出口仅在有独立覆盖链时另包（否则与 m.React 同源，已包装）
	if m.ReviewerGen != nil && m.ReviewerReact != nil {
		m.ReviewerReact = wrapStage(m.ReviewerReact, "reviewer", stageWindow(cfg.StageModelReviewer))
	}
}

// stageGenerator 阶段覆盖生成链：有全局降级 → Resilient 主备链（与主链共用
// fallback Provider）；否则单模型 Client（挂全局限速器）。
func stageGenerator(p llm.Provider, fallback llm.Provider, so *config.StageOverride,
	cfg *config.Config, limiter ratelimit.Limiter) (llm.Generator, error) {
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
	// v5.5.2：同 buildGenerateChain——单模型 Client 也吃全局韧性参数。
	c.MaxRetries = cfg.LLM.LLMResilienceRetries + 1
	c.BaseDelay = cfg.LLM.LLMResilienceBaseDelay
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
		fm := llm.NewFailoverModel(
			llm.ChainLink{Model: primary, Name: cfg.LLM.Model},
			llm.ChainLink{Model: fallbackBM, Name: cfg.LLM.FallbackModel},
		)
		fm.OnFailover = func(from, to, reason string) {
			log.Warn("argus.llm.failover", "from", from, "to", to,
				"stage", stage, "reason", reason)
			obs.IncrLLMFailover(stage)
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
