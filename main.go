// Argus —— 可插拔多 Agent 审查平台（design.md v3.0）。
// main 只做装配：配置 → 存储 → SDK → 插件 → 编排 → worker → HTTP。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/google/uuid"

	ekitbootstrap "git.example.com/enjoydream/ekit/bootstrap"
	"git.example.com/enjoydream/ekit/concurrency/async"
	ekithertz "git.example.com/enjoydream/ekit/hertz"
	hertzmetrics "git.example.com/enjoydream/ekit/hertz/metrics"
	"git.example.com/enjoydream/ekit/middleware/traceid/hmw"
	"git.example.com/enjoydream/ekit/observability/logx"
	ekitmetrics "git.example.com/enjoydream/ekit/observability/metrics"

	hapi "argus/biz/handler/api"
	rapi "argus/biz/router/api"
	"argus/internal/bootstrap"
	"argus/internal/channel/mcpserver"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/dream"
	"argus/internal/notify"
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poller"
	"argus/internal/poster"
	"argus/internal/progress"
	"argus/internal/runner"
	"argus/internal/service"
	"argus/internal/storage"
	"argus/internal/toolpool"
	"git.example.com/enjoydream/agentkit/audit"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/skill"
	"git.example.com/enjoydream/agentkit/workcopy"
	agentkitworker "git.example.com/enjoydream/agentkit/worker"

	argusworker "argus/internal/worker"

	"golang.org/x/time/rate"
)

// version 由构建时 -ldflags 注入；开发期回退 dev。
var version = "dev"

func main() {
	config.LoadDotEnv()
	cfg := config.Load()

	log := logx.NewSlogLogger("argus")
	obs.Init()
	// ekit async 的 panic 恢复日志注入 logx（裸 goroutine 已全部收编为 async.GoSafe）
	async.SetPanicHandler(func(msg string, stack []byte) {
		log.Error("argus.goroutine_panic", "error", msg, "stack", string(stack))
	})

	if err := cfg.Validate(); err != nil {
		log.Error("argus.boot.config_invalid", "error", err.Error())
		panic(err)
	}
	if cfg.APIToken == "" {
		log.Warn("argus.boot.no_apitoken",
			"hint", "ARGUS_APITOKEN 未配置：🔒 路由（/tasks /agents）将拒绝服务，webhook 不受影响；配置后立即恢复")
	}

	// ---- 生命周期（ekit bootstrap v0.17.2）：SIGINT/SIGTERM → ctx 取消 →
	// 资源逆序关闭（http → worker → store）→ 后台 goroutine 排水（30s 超时）。
	// ShutdownTimeout 须覆盖 http 排水（~5s）+ worker 两阶段停机（60s grace + ≤10s 硬取消）+ 关库。
	if err := ekitbootstrap.Run(ekitbootstrap.Config{
		ServiceName:     "argus",
		ShutdownTimeout: 90 * time.Second,
	}, func(app *ekitbootstrap.App) error {
		return runApp(app, cfg, log)
	}); err != nil {
		panic(err)
	}
}

// runApp 装配全部子系统。资源按 [store, worker, http] 注册 → 逆序关闭：
// HTTP 先停（新任务入口关闭）→ worker 两阶段排水 60s（在途自然完成，超时硬取消
// 保持 running 待下次启动重置）→ 最后关库。
func runApp(app *ekitbootstrap.App, cfg *config.Config, log logx.Logger) error {
	ctx := app.Context()

	// ---- 存储（§5.2：三张表 + 幂等/去重/抢占/staleness）----
	store, err := storage.Open(cfg.DBDSN)
	if err != nil {
		return err
	}
	app.AddResource("store", func(context.Context) error { return store.Close() })
	// claim 归属 + 心跳 + 选主身份（多副本滚动发布安全）。uuid 失败退化为
	// hostname:pid——RunnerID 为空会让心跳续期 no-op，本实例任务 90s 后被
	// reaper 反复重置重跑。
	instanceID := store.RunnerID
	if id, err := uuid.NewRandom(); err == nil {
		instanceID = id.String()[:16]
	} else {
		host, _ := os.Hostname()
		instanceID = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	store.RunnerID = instanceID
	log.Info("argus.boot.instance", "id", instanceID)
	if n, err := store.ResetRunningToPending(ctx, agentkitworker.StaleRunningAfter); err != nil {
		log.Error("argus.boot.reset_running_failed", "error", err.Error())
	} else if n > 0 {
		log.Warn("argus.boot.reset_running", "count", n,
			"stale_after", agentkitworker.StaleRunningAfter.String(),
			"hint", "心跳过期的遗留 running 任务已重置回 pending")
	}

	// ---- L5 上下文：SDK 封装 + 工作副本沙箱 + Skill ----
	sdk, err := argusctx.NewSDKCtx(cfg, log)
	if err != nil {
		panic(err)
	}
	// workcopy 按实例隔离：共享 WorkRoot 时，本实例 Sweep 的孤儿扫描会把
	// 他实例"已跟踪"的目录误判为孤儿删除（entries 是实例内内存态）
	wcRoot := filepath.Join(cfg.WorkRoot, "instance-"+instanceID)
	if err := os.MkdirAll(wcRoot, 0o755); err != nil {
		log.Warn("argus.boot.workroot_failed", "dir", wcRoot, "error", err.Error())
	}
	wc := workcopy.NewPool(wcRoot, log)
	wc.CredentialOf = func(platform string) (string, string, bool) {
		pc, ok := cfg.Platforms[platform]
		// GitHub App 认证时，优先用 installation token（自动刷新）
		if platform == "github" && sdk.GitHubAppToken() != "" {
			return pc.BaseURL, sdk.GitHubAppToken(), ok
		}
		return pc.BaseURL, pc.Token, ok
	}
	wc.InsecureTLSOf = func(platform string) bool {
		return cfg.Platforms[platform].SkipTLS
	}

	skills := skill.NewFileProvider(cfg.SkillsDir)

	// ---- LLM（复用 lacus/.env 的 OPENAI_*；无配置时仅 cli 插件可用）----
	// 配置 OPENAI_FALLBACK_* 四项后启用 Resilient 多模型降级链。
	var chatModel *openai.ChatModel
	var llmGen llm.Generator
	var fallbackProvider llm.Provider       // 全局降级 Provider（P6 阶段覆盖链复用）
	var fallbackBM model.BaseChatModel      // 备模型 BaseChatModel（ReAct Failover 用）
	var wrapReactFailover func(model.BaseChatModel, string) model.BaseChatModel
	if cfg.LLM.BaseURL != "" && cfg.LLM.APIKey != "" && cfg.LLM.Model != "" {
		maxTokens := cfg.MaxTokensParam()
		chatModelCfg := &openai.ChatModelConfig{
			BaseURL: cfg.LLM.BaseURL,
			APIKey:  cfg.LLM.APIKey,
			Model:   cfg.LLM.Model,
			Timeout: cfg.LLM.Timeout,
		}
		if maxTokens > 0 { // 负值 = 不下发 max_tokens（agentkit 语义，推理模型兼容）
			chatModelCfg.MaxTokens = &maxTokens
		}
		chatModel, err = openai.NewChatModel(ctx, chatModelCfg)
		if err != nil {
			panic(fmt.Errorf("构造 LLM 客户端失败: %w", err))
		}

		fallbackReady := cfg.LLM.FallbackBaseURL != "" && cfg.LLM.FallbackAPIKey != "" && cfg.LLM.FallbackModel != ""
		if fallbackReady {
			// 提升作用域：阶段覆盖（P6）构建各自降级链时复用同一 fallback Provider
			primary, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
				BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
				ContextTokens: cfg.LLM.ContextToken, MaxOutputTokens: cfg.MaxTokensParam(),
				Timeout: cfg.LLM.Timeout,
			})
			if e != nil {
				panic(fmt.Errorf("构造主 LLM Provider 失败: %w", e))
			}
			fb, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
				BaseURL: cfg.LLM.FallbackBaseURL, APIKey: cfg.LLM.FallbackAPIKey, Model: cfg.LLM.FallbackModel,
				ContextTokens: cfg.LLM.FallbackContextToken,
			})
			if e != nil {
				panic(fmt.Errorf("构造降级 LLM Provider 失败: %w", e))
			}
			fallbackProvider = fb
			resilient, e := llm.NewResilient(llm.NewFallbackChain(primary, fallbackProvider), llm.ResilientConfig{
				RetriesPerModel: cfg.LLM.LLMResilienceRetries,
				BaseDelay:       cfg.LLM.LLMResilienceBaseDelay,
			})
			if e != nil {
				panic(fmt.Errorf("构造 Resilient 失败: %w", e))
			}
			// 降级事件接结构化日志（运维观测主/备切换频率）
			resilient.OnFallback = func(from, to, stage, reason string) {
				log.Warn("argus.llm.fallback", "from", from, "to", to,
					"stage", stage, "reason", reason)
			}
			llmGen = resilient
			// ReAct 主路径（ADK ChatModelAgent 直调 BaseChatModel）绕过 Resilient——
			// FailoverModel 装饰补齐主备切换（agentkit v0.9.4；Resilient.RawModel 只返回主模型）
			fbModel, e := openai.NewChatModel(ctx, &openai.ChatModelConfig{
				BaseURL: cfg.LLM.FallbackBaseURL,
				APIKey:  cfg.LLM.FallbackAPIKey,
				Model:   cfg.LLM.FallbackModel,
				Timeout: cfg.LLM.Timeout,
			})
			if e != nil {
				panic(fmt.Errorf("构造降级 BaseChatModel 失败: %w", e))
			}
			fallbackBM = fbModel
			wrapReactFailover = func(primary model.BaseChatModel, stage string) model.BaseChatModel {
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
			log.Info("argus.boot.llm_resilient", "chain", resilient.String(),
				"primary_ctx", cfg.LLM.ContextToken, "fallback_ctx", cfg.LLM.FallbackContextToken)
			log.Info("argus.boot.llm_failover", "react", "enabled", "fallback_model", cfg.LLM.FallbackModel)
		} else {
			c := llm.NewClient(chatModel, cfg.LLM.Model, nil)
			c.ContextTokens = cfg.LLM.ContextToken
			c.MaxOutputTokens = cfg.MaxTokensResolved()
			llmGen = c
		}
		log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model, "base_url", cfg.LLM.BaseURL,
			"context_tokens", cfg.LLM.ContextToken, "max_tokens", maxTokens,
			"diff_chars", cfg.MaxDiffCharsResolved(), "task_budget", cfg.EffectiveTaskTokenBudget())
	} else {
		log.Warn("argus.boot.no_llm", "hint", "OPENAI_* 未配置，builtin 插件不可用（仅 cli 插件可工作）")
	}

	// ---- L5 知识子系统：Milvus 向量检索 > 本地 RAG > Noop ----
	var knowledge rag.KnowledgeService = rag.NewNoop()
	if cfg.MilvusAddr != "" && cfg.EmbeddingBaseURL != "" {
		// Milvus 向量检索模式
		embedder := rag.NewOpenAIEmbedder(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim)
		milvusCfg := rag.MilvusConfig{
			Address:    cfg.MilvusAddr,
			Collection: cfg.MilvusCollection,
			Dimension:  cfg.EmbeddingDim,
		}
		if k, e := rag.NewMilvusStore(ctx, milvusCfg, embedder); e == nil {
			knowledge = k
			log.Info("argus.boot.milvus_ready", "addr", cfg.MilvusAddr, "collection", cfg.MilvusCollection,
				"embedding", cfg.EmbeddingModel, "dim", cfg.EmbeddingDim)
			// 启动时索引 knowledge 目录到 Milvus
			if fi, err := os.Stat(cfg.KnowledgeDir); err == nil && fi.IsDir() {
				indexKnowledgeDir(ctx, k, cfg.KnowledgeDir, log)
			}
		} else {
			log.Warn("argus.boot.milvus_failed", "addr", cfg.MilvusAddr, "error", e.Error(),
				"fallback", "本地 RAG")
			// 回退到本地 RAG
			knowledge = fallbackLocalRAG(cfg.KnowledgeDir, log)
		}
	} else {
		// 本地 RAG 模式
		knowledge = fallbackLocalRAG(cfg.KnowledgeDir, log)
	}

	// ---- L1 扩展：进度事件总线 + octopus IM 推送（v3.1）----
	bus := progress.NewBus()
	var imNotify *notify.Notifier
	if cfg.OctopusURL != "" {
		imNotify = &notify.Notifier{
			BaseURL: cfg.OctopusURL, ChannelID: cfg.OctopusChannelID,
			User: cfg.OctopusUser, Token: cfg.OctopusToken, Log: log,
		}
		log.Info("argus.boot.notify_ready", "octopus", cfg.OctopusURL, "channel", cfg.OctopusChannelID)
	}

	// ---- LLM 全局限速器（token bucket；ARGUS_LLM_RATE_LIMIT > 0 时启用）----
	var llmLimiter *rate.Limiter
	if cfg.LLMRateLimit > 0 {
		llmLimiter = rate.NewLimiter(rate.Limit(cfg.LLMRateLimit), cfg.LLMRateBurst)
		log.Info("argus.boot.llm_rate_limiter", "rps", cfg.LLMRateLimit, "burst", cfg.LLMRateBurst)
	}

	// ---- P6 分阶段模型路由：ARGUS_MODEL_{R1,R2,R3,R35,REVIEWER,QA,ROUTER} 覆盖 ----
	// 覆盖阶段继承主 BaseURL/APIKey；配置了全局降级时各自构建降级链（fallback 模型不覆盖）。
	// R1/R2 经 StageRouter 进 workflow；R3/R35/Router/Reviewer/QA 是独立模型出口，走专用字段。
	var (
		stageLLMs    map[string]llm.Generator
		r3Model      model.BaseChatModel
		reviewerLLM  llm.Generator
		reviewerName string
		qaModel      model.BaseChatModel
		reflectModel model.BaseChatModel // R3.5 报告反思（v3.4；nil = runner 侧回退 R3 模型）
		routerModel  model.BaseChatModel // 评论意图路由分类器（v3.4；nil = 用 QA/主模型）
	)
	buildStageModel := func(so *config.StageOverride) (model.BaseChatModel, error) {
		ctxTok := so.ContextTokens
		if ctxTok <= 0 {
			ctxTok = cfg.LLM.ContextToken
		}
		var maxTok *int
		if mt := cfg.MaxTokensParam(); mt > 0 {
			t := mt
			maxTok = &t
		}
		return openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			MaxTokens: maxTok, Timeout: cfg.LLM.Timeout,
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
		if fallbackProvider != nil {
			return llm.NewResilient(llm.NewFallbackChain(p, fallbackProvider), llm.ResilientConfig{
				RetriesPerModel: cfg.LLM.LLMResilienceRetries, BaseDelay: cfg.LLM.LLMResilienceBaseDelay,
			})
		}
		c := llm.NewClient(p.Model(), so.Model, nil)
		c.ContextTokens = so.ContextTokens
		if c.ContextTokens <= 0 {
			c.ContextTokens = cfg.LLM.ContextToken
		}
		c.MaxOutputTokens = cfg.MaxTokensResolved()
		c.Limiter = llmLimiter
		return c, nil
	}
	if cfg.LLM.BaseURL != "" {
		for stage, so := range map[string]*config.StageOverride{
			"R1": cfg.StageModelR1, "R2": cfg.StageModelR2,
		} {
			if so == nil {
				continue
			}
			if stageLLMs == nil {
				stageLLMs = map[string]llm.Generator{}
			}
			g, err := buildStageGen(so)
			if err != nil {
				panic(fmt.Errorf("构造 %s 阶段模型失败: %w", stage, err))
			}
			stageLLMs[stage] = g
			log.Info("argus.boot.stage_model", "stage", stage, "model", so.Model)
		}
		if so := cfg.StageModelR3; so != nil {
			m, err := buildStageModel(so)
			if err != nil {
				panic(fmt.Errorf("构造 R3 模型失败: %w", err))
			}
			r3Model = m
			log.Info("argus.boot.stage_model", "stage", "R3(ReAct)", "model", so.Model)
		}
		if so := cfg.StageModelR35; so != nil {
			m, err := buildStageModel(so)
			if err != nil {
				panic(fmt.Errorf("构造 R3.5 模型失败: %w", err))
			}
			reflectModel = m
			log.Info("argus.boot.stage_model", "stage", "R35(reflection)", "model", so.Model)
		}
		if so := cfg.StageModelRouter; so != nil {
			m, err := buildStageModel(so)
			if err != nil {
				panic(fmt.Errorf("构造 Router 模型失败: %w", err))
			}
			routerModel = m
			log.Info("argus.boot.stage_model", "stage", "router", "model", so.Model)
		}
		if so := cfg.StageModelReviewer; so != nil {
			g, err := buildStageGen(so)
			if err != nil {
				panic(fmt.Errorf("构造 Reviewer 模型失败: %w", err))
			}
			reviewerLLM = g
			reviewerName = so.Model
			log.Info("argus.boot.stage_model", "stage", "reviewer", "model", so.Model)
		}
		if so := cfg.StageModelQA; so != nil {
			m, err := buildStageModel(so)
			if err != nil {
				panic(fmt.Errorf("构造 QA 模型失败: %w", err))
			}
			qaModel = m
			log.Info("argus.boot.stage_model", "stage", "qa", "model", so.Model)
		}
	}

	// ReAct / 单轮直调路径统一包 FailoverModel（主模型故障切备；Generator 路径仍走 Resilient）。
	// chatModel 类型保持 *openai.ChatModel 供 SDK/非 ReAct 场景；ReAct 用 reactChatModel。
	var reactChatModel model.BaseChatModel
	if chatModel != nil {
		reactChatModel = chatModel
	}
	if wrapReactFailover != nil {
		if reactChatModel != nil {
			reactChatModel = wrapReactFailover(reactChatModel, "chat")
		}
		if r3Model != nil {
			r3Model = wrapReactFailover(r3Model, "R3")
		}
		if qaModel != nil {
			qaModel = wrapReactFailover(qaModel, "qa")
		}
		if routerModel != nil {
			routerModel = wrapReactFailover(routerModel, "router")
		}
		if reflectModel != nil {
			reflectModel = wrapReactFailover(reflectModel, "R35")
		}
	}

	// ---- L4 插拔层 + L3 编排 + runner ----
	// runnerRef 延迟填充：requirement-compliance 插件的 factory 在 Invoke 时才解引用。
	var runnerRef *runner.Runner
	// Reviewer ReAct 模型：有阶段覆盖时用 Resilient.RawModel（需 Failover 包）；
	// 否则直接用 reactChatModel（已 Failover）。
	reviewerReactModel := reactChatModel
	if reviewerLLM != nil {
		if rm, ok := reviewerLLM.(interface{ RawModel() model.BaseChatModel }); ok {
			if raw := rm.RawModel(); raw != nil {
				reviewerReactModel = raw
				if wrapReactFailover != nil {
					reviewerReactModel = wrapReactFailover(raw, "reviewer")
				}
			}
		}
	}
	reg, complianceSkillRefs, err := bootstrap.BuildRegistry(&bootstrap.Deps{
		Cfg: cfg, Log: log, WC: wc, Model: reactChatModel, Skills: skills,
		WorkflowFactory: func(budget *plugin.TokenBudget) adapter_builtin.WorkflowRunner {
			if runnerRef == nil {
				return adapter_builtin.WorkflowRunner(nil) // 装配完成前不该被调用；调用方 panic=明确报错
			}
			return runnerRef.WorkflowFactory()(budget)
		},
		// R4 builtin ReAct 化：Reviewer 复用 R3 工具基础设施（get_file/get_diff/
		// 知识/技能/MCP），失败自动落回单轮调用
		ReviewTools: func(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) ([]tool.BaseTool, string, error) {
			if runnerRef == nil {
				return nil, "", fmt.Errorf("runner 未就绪")
			}
			return runnerRef.WorkflowFor(budget).ReviewTools(ctx, task)
		},
		ReviewerLLM:  reviewerLLM,
		ReviewerName: reviewerName,
		ReactModel:   reviewerReactModel,
	})
	if err != nil {
		panic(err)
	}

	// ---- L5 工具子系统：MCP server → eino 工具表（design §9.2 方向 A 实装）----
	// plugins.yaml 的 mcpServers/tools 段驱动；未声明时 nil（workflow 不挂工具）。
	var toolPool toolpool.Pool
	var toolSpecs []toolpool.ToolSpec
	var squads []plugin.SquadDecl
	if pf, e := plugin.LoadPluginsFile(cfg.PluginsFile); e == nil {
		if len(pf.MCPServers) > 0 {
			if rp := toolpool.NewRealPool(pf.MCPServers, log); rp != nil {
				toolPool = &toolpool.RealAdapter{Pool: rp}
				toolSpecs = toolpool.SpecsFromDecls(pf.Tools)
				log.Info("argus.boot.mcp_ready", "servers", strings.Join(rp.Servers(), ","),
					"tool_specs", len(toolSpecs))
			}
		}
		squads = pf.Squads
	}

	// ---- LLM 成本记账（obsx OnUsage 回流，覆盖直连与 ReAct RawModel 两条路径）----
	costTracker := llm.NewCostTracker()
	costRates := cfg.CostPer1K()
	costPer1K := func(mdl string) [2]float64 {
		if mdl == cfg.LLM.Model || mdl == cfg.LLM.FallbackModel {
			return costRates // 备用模型按同一费率近似（百炼兼容端点同价）
		}
		return [2]float64{}
	}

	// ---- 多副本控制面选主（agentkit/worker.LeaderElector）----
	// 任务面（worker pool）靠 ClaimNextPending 原子抢占天然分片，无需选主；
	// 轮询器是"只能跑一份"的控制面组件——租约收敛，失联 ≤ttl 自动换主。
	pollElector := agentkitworker.NewLeaderElector(store, "argus/poller", instanceID,
		30*time.Second, 10*time.Second, agentkitworker.WithLeaderLogger(log))
	pollElector.Start(ctx)
	app.AddResource("poll-leader", func(context.Context) error {
		pollElector.Stop() // 优雅停机主动让位，缩短换主窗口
		return nil
	})

	dispatcher := plugin.NewDispatcher(reg, log).WithSquads(squads)
	merger := plugin.NewMerger(store, log)
	if cfg.RegretLearning {
		merger.WithLearning(store, plugin.LearningConfig{
			Enabled:     true,
			MinSamples:  cfg.RegretMinSamples,
			SoftFloor:   cfg.RegretSoftFloor,
			HardFloor:   cfg.RegretHardFloor,
			InjectFloor: cfg.RegretInjectFloor,
			InjectTopN:  cfg.RegretInjectTopN,
		})
		log.Info("argus.regret_learning.enabled",
			"min_samples", cfg.RegretMinSamples,
			"soft_floor", cfg.RegretSoftFloor,
			"hard_floor", cfg.RegretHardFloor)
	}
	posterSvc := &poster.Poster{SDK: sdk, Store: store, Version: version, BotUsername: cfg.BotUsername, Log: log}
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdk,
		Model: reactChatModel, ModelName: cfg.LLM.Model,
		LLM:        llmGen,
		ToolPool:   toolPool,
		ToolSpecs:  toolSpecs,
		Dispatcher: dispatcher, Merger: merger,
		Poster: posterSvc, Log: log, Version: version,
		SkillProvider: skills, SkillRefs: complianceSkillRefs,
		Bus:        bus,
		Notify:     imNotify,
		Knowledge:  knowledge,
		LLMLimiter: llmLimiter,
		Cost:       costTracker,
		CostPer1K:  costPer1K,
		StageLLMs:  stageLLMs,
		R3Model:    r3Model,
		// ReAct failover：R3Model 已包时用它；nil 时 runner 用 RawModel + FallbackModel 再包
		FallbackModel: fallbackBM,
		FallbackName:  cfg.LLM.FallbackModel,
		// R3.5 报告反思（v3.4 reflection）：轮数 0 = 关闭；模型缺省回退 R3 专用模型
		ReflectRounds:   cfg.ReflectRounds,
		ReflectModel:    reflectModel,
		ReflectEvidence: cfg.ReflectEvidence,
		// 模式级后悔学习环（v3.5）：默认关闭
		Learning: plugin.LearningConfig{
			Enabled:     cfg.RegretLearning,
			MinSamples:  cfg.RegretMinSamples,
			SoftFloor:   cfg.RegretSoftFloor,
			HardFloor:   cfg.RegretHardFloor,
			InjectFloor: cfg.RegretInjectFloor,
			InjectTopN:  cfg.RegretInjectTopN,
		},
	}

	// ---- worker pool（§10.4 DB 即队列；两阶段优雅停机）----
	wp := &agentkitworker.Pool{Queue: &argusworker.StoreQueue{Store: store}, N: cfg.MaxConcurrentTasks, Log: log, Run: runnerRef.Execute}
	wp.Start(ctx)
	// 注册序 [store, worker, http] → 关闭逆序：worker 在 HTTP 停止后排水 60s，store 最后关
	app.AddResource("worker", func(context.Context) error {
		wp.Stop(60 * time.Second)
		return nil
	})

	// ---- 运维加固：任务保留清理 + 队列深度采样（WorkerManager 管理生命周期）----
	// ctx 取消时所有 ticker 自动退出（cancel 先于 close），无需显式 Close
	wm := ekitbootstrap.NewWorkerManager(ctx)
	wm.AddTicker("workcopy-scavenger", time.Hour, func(context.Context) { wc.Sweep(time.Hour) })
	if cfg.TaskRetentionDays > 0 {
		retention := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		pruneOnce := func(ctx context.Context) {
			if n, err := store.PruneTasks(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("argus.boot.prune_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned", "removed", n, "retention_days", cfg.TaskRetentionDays)
			}
		}
		pruneOnce(ctx)
		wm.AddTicker("task-prune", 24*time.Hour, pruneOnce)
	}
	wm.AddTicker("queue-depth", 30*time.Second, func(ctx context.Context) {
		if pending, running, err := store.TaskCounts(ctx); err == nil {
			obs.SetTaskQueueDepth(pending, running)
		}
	})
	// ---- dream 长期记忆整合（v3.5.1）：去重 pattern_signals + 导出低置信模式到知识库 ----
	// 仅在学习环开启且显式配置周期时启动（默认 0=关闭）。
	if cfg.RegretLearning && cfg.DreamInterval > 0 {
		dm := &dream.Dream{
			Store:        store,
			Knowledge:    knowledge,
			KnowledgeDir: cfg.KnowledgeDir,
			Learning: plugin.LearningConfig{
				Enabled:     true,
				MinSamples:  cfg.RegretMinSamples,
				SoftFloor:   cfg.RegretSoftFloor,
				HardFloor:   cfg.RegretHardFloor,
				InjectFloor: cfg.RegretInjectFloor,
				InjectTopN:  cfg.RegretInjectTopN,
			},
			ExportTopN: cfg.RegretInjectTopN,
			Log:        log,
		}
		wm.AddTicker("dream", cfg.DreamInterval, func(ctx context.Context) {
			_, _, err := dm.RunOnce(ctx)
			if err != nil {
				log.Warn("argus.dream.run_failed", "error", err.Error())
			}
		})
		log.Info("argus.dream.enabled", "interval", cfg.DreamInterval.String())
	}

	// ---- 轮询兜底任务源（v3.0.1 改造：平台入站 webhook 不可达环境）----
	if cfg.PollInterval > 0 && cfg.PollRepos != "" {
		targets, bad := poller.ParseTargets(cfg.PollRepos)
		for _, b := range bad {
			log.Warn("argus.poller.bad_target", "item", b, "format", "platform:owner/repo")
		}
		if len(targets) > 0 {
			pl := &poller.Poller{SDK: sdk, Store: store, Targets: targets,
				Interval: cfg.PollInterval, SkipDraft: cfg.SkipDraft, Log: log, Bus: bus,
				IsLeader: pollElector.IsLeader}
			pl.Start(ctx)
		}
	}

	// ---- service + biz 接线 ----
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sdk, Registry: reg, Knowledge: knowledge,
		Log: log, Bus: bus, Model: reactChatModel, QAModel: qaModel,
		RouterModel: routerModel,
		Audit: audit.New(log, "argus-audit"), Version: version}
	hapi.SetService(svc)
	rapi.SetAuth = svc.BearerMiddleware()

	// ---- HTTP（ARGUS_ADDR 默认 :8899）----
	// ekit hlog 桥接：Hertz 框架内部日志（连接异常、路由警告等）统一走 logx。
	// ⚠️ 必须传 slog 实例（NewSlogLogger），不能传 logx.WithService(...)——后者走
	// logx 默认 hlog 后端，装回 hlog 后形成 adapter↔logx 无限递归（实测主 goroutine
	// 卡死 + 100% CPU + SIGTERM 无响应，ekit AGENTS.md 的示例即此雷）。
	hlog.SetLogger(ekithertz.NewLogger(logx.NewSlogLogger("argus-http")))
	h := server.Default(
		server.WithHostPorts(cfg.Addr),
		server.WithMaxRequestBodySize(cfg.WebhookMaxBody),
	)
	// ekit traceid：生成/透传 X-Trace-Id + 注入 RequestContext 与标准 context；
	// mw 响应壳与 logx.Ctx* 日志的 trace_id 皆源于此
	h.Use(hmw.Middleware()...)
	registerCustom(h, store, sdk, cfg, log, svc)
	// h.Run() 进 bootstrap 后台（关闭阶段先 h.Shutdown 排水，h.Run 退出由 wait 阶段收割）
	ekithertz.RunServer(app, "http", h)
	log.Info("argus.boot.http_ready", "addr", cfg.Addr, "version", version)
	// bootstrap 语义：userFn 返回 = 主动关机。此处阻塞直到停机第一阶段取消 ctx，
	// 否则进程装配完成即退出（SIGTERM 由 bootstrap 的 signal 监听承接）。
	<-ctx.Done()
	return nil
}

// register 自定义路由：健康检查 / 指标 / MCP+A2A 占位（§5 路由表 v3.0 部分 + §9.2 方向 B 占位）。
func registerCustom(h *server.Hertz, store *storage.Store, sdk *argusctx.SDKCtx, cfg *config.Config, log logx.Logger, svc *service.Service) {
	register(h) // hz 生成路由（IDL 声明的 /webhook /tasks /agents + /ping）

	// 存活（§10.1：hertz 无默认 healthz，自写一行）
	h.GET("/healthz", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok", "version": version})
	})

	// 就绪：DB 连通 + LLM 端点可达（未配置 LLM 时跳过该检查）
	h.GET("/readyz", func(ctx context.Context, c *app.RequestContext) {
		if err := store.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, map[string]any{"status": "db unavailable", "error": err.Error()})
			return
		}
		if cfg.LLM.BaseURL != "" {
			if !llmReachableCached(cfg.LLM.BaseURL) {
				c.JSON(http.StatusServiceUnavailable, map[string]any{"status": "llm unreachable", "base_url": cfg.LLM.BaseURL})
				return
			}
		}
		c.JSON(http.StatusOK, map[string]any{"status": "ready", "platforms": sdk.Ready()})
	})

	// Prometheus（ekit：挂 metrics.Registry——obs.Init 已补齐 go/process 收集器；
	// MetricsHandlerFor 内部完成 RequestContext→http.ResponseWriter 适配）
	h.GET("/metrics", hertzmetrics.MetricsHandlerFor(ekitmetrics.Registry))

	// MCP Server（v3.1 方向 B 实装）：streamable http + Bearer 鉴权
	mcpBridge := mcpserver.HertzBridge(mcpserver.New(svc, version))
	mcpHandlers := []app.HandlerFunc{svc.BearerMiddleware(), mcpBridge}
	h.GET("/mcp", mcpHandlers...)
	h.POST("/mcp", mcpHandlers...)
	h.DELETE("/mcp", mcpHandlers...)
	h.GET("/.well-known/agent.json", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, mcpserver.AgentCardPlaceholder("argus", version))
	})
	// register(h) 开头已包含 customizedRegister 的 /ping，勿重复注册
	//（http_ready 日志在 runApp 的 RunServer 之后统一打）
}

// llmReachableClient readyz 探针复用的 HTTP 客户端（避免每次 new transport 泄漏）。
var llmReachableClient = &http.Client{Timeout: 3 * time.Second}

var (
	llmReachableMu   sync.Mutex
	llmReachableOk   bool
	llmReachableTime time.Time
)

// llmReachableCached LLM 端点连通性（readyz 用；30s TTL 缓存，避免每 10s 真实请求 LLM 浪费配额）。
// mutex 保护 read-check-write 原子性，防止并发探针写入陈旧结果。
func llmReachableCached(baseURL string) bool {
	llmReachableMu.Lock()
	defer llmReachableMu.Unlock()
	if time.Since(llmReachableTime) < 30*time.Second {
		return llmReachableOk
	}
	ok := llmReachable(baseURL)
	llmReachableOk = ok
	llmReachableTime = time.Now()
	return ok
}

// llmReachable LLM 端点连通性（任意 HTTP 响应均视为可达）。
func llmReachable(baseURL string) bool {
	resp, err := llmReachableClient.Get(baseURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return true
}

// fallbackLocalRAG 本地 RAG 回退（目录缺失回退 Noop）。
func fallbackLocalRAG(dir string, log logx.Logger) rag.KnowledgeService {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if k, e := rag.NewLocal(dir); e == nil {
			log.Info("argus.boot.knowledge_ready", "dir", dir)
			return k
		} else {
			log.Warn("argus.boot.knowledge_failed", "dir", dir, "error", e.Error())
		}
	} else {
		log.Warn("argus.boot.knowledge_absent", "dir", dir,
			"hint", "创建目录并放入 markdown 知识文件即启用 search_knowledge")
	}
	return rag.NewNoop()
}

// indexKnowledgeDir 将知识库目录中的 markdown 文件索引到 Milvus。
func indexKnowledgeDir(ctx context.Context, store *rag.MilvusStore, dir string, log logx.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("argus.boot.index_dir_failed", "dir", dir, "error", err.Error())
		return
	}
	indexed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn("argus.boot.index_file_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		if err := store.Index(ctx, entry.Name(), string(data)); err != nil {
			log.Warn("argus.boot.index_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		indexed++
	}
	log.Info("argus.boot.knowledge_indexed", "dir", dir, "files", indexed)
}
