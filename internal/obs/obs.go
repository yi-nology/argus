// Package obs 运行时观测（design §10.1）：argus_* 指标定义并挂到 oekit/metrics.Registry
// （对齐工作区 kube-prometheus-stack 接入），在 router.go 暴露 /metrics。
package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"time"

	"github.com/oekit/metrics"

	"github.com/yi-nology/argus/internal/plugin"
)

var (
	tasksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_tasks_total", Help: "任务终态计数（吞吐与失败率）",
	}, []string{"platform", "kind", "status"})

	stageDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_stage_duration_seconds",
		Help:    "R1-R5 阶段耗时分布",
		Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"stage"})

	pluginDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_plugin_duration_seconds",
		Help:    "插件耗时（熔断依据的可视化）",
		Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"plugin"})

	pluginFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_failures_total", Help: "插件失败计数（含 poster）",
	}, []string{"plugin"})

	llmTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_tokens_total", Help: "LLM token 消耗（成本核算；聚合计数器，不做预算判定）",
	}, []string{"stage", "model"})

	// llmCachedTokens prompt 缓存命中 token（model）。前缀缓存命中率 = cached/prompt，
	// 命中 token 计价折扣（省 token 盲区修复：此前 CachedTokens 被采集但零消费，
	// 缓存命中率不可见、成本按全价入账高估）。
	llmCachedTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_cached_tokens_total", Help: "LLM prompt 缓存命中 token（前缀缓存命中率=cached/prompt）",
	}, []string{"model"})
	llmPromptTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_prompt_tokens_total", Help: "LLM prompt token（缓存命中率分母）",
	}, []string{"model"})

	findingsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_findings_total", Help: "审查产出分布",
	}, []string{"severity", "plugin"})

	findingsByRepo = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_findings_by_repo_total", Help: "按仓 findings 趋势（密度/异常仓识别）",
	}, []string{"platform", "repo", "severity"})

	taskDurationByRepo = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_task_duration_seconds_by_repo",
		Help:    "按仓任务耗时分布（审查延迟趋势）",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 900, 1800},
	}, []string{"platform", "repo", "kind"})

	tasksPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "argus_tasks_pending", Help: "当前 pending 任务数（队列积压观测）",
	})
	tasksRunning = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "argus_tasks_running", Help: "当前 running 任务数",
	})
	notifyFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_notify_failures_total", Help: "IM 通知推送失败计数（octopus 可达性观测）",
	}, []string{"channel"})

	reflectDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_reflect_dropped_total", Help: "R3.5 证据门 strict 丢弃计数",
	}, []string{"reason"})

	reflectDegraded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_reflect_degraded_total", Help: "R3.5 证据门 soft 降级计数",
	}, []string{"reason"})

	ctxFit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_ctxfit_total", Help: "上下文窗口拟合计数（stage×action；passthrough 占绝对多数为常态）",
	}, []string{"stage", "action"})

	llmContinued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_continued_total", Help: "输出截断续写轮次（stage；length 截断的续写救济）",
	}, []string{"stage"})

	toolAnomaly = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_tool_anomaly_total", Help: "ReAct 循环异常守卫拦截计数（同名同参连续重复调用）",
	}, []string{"tool"})

	ctxBreakdown = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_ctx_breakdown_runes_total", Help: "上下文构成分解（stage×role 的 rune 累计量；FitModel 发送路径顺手分桶）",
	}, []string{"stage", "role"})

	r2SoftFailure = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "argus_r2_soft_failures_total", Help: "R2 变更摘要软失败计数（输出空/过短，重试后仍无效走降级）",
	})

	// layerExec 分层活检：层被真正走到的次数。increase()==0 且 tasks_total{done}>0
	// → 该层静默失效（配置关掉的层不注册/不打点，不误报）。
	layerExec = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_layer_exec_total", Help: "分层执行探针（活检：名义存在但零执行）",
	}, []string{"layer"})

	// verdicts R5 真值表命中分布（深挖补充：此前裁决分布只能查库）。
	verdicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_verdicts_total", Help: "裁决分布（rule=R5.0..R5.5）",
	}, []string{"platform", "kind", "verdict", "rule"})

	// fenceNeutralized 围栏中和计数：注入特征文本出现信号（防线 #1 观测面，
	// 此前为纯字符串函数零留痕）。
	fenceNeutralized = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "argus_fence_neutralized_total", Help: "DataFence 中和的围栏标记序列数（注入攻击信号）",
	})

	// pluginSkipped 插件跳过原因分布（circuit/budget/canceled）。
	pluginSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_skipped_total", Help: "插件未参与原因分布",
	}, []string{"reason"})

	// commentRoutes 评论意图路由分布。
	commentRoutes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_route_total", Help: "@argus 评论意图路由分布",
	}, []string{"route", "via"})

	// taskDeduped 任务创建去重分布。
	taskDeduped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_task_create_deduped_total", Help: "webhook 任务去重分布（幂等观测）",
	}, []string{"reason"})

	// sseConnections 当前 SSE 订阅连接数。
	sseConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "argus_sse_connections", Help: "当前 /events SSE 订阅连接数",
	})

	// webhookSkipped 事件被白名单/规则过滤分布：此前 skipped() 零日志零指标，
	// "整类事件被丢"（如 v3.7.x 白名单词表与归一化脱节导致 cr.updated 断流）
	// 在生产完全不可观测。
	webhookSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_skipped_total", Help: "webhook 事件被规则跳过分布（event_type/bot_self/missing_cr/draft）",
	}, []string{"reason"})

	// webhookRejected webhook 入口拒绝分布（安全信号：签名拒绝/无 secret 拒绝）。
	webhookRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_rejected_total", Help: "webhook 入口拒绝分布（no_secret/signature/parse/body_too_large）",
	}, []string{"reason"})

	// webhookEnsure 启动期幂等确保动作分布（v5.4.2 补观测）：created/updated/
	// unchanged/secret_patched/skipped/failed——ensure 是 opt-in 新通道，没指标
	// 就只能翻日志核实它是否真的在工作（与 v3.7.x skipped 盲区同型教训）。
	webhookEnsure = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_ensure_total", Help: "启动期 webhook 幂等确保动作分布（created/updated/unchanged/secret_patched/skipped/failed）",
	}, []string{"action"})

	// llmFailovers LLM 主备切换计数：此前只有 Warn 日志，切换频率无法聚合告警。
	llmFailovers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_failovers_total", Help: "LLM 主→备切换次数（fallback/failover）",
	}, []string{"stage"})

	// qaFailures 评论链路 LLM 失败计数。
	qaFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_qa_failures_total", Help: "评论链路处理失败分布（llm/reject/…）",
	}, []string{"reason"})

	// testGenVerify testgen 沙箱验证结果计数（v3.22.0）。
	testGenVerify = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_testgen_verify_total", Help: "testgen 沙箱验证结果分布（passed/failed）",
	}, []string{"result"})

	// pollerFailures 轮询兜底链路失败计数（v3.9.2：轮询是 webhook 不可达环境的
	// 主力任务源，此前失败仅 Warn 日志，API 故障率/截断率无法聚合告警）。
	pollerFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_poller_failures_total", Help: "轮询兜底失败分布（list_failed/page_truncated/task_create_failed）",
	}, []string{"reason"})

	// commentBusy 评论链路并发闸满载拒绝（评论风暴 / 容量不足信号）。
	commentBusy = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "argus_comment_busy_total", Help: "评论链路并发闸满载拒绝次数（调用方收到 busy）",
	})

	// commentPostFailures 评论回帖失败计数（问了没人回的可观测面）。
	commentPostFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_post_failures_total", Help: "评论回帖失败分布（reply/…）",
	}, []string{"kind"})

	// bestOfN best-of-N 采样结果分布（result: full|partial；成本 ×N 的对照组观测）。
	bestOfN = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_bestofn_total", Help: "best-of-N 采样结果分布（v3.10.0）",
	}, []string{"plugin", "result"})
)

// Init 把指标注册进 oekit/metrics Registry 并接通 plugin 观测钩子。幂等。
// metrics.Init 补齐 go/process/buildinfo 通用收集器（此前 /metrics 只有 argus_* 指标）。
func Init() {
	metrics.Init("argus")
	metrics.Registry.MustRegister(
		tasksTotal, stageDuration, pluginDuration, pluginFailures, llmTokens, findingsTotal,
		llmCachedTokens, llmPromptTokens,
		findingsByRepo, taskDurationByRepo,
		tasksPending, tasksRunning, notifyFailures, reflectDropped, reflectDegraded, layerExec,
		ctxFit, llmContinued, toolAnomaly, ctxBreakdown, r2SoftFailure,
		verdicts, fenceNeutralized, pluginSkipped, commentRoutes, taskDeduped, sseConnections,
		webhookSkipped, webhookRejected, webhookEnsure, llmFailovers, qaFailures, testGenVerify,
		pollerFailures, commentBusy, commentPostFailures, bestOfN,
		evalMatchRatio, evalCasesTotal, evalCasesPassed, evalResultsTimestamp,
	)
	plugin.HookTaskTotal = func(platform, kind, status string) {
		tasksTotal.WithLabelValues(platform, kind, status).Inc()
	}
	plugin.HookStageDuration = func(stage string, d time.Duration) {
		stageDuration.WithLabelValues(stage).Observe(d.Seconds())
	}
	plugin.HookPluginDuration = func(name string, d time.Duration) {
		pluginDuration.WithLabelValues(name).Observe(d.Seconds())
	}
	plugin.HookPluginFailure = func(name string) {
		pluginFailures.WithLabelValues(name).Inc()
	}
	plugin.HookLLMTokens = func(stage, model string, n int) {
		llmTokens.WithLabelValues(stage, model).Add(float64(n))
	}
	plugin.HookLLMCache = func(model string, cached, prompt int) {
		llmCachedTokens.WithLabelValues(model).Add(float64(cached))
		llmPromptTokens.WithLabelValues(model).Add(float64(prompt))
	}
	plugin.HookFindings = func(severity, name string, n int) {
		findingsTotal.WithLabelValues(severity, name).Add(float64(n))
	}
	plugin.HookFindingsByRepo = func(platform, repo, severity string, n int) {
		findingsByRepo.WithLabelValues(platform, repo, severity).Add(float64(n))
	}
	plugin.HookTaskDurationByRepo = func(platform, repo, kind string, seconds float64) {
		taskDurationByRepo.WithLabelValues(platform, repo, kind).Observe(seconds)
	}
	plugin.HookReflectDrop = func(reason string) {
		reflectDropped.WithLabelValues(reason).Inc()
	}
	plugin.HookReflectDegrade = func(reason string) {
		reflectDegraded.WithLabelValues(reason).Inc()
	}
	plugin.HookLayerExec = func(layer string) {
		layerExec.WithLabelValues(layer).Inc()
	}
	plugin.HookVerdict = func(platform, kind, verdict, rule string) {
		verdicts.WithLabelValues(platform, kind, verdict, rule).Inc()
	}
	plugin.HookFenceNeutralized = func(n int) {
		fenceNeutralized.Add(float64(n))
	}
	plugin.HookPluginSkipped = func(reason string) {
		pluginSkipped.WithLabelValues(reason).Inc()
	}
	plugin.HookCommentRoute = func(route, via string) {
		commentRoutes.WithLabelValues(route, via).Inc()
	}
	plugin.HookTaskDeduped = func(reason string) {
		taskDeduped.WithLabelValues(reason).Inc()
	}
	plugin.HookBestOfN = func(name, result string) {
		bestOfN.WithLabelValues(name, result).Inc()
	}
}

// AddSSEConnections SSE 订阅连接数增减（biz events handler 调用）。
func AddSSEConnections(delta int) {
	sseConnections.Add(float64(delta))
}

// SetTaskQueueDepth 队列深度 gauge（main 的周期采样器调用）。
func SetTaskQueueDepth(pending, running int64) {
	tasksPending.Set(float64(pending))
	tasksRunning.Set(float64(running))
}

// IncrNotifyFailure IM 通知推送失败计数。
func IncrNotifyFailure(channel string) {
	notifyFailures.WithLabelValues(channel).Inc()
}

// IncrWebhookSkipped webhook 事件被规则跳过计数（service 包调用）。
func IncrWebhookSkipped(reason string) {
	webhookSkipped.WithLabelValues(reason).Inc()
}

// IncrWebhookRejected webhook 入口拒绝计数（含安全信号：签名/secret 拒绝）。
func IncrWebhookRejected(reason string) {
	webhookRejected.WithLabelValues(reason).Inc()
}

// IncrWebhookEnsure 启动期 webhook 幂等确保动作计数（bootstrap 调用）。
func IncrWebhookEnsure(action string) {
	webhookEnsure.WithLabelValues(action).Inc()
}

// IncrLLMFailover LLM 主备切换计数（bootstrap 装配的两类切换回调调用）。
func IncrLLMFailover(stage string) {
	llmFailovers.WithLabelValues(stage).Inc()
}

// IncrCtxFit 上下文窗口拟合事件（ctxfit 装饰器回调；仅非 passthrough 上报）。
func IncrCtxFit(stage, action string) {
	ctxFit.WithLabelValues(stage, action).Inc()
}

// IncrLLMContinued 输出截断续写轮次（ContinueModel 回调）。
func IncrLLMContinued(stage string) {
	llmContinued.WithLabelValues(stage).Inc()
}

// IncrToolAnomaly 循环异常守卫拦截（同名同参连续重复调用）。
func IncrToolAnomaly(tool string) {
	toolAnomaly.WithLabelValues(tool).Inc()
}

// AddCtxBreakdown 上下文构成分解累计（stage×role 的 rune 量）。
func AddCtxBreakdown(stage, role string, runes int) {
	ctxBreakdown.WithLabelValues(stage, role).Add(float64(runes))
}

// IncrR2SoftFailure R2 变更摘要软失败（输出空/过短）。
func IncrR2SoftFailure() {
	r2SoftFailure.Inc()
}

// IncrQAFailure 评论链路处理失败计数（service 层调用）。
func IncrQAFailure(reason string) {
	qaFailures.WithLabelValues(reason).Inc()
}

// IncrPollerFailure 轮询兜底失败计数（poller 包调用）。
func IncrPollerFailure(reason string) {
	pollerFailures.WithLabelValues(reason).Inc()
}

// IncrCommentBusy 评论并发闸满载拒绝计数（service 层调用）。
func IncrCommentBusy() {
	commentBusy.Inc()
}

// IncrTestGenVerify testgen 沙箱验证结果计数（v3.22.0；passed/failed）。
func IncrTestGenVerify(passed bool) {
	if passed {
		testGenVerify.WithLabelValues("passed").Inc()
		return
	}
	testGenVerify.WithLabelValues("failed").Inc()
}

// IncrCommentPostFailure 评论回帖失败计数（service 层调用）。
func IncrCommentPostFailure(kind string) {
	commentPostFailures.WithLabelValues(kind).Inc()
}
