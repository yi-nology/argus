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
	"git.example.com/enjoydream/agentkit/audit"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/workcopy"
	"argus/internal/bootstrap"
	"argus/internal/channel/mcpserver"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/skill"
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
	// claim 归属 + 心跳（多副本滚动发布安全）。uuid 失败退化为 hostname:pid——
	// RunnerID 为空会让心跳续期 no-op，本实例任务 90s 后被 reaper 反复重置重跑。
	if id, err := uuid.NewRandom(); err == nil {
		store.RunnerID = id.String()[:16]
	} else {
		host, _ := os.Hostname()
		store.RunnerID = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
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
	if err := os.MkdirAll(cfg.WorkRoot, 0o755); err != nil {
		log.Warn("argus.boot.workroot_failed", "dir", cfg.WorkRoot, "error", err.Error())
	}
	wc := workcopy.NewPool(cfg.WorkRoot, log)
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
	if cfg.LLM.BaseURL != "" && cfg.LLM.APIKey != "" && cfg.LLM.Model != "" {
		maxTokens := cfg.MaxTokensResolved()
		chatModel, err = openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL:   cfg.LLM.BaseURL,
			APIKey:    cfg.LLM.APIKey,
			Model:     cfg.LLM.Model,
			MaxTokens: &maxTokens,
			Timeout:   cfg.LLM.Timeout,
		})
		if err != nil {
			panic(fmt.Errorf("构造 LLM 客户端失败: %w", err))
		}

		fallbackReady := cfg.LLM.FallbackBaseURL != "" && cfg.LLM.FallbackAPIKey != "" && cfg.LLM.FallbackModel != ""
		if fallbackReady {
			primary, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
				BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
				ContextTokens: cfg.LLM.ContextToken, MaxOutputTokens: cfg.MaxTokensResolved(),
				Timeout: cfg.LLM.Timeout,
			})
			if e != nil {
				panic(fmt.Errorf("构造主 LLM Provider 失败: %w", e))
			}
			fallback, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
				BaseURL: cfg.LLM.FallbackBaseURL, APIKey: cfg.LLM.FallbackAPIKey, Model: cfg.LLM.FallbackModel,
				ContextTokens: cfg.LLM.FallbackContextToken,
			})
			if e != nil {
				panic(fmt.Errorf("构造降级 LLM Provider 失败: %w", e))
			}
			resilient, e := llm.NewResilient(llm.NewFallbackChain(primary, fallback), llm.ResilientConfig{})
			if e != nil {
				panic(fmt.Errorf("构造 Resilient 失败: %w", e))
			}
			llmGen = resilient
			log.Info("argus.boot.llm_resilient", "chain", resilient.String(),
				"primary_ctx", cfg.LLM.ContextToken, "fallback_ctx", cfg.LLM.FallbackContextToken)
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

	// ---- L4 插拔层 + L3 编排 + runner ----
	// runnerRef 延迟填充：requirement-compliance 插件的 factory 在 Invoke 时才解引用。
	var runnerRef *runner.Runner
	reg, complianceSkillRefs, err := bootstrap.BuildRegistry(&bootstrap.Deps{
		Cfg: cfg, Log: log, WC: wc, Model: chatModel, Skills: skills,
		WorkflowFactory: func(budget *plugin.TokenBudget) adapter_builtin.WorkflowRunner {
			if runnerRef == nil {
				return adapter_builtin.WorkflowRunner(nil) // 装配完成前不该被调用；调用方 panic=明确报错
			}
			return runnerRef.WorkflowFactory()(budget)
		},
	})
	if err != nil {
		panic(err)
	}

	// ---- LLM 全局限速器（token bucket；ARGUS_LLM_RATE_LIMIT > 0 时启用）----
	var llmLimiter *rate.Limiter
	if cfg.LLMRateLimit > 0 {
		llmLimiter = rate.NewLimiter(rate.Limit(cfg.LLMRateLimit), cfg.LLMRateBurst)
		log.Info("argus.boot.llm_rate_limiter", "rps", cfg.LLMRateLimit, "burst", cfg.LLMRateBurst)
	}

	dispatcher := plugin.NewDispatcher(reg, log)
	merger := plugin.NewMerger(store, log)
	posterSvc := &poster.Poster{SDK: sdk, Store: store, Version: version, BotUsername: cfg.BotUsername, Log: log}
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdk,
		Model: chatModel, ModelName: cfg.LLM.Model,
		LLM:        llmGen,
		Dispatcher: dispatcher, Merger: merger,
		Poster: posterSvc, Log: log, Version: version,
		SkillProvider: skills, SkillRefs: complianceSkillRefs,
		Bus:        bus,
		Notify:     imNotify,
		Knowledge:  knowledge,
		LLMLimiter: llmLimiter,
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

	// ---- 轮询兜底任务源（v3.0.1 改造：平台入站 webhook 不可达环境）----
	if cfg.PollInterval > 0 && cfg.PollRepos != "" {
		targets, bad := poller.ParseTargets(cfg.PollRepos)
		for _, b := range bad {
			log.Warn("argus.poller.bad_target", "item", b, "format", "platform:owner/repo")
		}
		if len(targets) > 0 {
			pl := &poller.Poller{SDK: sdk, Store: store, Targets: targets,
				Interval: cfg.PollInterval, SkipDraft: cfg.SkipDraft, Log: log, Bus: bus}
			pl.Start(ctx)
		}
	}

	// ---- service + biz 接线 ----
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sdk, Registry: reg,
		Log: log, Bus: bus, Model: chatModel,
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
