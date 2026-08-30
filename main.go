// Argus —— 可插拔多 Agent 审查平台（design.md v3.0）。
// main 只做装配：配置 → 存储 → SDK → 插件 → 编排 → worker → HTTP。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/prometheus/common/expfmt"

	"git.example.com/enjoydream/ekit/observability/logx"
	ekitmetrics "git.example.com/enjoydream/ekit/observability/metrics"

	hapi "argus/biz/handler/api"
	rapi "argus/biz/router/api"
	"argus/internal/bootstrap"
	"argus/internal/channel/mcpserver"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/knowledge/skill"
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poller"
	"argus/internal/poster"
	"argus/internal/runner"
	"argus/internal/service"
	"argus/internal/storage"
	"argus/internal/worker"
	"argus/internal/workflow/requirement"
)

// version 报告头与元信息里的服务版本。
const version = "v3.0.0"

func main() {
	config.LoadDotEnv()
	cfg := config.Load()

	log := logx.NewSlogLogger("argus")
	obs.Init()

	if err := cfg.Validate(); err != nil {
		log.Error("argus.boot.config_invalid", "error", err.Error())
		panic(err)
	}
	if cfg.APIToken == "" {
		log.Warn("argus.boot.no_apitoken",
			"hint", "ARGUS_APITOKEN 未配置：🔒 路由（/tasks /agents）将拒绝服务，webhook 不受影响；配置后立即恢复")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- 存储（§5.2：三张表 + 幂等/去重/抢占/staleness）----
	store, err := storage.Open(cfg.DBDSN)
	if err != nil {
		panic(err)
	}
	defer store.Close()
	if n, err := store.ResetRunningToPending(ctx); err != nil {
		log.Error("argus.boot.reset_running_failed", "error", err.Error())
	} else if n > 0 {
		log.Warn("argus.boot.reset_running", "count", n,
			"hint", "上次崩溃遗留的 running 任务已重置回 pending")
	}

	// ---- L5 上下文：SDK 封装 + 工作副本沙箱 + Skill ----
	sdk, err := argusctx.NewSDKCtx(cfg, log)
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(cfg.WorkRoot, 0o755); err != nil {
		log.Warn("argus.boot.workroot_failed", "dir", cfg.WorkRoot, "error", err.Error())
	}
	wc := argusctx.NewWorkcopy(cfg.WorkRoot, log)
	wc.CredentialOf = func(platform string) (string, string, bool) {
		pc, ok := cfg.Platforms[platform]
		return pc.BaseURL, pc.Token, ok
	}
	wc.InsecureTLSOf = func(platform string) bool {
		return cfg.Platforms[platform].SkipTLS
	}
	wc.StartTTLScavenger(ctx, time.Hour)

	skills := skill.NewFileProvider(cfg.SkillsDir)

	// ---- LLM（复用 lacus/.env 的 OPENAI_*；无配置时仅 cli 插件可用）----
	var chatModel *openai.ChatModel
	if cfg.LLM.BaseURL != "" && cfg.LLM.APIKey != "" && cfg.LLM.Model != "" {
		maxTokens := cfg.LLM.MaxTokens
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
		log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model, "base_url", cfg.LLM.BaseURL)
	} else {
		log.Warn("argus.boot.no_llm", "hint", "OPENAI_* 未配置，builtin 插件不可用（仅 cli 插件可工作）")
	}

	// ---- L4 插拔层 + L3 编排 + runner ----
	// runnerRef 延迟填充：requirement-compliance 插件的 factory 在 Invoke 时才解引用。
	var runnerRef *runner.Runner
	reg, complianceSkillRefs, err := bootstrap.BuildRegistry(&bootstrap.Deps{
		Cfg: cfg, Log: log, WC: wc, Model: chatModel, Skills: skills,
		WorkflowFactory: func(budget *plugin.TokenBudget) adapter_builtin.WorkflowRunner {
			if runnerRef == nil {
				return runnerFunc(nilRunner)
			}
			return runnerRef.WorkflowFactory()(budget)
		},
	})
	if err != nil {
		panic(err)
	}

	dispatcher := plugin.NewDispatcher(reg, log)
	merger := plugin.NewMerger(store, log)
	posterSvc := &poster.Poster{SDK: sdk, Store: store, Version: version, Log: log}
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdk,
		Model: chatModel, ModelName: cfg.LLM.Model,
		Dispatcher: dispatcher, Merger: merger,
		Poster: posterSvc, Log: log, Version: version,
		SkillProvider: skills, SkillRefs: complianceSkillRefs,
	}

	// ---- worker pool（§10.4 DB 即队列；两阶段优雅停机）----
	wp := &worker.Pool{Store: store, N: cfg.MaxConcurrentTasks, Log: log, Run: runnerRef.Execute}
	wp.Start(ctx)

	// ---- 运维加固：任务保留清理 + 队列深度采样 ----
	if cfg.TaskRetentionDays > 0 {
		retention := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		pruneOnce := func() {
			if n, err := store.PruneTasks(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("argus.boot.prune_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned", "removed", n, "retention_days", cfg.TaskRetentionDays)
			}
		}
		pruneOnce()
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					pruneOnce()
				}
			}
		}()
	}
	go func() { // 队列深度 gauge（30s 采样；argus_tasks_pending/running）
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if pending, running, err := store.TaskCounts(ctx); err == nil {
					obs.SetTaskQueueDepth(pending, running)
				}
			}
		}
	}()

	// ---- 轮询兜底任务源（v3.0.1 改造：平台入站 webhook 不可达环境）----
	if cfg.PollInterval > 0 && cfg.PollRepos != "" {
		targets, bad := poller.ParseTargets(cfg.PollRepos)
		for _, b := range bad {
			log.Warn("argus.poller.bad_target", "item", b, "format", "platform:owner/repo")
		}
		if len(targets) > 0 {
			pl := &poller.Poller{SDK: sdk, Store: store, Targets: targets,
				Interval: cfg.PollInterval, Log: log}
			pl.Start(ctx)
		}
	}

	// ---- service + biz 接线 ----
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sdk, Registry: reg,
		Log: log, Version: version}
	hapi.SetService(svc)
	rapi.SetAuth = svc.BearerMiddleware()

	// ---- HTTP（ARGUS_ADDR 默认 :8899）----
	h := server.Default(server.WithHostPorts(cfg.Addr))
	registerCustom(h, store, sdk, cfg, log)
	h.Spin()
	// 优雅停机：HTTP 已停 → worker 停止领取并给在途任务 60s 自然完成；
	// 超时才硬取消（任务保持 running，下次启动重置回 pending）。
	wp.Stop(60 * time.Second)
	cancel()
}

// nilRunner factory 在装配完成前被调用的防御位（正常不会发生）。
func nilRunner(_ context.Context, _ plugin.Task, _ *plugin.TokenBudget) (*requirement.Output, error) {
	return nil, fmt.Errorf("runner 尚未装配完成")
}

var _ adapter_builtin.WorkflowRunner = runnerFunc(nilRunner)

// runnerFunc 函数适配 WorkflowRunner。
type runnerFunc func(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*requirement.Output, error)

func (f runnerFunc) Run(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) (*requirement.Output, error) {
	return f(ctx, task, budget)
}

// register 自定义路由：健康检查 / 指标 / MCP+A2A 占位（§5 路由表 v3.0 部分 + §9.2 方向 B 占位）。
func registerCustom(h *server.Hertz, store *storage.Store, sdk *argusctx.SDKCtx, cfg *config.Config, log logx.Logger) {
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
			if !llmReachable(cfg.LLM.BaseURL) {
				c.JSON(http.StatusServiceUnavailable, map[string]any{"status": "llm unreachable", "base_url": cfg.LLM.BaseURL})
				return
			}
		}
		c.JSON(http.StatusOK, map[string]any{"status": "ready", "platforms": sdk.Ready()})
	})

	// Prometheus（挂 ekit Registry，含 argus_* 与 go/process 收集器）
	h.GET("/metrics", func(ctx context.Context, c *app.RequestContext) {
		c.SetContentType(string(expfmt.FmtText))
		mfs, err := ekitmetrics.Registry.Gather()
		if err != nil {
			c.SetStatusCode(http.StatusInternalServerError)
			return
		}
		enc := expfmt.NewEncoder(c, expfmt.FmtText)
		for _, mf := range mfs {
			if err := enc.Encode(mf); err != nil {
				break
			}
		}
	})

	// v3.1 预留占位（§9.2 方向 B / §2 a2a 接口位）
	h.POST("/mcp", mcpserver.StubHandler())
	h.GET("/.well-known/agent.json", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(http.StatusOK, mcpserver.AgentCardPlaceholder("argus", version))
	})
	// register(h) 开头已包含 customizedRegister 的 /ping，勿重复注册
	log.Info("argus.boot.http_ready", "addr", cfg.Addr, "version", version)
}

// llmReachable LLM 端点连通性（readyz 用；任意 HTTP 响应均视为可达）。
func llmReachable(baseURL string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return true
}
