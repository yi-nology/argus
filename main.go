// Argus —— 可插拔多 Agent 审查平台（design.md v3.0）。
// main 只做进程生命周期：配置 → oekit/bootstrap（信号/停机）→ storage 打开 →
// bootstrap.Assemble 装配对象图 → bootstrap.StartSubsystems 启动后台子系统 →
// HTTP 接线 → 资源逆序关闭。全部对象构造与子系统组装在 internal/bootstrap
// （v3.63.0 组合根单点闭环）。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/google/uuid"

	oekitbootstrap "github.com/oekit/bootstrap"
	"github.com/oekit/concurrency/async"
	oekitversion "github.com/oekit/core/version"
	ekcache "github.com/oekit/data/cache"
	ekithertz "github.com/oekit/hertz"
	health "github.com/oekit/hertz/health"
	hertzmetrics "github.com/oekit/hertz/metrics"
	versionh "github.com/oekit/hertz/versionh"
	"github.com/oekit/logx"
	ekitmetrics "github.com/oekit/metrics"
	mw "github.com/oekit/middleware"
	"github.com/oekit/middleware/hmw"
	oekittracing "github.com/oekit/tracing"
	gormotel "github.com/oekit/tracing/gormotel"

	hapi "argus/biz/handler/api"
	rapi "argus/biz/router/api"
	"argus/internal/bootstrap"
	"argus/internal/channel/mcpserver"
	"argus/internal/config"
	"argus/internal/obs"
	"argus/internal/service"
	"argus/internal/storage"
)

// version 由构建时 -ldflags 注入；开发期回退 dev。
var version = "dev"

func main() {
	config.LoadDotEnv()
	cfg := config.Load()

	log := logx.NewSlogLogger("argus")
	obs.Init()
	// 版本元数据单源：/version 端点（oekit core/version）与报告头/AgentCard
	// 共用同一 ldflags 注入值（BuildCommit/BuildTime/BuildBranch 由 Makefile
	// 直接注入 core/version 包变量）。
	oekitversion.Version = version
	// oekit/concurrency async 的 panic 恢复日志注入 logx（裸 goroutine 已全部收编为 async.GoSafe）
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

	// ---- 生命周期（oekit bootstrap v0.4.2）：SIGINT/SIGTERM → ctx 取消 →
	// 资源逆序关闭（http → worker → store）→ 后台 goroutine 排水（30s 超时）。
	// ShutdownTimeout 须覆盖 http 排水（~5s）+ worker 两阶段停机（60s grace + ≤10s 硬取消）+ 关库。
	boot, bootErr := oekitbootstrap.New(
		oekitbootstrap.WithServiceName("argus"),
		oekitbootstrap.WithShutdownTimeout(90*time.Second),
	)
	if bootErr != nil {
		panic(bootErr)
	}
	if err := boot.Run(func(app *oekitbootstrap.App) error {
		return runApp(app, cfg, log)
	}); err != nil {
		panic(err)
	}
}

// runApp 存储与生命周期接线，子系统对象构造全部在 bootstrap.Assemble。
// 资源按 [store, poll-leader, worker, http] 注册 → 逆序关闭：
// HTTP 先停（新任务入口关闭）→ worker 两阶段排水 60s（在途自然完成，超时硬取消
// 保持 running 待下次启动重置）→ 选主让位 → 最后关库。
func runApp(app *oekitbootstrap.App, cfg *config.Config, log logx.Logger) error {
	ctx := app.Context()

	// ---- OTel 链路导出（v3.163.0，oekit/tracing；ARGUS_OTEL_ENDPOINT opt-in）----
	// 全局 TracerProvider + W3C/B3 传播器 + gormotel 挂主库（DB 查询成 span）。
	// Endpoint 空 = 完全不 Init（零开销）；flush 资源先于 store 注册（逆序关闭
	// 时最后 flush——关库之后的残余 span 也能带出去）。
	if cfg.OTelEndpoint != "" {
		prov, terr := oekittracing.Init(oekittracing.Config{
			ServiceName: "argus",
			Endpoint:    cfg.OTelEndpoint,
			Insecure:    cfg.OTelInsecure,
			SampleRate:  cfg.OTelSampleRate,
			Protocol:    cfg.OTelProtocol,
		})
		if terr != nil {
			return fmt.Errorf("otel 初始化失败: %w", terr)
		}
		storage.SetGormPlugin(gormotel.NewPlugin(gormotel.WithDBName("argus")))
		app.AddResource("otel-flush", func(ctx context.Context) error { return prov.Shutdown(ctx) })
		log.Info("argus.boot.otel_enabled", "endpoint", cfg.OTelEndpoint, "protocol", cfg.OTelProtocol)
	}

	// ---- 存储（§5.2：三张表 + 幂等/去重/抢占/staleness）----
	store, err := storage.Open(cfg.DBDSN)
	if err != nil {
		return err
	}
	app.AddResource("store", func(context.Context) error { return store.Close() })
	// claim 归属 + 心跳 + 选主身份（多副本滚动发布安全）。uuid 失败退化为
	// hostname:pid——RunnerID 为空会让心跳续期 no-op，本实例任务 90s 后被
	// reaper 反复重置重跑。
	var instanceID string
	if id, err := uuid.NewRandom(); err == nil {
		instanceID = id.String()[:16]
	} else {
		host, _ := os.Hostname()
		instanceID = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	store.RunnerID = instanceID
	log.Info("argus.boot.instance", "id", instanceID)
	// 多副本同 SHA 双跑最终防线（深挖 P0）：SQLite 部分唯一索引；历史在飞重复
	// 数据会使其退化（active=false），清理后重启自愈。真实执行错误只告警不阻断
	// （v3.9.1：错误必须带原始原因透出，此前吞掉会误导运维清数据）。
	if guardActive, err := store.EnsureSameSHAGuard(); err != nil {
		log.Warn("argus.boot.same_sha_guard_failed", "error", err.Error())
	} else if !guardActive {
		log.Warn("argus.boot.same_sha_guard_skipped", "hint",
			"tasks 表存在历史在飞重复 (platform,owner,repo,number,head_sha)，唯一索引未建立；清理重复任务后重启自愈")
	}
	// ---- 子系统装配（模型栈/知识库/插件/工具/编排，internal/bootstrap）----
	sys, err := bootstrap.Assemble(ctx, cfg, log, store, version, instanceID)
	if err != nil {
		return err
	}

	// ---- 启动即复位（v3.30.1 引入，v3.36.0 租约化）：上一进程被打断的 running
	// 任务立即重排 pending，不再等 90s 心跳过期扫。阈值 30s = 一个心跳周期
	//（agentkit worker 20s）+ 余量——本实例死任务的最后心跳必然更旧，立即复位；
	// 多副本部署下其他存活副本的在途任务心跳新鲜（≤20s），不会被误复位。
	if n, err := store.ResetRunningToPending(ctx, 30*time.Second); err != nil {
		log.Warn("argus.boot.running_reset_failed", "error", err.Error())
	} else if n > 0 {
		log.Info("argus.boot.running_reset", "requeued", n,
			"stale_after", "30s",
			"hint", "上次进程中断的在途任务已重新发起（租约感知：存活副本在途任务不受影响）")
	}

	// ---- 后台子系统组装与启动（v3.63.0 收口进 bootstrap：选主/worker 池/
	// 运维 ticker/dream/health/poller 全部构造收敛于 bootstrap.StartSubsystems，
	// 组合根单点闭环——main 只剩进程生命周期）----
	bootstrap.StartSubsystems(app, cfg, log, store, sys, instanceID)

	// ---- service + biz 接线 ----
	// service 构造已收敛进 bootstrap.Assemble（v3.48.0，System 即完整对象图）；
	// main 只剩 HTTP 面接线与停机排水。
	svc := sys.Service
	// 评论链路停机排水（v3.8.0）：HTTP 已停（不再进新事件）后等在途 Q&A 收尾，
	// 此前进程退出直接杀在途 ReAct——用户永无回复。放 worker 停机之前。
	app.AddResource("comments", func(context.Context) error {
		svc.DrainComments(30 * time.Second)
		return nil
	})
	hapi.SetService(svc)
	rapi.SetAuth = svc.BearerMiddleware()

	// ---- HTTP（ARGUS_ADDR 默认 :8899）----
	// oekit/logx hlog 桥接：Hertz 框架内部日志（连接异常、路由警告等）统一走 logx。
	// ⚠️ 必须传 slog 实例（NewSlogLogger），不能传 logx.WithService(...)——后者走
	// logx 默认 hlog 后端，装回 hlog 后形成 adapter↔logx 无限递归（实测主 goroutine
	// 卡死 + 100% CPU + SIGTERM 无响应，oekit AGENTS.md 的示例即此雷）。
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
	// oekit/middleware/hmw traceid：生成/透传 X-Trace-Id + 注入 RequestContext 与标准 context；
	// mw 响应壳与 logx.Ctx* 日志的 trace_id 皆源于此
	h.Use(hmw.Middleware()...)
	// 请求级指标（v3.9.2）：状态码/路由模板计数 + 时延分布（5xx 率可告警）
	h.Use(obs.HTTPMiddleware())
	// panic 恢复（v3.146.0，oekit mw）：hertz 引擎兜底仅在 PanicHandler 非空时挂
	//（默认 nil）、netpoll 事件循环显式不 recover——此前任一 handler panic 直接
	// 打死进程。挂在 obs 内层：恢复出的 500 仍进指标；堆栈走 logx 结构化日志，
	// 响应走 mw.ErrorResp 信封（与业务错误同形状）。
	h.Use(mw.RecoveryMiddleware())
	// 统一响应壳（v3.147.0，oekit mw）：裸 JSON 自动包
	// {code,data,message,trace_id} 信封；已带 code 的响应（mw.SuccessResp/ErrorResp、
	// health/version 端点自写信封）不重复包裹。协议端点豁免清单收在栈装配处
	//（v3.148.0 升 middleware v0.5.0 的声明式 WithSkipPaths/Prefixes）：
	// /.well-known/（A2A agent card）与 /mcp（MCP JSON-RPC）按规范解析顶层字段，
	// 禁止信封包裹；MCP 桥内部另带 SkipResponseWrap 组件级标记（挂到别的路径也安全）。
	h.Use(mw.ResponseMiddleware(
		mw.WithSkipPrefixes("/.well-known/"),
		mw.WithSkipPaths("/mcp"),
	))
	registerCustom(h, store, cfg, svc)
	// h.Run() 进 bootstrap 后台（关闭阶段先 h.Shutdown 排水，h.Run 退出由 wait 阶段收割）
	ekithertz.RunServer(app, "http", h)
	log.Info("argus.boot.http_ready", "addr", cfg.Addr, "version", version)
	// bootstrap 语义：userFn 返回 = 主动关机。此处阻塞直到停机第一阶段取消 ctx，
	// 否则进程装配完成即退出（SIGTERM 由 bootstrap 的 signal 监听承接）。
	<-ctx.Done()
	return nil
}

// register 自定义路由：健康检查 / 指标 / MCP+A2A 占位（§5 路由表 v3.0 部分 + §9.2 方向 B 占位）。
func registerCustom(h *server.Hertz, store *storage.Store, cfg *config.Config, svc *service.Service) {
	register(h) // hz 生成路由（idl/argus.thrift 声明的全部业务面，v3.188.0 起含收编路由）

	// 存活（v3.147.0：oekit hertz/health PingHandler，统一信封；进程级存活，零依赖检查）
	h.GET("/healthz", health.PingHandler)

	// 就绪（v3.147.0：oekit NewHealthHandler，Checker 并发执行，任一失败 503，
	// 总超时 5s 上界）：DB 连通 + LLM 端点可达（未配置 LLM 直接通过；探针仍走
	// 下方 30s TTL 缓存）。失败细节只进 logx，响应统一 "check failed"（库内脱敏，与
	// v3.8.0 不回显 BaseURL 的纪律同向）。
	h.GET("/readyz", health.NewHealthHandler(
		health.WithCheckers(
			health.NamedChecker("db", func(ctx context.Context) error { return store.Ping(ctx) }),
			health.NamedChecker("llm", func(ctx context.Context) error {
				if cfg.LLM.BaseURL == "" || llmReachableCached(cfg.LLM.BaseURL) {
					return nil
				}
				return errors.New("llm unreachable")
			}),
		),
		health.WithCheckTimeout(5*time.Second),
	))

	// 版本端点（v3.147.0，oekit versionh）：core/version 的 ldflags 注入值 + 运行时
	// uptime；自带 code/trace_id 信封，不会被 ResponseMiddleware 重复包裹。
	h.GET("/version", versionh.Handler)

	// Prometheus（ekit：挂 metrics.Registry——obs.Init 已补齐 go/process 收集器；
	// MetricsHandlerFor 内部完成 RequestContext→http.ResponseWriter 适配）。
	// v3.8.0：ARGUS_METRICS_AUTH=1 时挂 Bearer——与 fail-closed 姿态对齐的可选
	// 加固；默认保持无鉴权（k8s prometheus 注解无 token 抓取，开了会 401）。
	metricsHandlers := []app.HandlerFunc{hertzmetrics.MetricsHandlerFor(ekitmetrics.Registry)}
	if cfg.MetricsAuth {
		metricsHandlers = []app.HandlerFunc{svc.BearerMiddleware(), hertzmetrics.MetricsHandlerFor(ekitmetrics.Registry)}
	}
	h.GET("/metrics", metricsHandlers...)

	// MCP Server（v3.1 方向 B 实装）：streamable http + Bearer 鉴权
	mcpBridge := mcpserver.HertzBridge(mcpserver.New(svc, version))
	mcpHandlers := []app.HandlerFunc{svc.BearerMiddleware(), mcpBridge}
	h.GET("/mcp", mcpHandlers...)
	h.POST("/mcp", mcpHandlers...)
	h.DELETE("/mcp", mcpHandlers...)
	// /reports/health（v3.13.0 健康报告）与 /findings/restore（v3.131.0 恢复抑制）
	// 已收编进 idl/argus.thrift 生成路由（2026-10-27 收敛），不再在此手挂。
	h.GET("/.well-known/agent.json", func(ctx context.Context, c *app.RequestContext) {
		// A2A 协议端点：豁免声明在栈级 WithSkipPrefixes("/.well-known/")
		c.JSON(http.StatusOK, mcpserver.AgentCardPlaceholder("argus", version))
	})
	// register(h)（IDL 生成路由）与 customizedRegister（/events）勿在此重复注册
	//（http_ready 日志在 runApp 的 RunServer 之后统一打）
}

// llmReachableClient readyz 探针复用的 HTTP 客户端（避免每次 new transport 泄漏）。
var llmReachableClient = &http.Client{Timeout: 3 * time.Second}

// llmProbeCache LLM 端点连通性缓存（readyz 用；30s TTL + single-flight 合并，
// 避免每 10s 真实请求 LLM 浪费配额）。v3.82.0 引入 ekit data/cache——手写
// mutex+read-check-write 状态机（探针期间全串行阻塞）由库接管：并发探针
// GetOrLoad 合并为一次回源。
var llmProbeCache = ekcache.NewLocalCache(ekcache.LocalConfig[bool]{
	TTL: 30 * time.Second,
	Source: func(ctx context.Context, key string) (*bool, error) {
		ok := llmReachable(key)
		return &ok, nil
	},
})

// llmReachableCached LLM 端点连通性（30s TTL，任意 HTTP 响应均视为可达）。
func llmReachableCached(baseURL string) bool {
	v, _ := llmProbeCache.GetOrLoad(context.Background(), baseURL)
	return v != nil && *v
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
