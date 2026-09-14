// Argus —— 可插拔多 Agent 审查平台（design.md v3.0）。
// main 只做进程生命周期：配置 → ekit bootstrap（信号/停机）→ bootstrap.Assemble
// 装配子系统 → 资源注册（store/worker/http 逆序关闭）→ HTTP 服务。
// 全部对象构造（模型栈/知识库/插件/编排）在 internal/bootstrap。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

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
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/poller"
	"argus/internal/service"
	"argus/internal/storage"
	"git.example.com/enjoydream/agentkit/audit"
	agentkitworker "git.example.com/enjoydream/agentkit/worker"

	argusworker "argus/internal/worker"
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
	cfg.WarnOnly(log) // 部分配置交叉校验告警（Milvus 半配 / fallback 三缺一）
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

// runApp 存储与生命周期接线，子系统对象构造全部在 bootstrap.Assemble。
// 资源按 [store, poll-leader, worker, http] 注册 → 逆序关闭：
// HTTP 先停（新任务入口关闭）→ worker 两阶段排水 60s（在途自然完成，超时硬取消
// 保持 running 待下次启动重置）→ 选主让位 → 最后关库。
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
	// 多副本同 SHA 双跑最终防线（深挖 P0）：SQLite 部分唯一索引；历史在飞重复
	// 数据会使其退化（active=false），清理后重启自愈。
	if guardActive, err := store.EnsureSameSHAGuard(); err != nil {
		return err
	} else if !guardActive {
		log.Warn("argus.boot.same_sha_guard_skipped", "hint",
			"tasks 表存在历史在飞重复 (platform,owner,repo,number,head_sha)，唯一索引未建立；清理重复任务后重启自愈")
	}
	if n, err := store.ResetRunningToPending(ctx, agentkitworker.StaleRunningAfter); err != nil {
		log.Error("argus.boot.reset_running_failed", "error", err.Error())
	} else if n > 0 {
		log.Warn("argus.boot.reset_running", "count", n,
			"stale_after", agentkitworker.StaleRunningAfter.String(),
			"hint", "心跳过期的遗留 running 任务已重置回 pending")
	}

	// ---- 子系统装配（模型栈/知识库/插件/工具/编排，internal/bootstrap）----
	sys, err := bootstrap.Assemble(ctx, cfg, log, store, version, instanceID)
	if err != nil {
		return err
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

	// ---- worker pool（§10.4 DB 即队列；两阶段优雅停机）----
	wp := &agentkitworker.Pool{Queue: &argusworker.StoreQueue{Store: store}, N: cfg.MaxConcurrentTasks, Log: log, Run: sys.Runner.Execute}
	wp.Start(ctx)
	// 注册序 [store, poll-leader, worker, http] → 关闭逆序：worker 在 HTTP 停止后排水 60s，store 最后关
	app.AddResource("worker", func(context.Context) error {
		wp.Stop(60 * time.Second)
		return nil
	})

	// ---- 运维加固：任务保留清理 + 队列深度采样（WorkerManager 管理生命周期）----
	// ctx 取消时所有 ticker 自动退出（cancel 先于 close），无需显式 Close
	wm := ekitbootstrap.NewWorkerManager(ctx)
	wm.AddTicker("workcopy-scavenger", time.Hour, func(context.Context) { sys.WC.Sweep(time.Hour) })
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
	// 会话与指纹的保留清理（深挖修复：qa_sessions/finding_prints 此前永不清理，
	// history_json/comment 单调膨胀拖慢全表扫描与备份）。
	// qa_sessions：上下文 TTL 24h，7 天前会话无回放价值；finding_prints：长期
	// 未复现（last_seen_at）的指纹遗忘——最坏效果是同型意见重新从头计数，不会
	// 复活误报（保留期给足 180 天起）。
	qaCutoff := 7 * 24 * time.Hour
	printCutoff := 180 * 24 * time.Hour
	if cfg.TaskRetentionDays > 0 {
		r := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		if r > qaCutoff {
			qaCutoff = r
		}
		if r*3 > printCutoff {
			printCutoff = r * 3
		}
	}
	pruneAuxOnce := func(ctx context.Context) {
		if n, err := store.PruneQASessions(ctx, time.Now().Add(-qaCutoff)); err != nil {
			log.Warn("argus.boot.qa_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.qa_pruned", "removed", n)
		}
		if n, err := store.PruneStalePrints(ctx, time.Now().Add(-printCutoff)); err != nil {
			log.Warn("argus.boot.print_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.print_pruned", "removed", n)
		}
	}
	pruneAuxOnce(ctx)
	wm.AddTicker("session-print-prune", 24*time.Hour, pruneAuxOnce)
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
			Knowledge:    sys.Knowledge,
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
			pl := &poller.Poller{SDK: sys.SDK, Store: store, Targets: targets,
				Interval: cfg.PollInterval, SkipDraft: cfg.SkipDraft, Log: log, Bus: sys.Bus,
				IsLeader: pollElector.IsLeader}
			pl.Start(ctx)
		}
	}

	// ---- service + biz 接线 ----
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sys.SDK, Registry: sys.Registry,
		Knowledge: sys.Knowledge, Log: log, Bus: sys.Bus,
		Model: sys.Models.React, QAModel: sys.Models.QA,
		RouterModel: sys.Models.Router,
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
		// 显式超时（深挖修复）：无读超时面向办公网/公网时慢连接可耗尽 fd。
		// 不设 WriteTimeout——SSE（/events）与 MCP 流式响应是长写，写超时会掐断；
		// IdleTimeout 需大于 SSE keepalive（15s）与代理空闲阈值。
		server.WithReadTimeout(30*time.Second),
		server.WithIdleTimeout(120*time.Second),
	)
	// ekit traceid：生成/透传 X-Trace-Id + 注入 RequestContext 与标准 context；
	// mw 响应壳与 logx.Ctx* 日志的 trace_id 皆源于此
	h.Use(hmw.Middleware()...)
	registerCustom(h, store, sys.SDK, cfg, svc)
	// h.Run() 进 bootstrap 后台（关闭阶段先 h.Shutdown 排水，h.Run 退出由 wait 阶段收割）
	ekithertz.RunServer(app, "http", h)
	log.Info("argus.boot.http_ready", "addr", cfg.Addr, "version", version)
	// bootstrap 语义：userFn 返回 = 主动关机。此处阻塞直到停机第一阶段取消 ctx，
	// 否则进程装配完成即退出（SIGTERM 由 bootstrap 的 signal 监听承接）。
	<-ctx.Done()
	return nil
}

// register 自定义路由：健康检查 / 指标 / MCP+A2A 占位（§5 路由表 v3.0 部分 + §9.2 方向 B 占位）。
func registerCustom(h *server.Hertz, store *storage.Store, sdk *argusctx.SDKCtx, cfg *config.Config, svc *service.Service) {
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
