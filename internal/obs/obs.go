// Package obs 运行时观测（design §10.1）：argus_* 指标定义并挂到 ekit metrics.Registry
// （对齐工作区 kube-prometheus-stack 接入），在 router.go 暴露 /metrics。
package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"time"

	ekitmetrics "git.example.com/enjoydream/ekit/observability/metrics"

	"argus/internal/plugin"
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

	findingsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_findings_total", Help: "审查产出分布",
	}, []string{"severity", "plugin"})

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

	// layerExec 分层活检：层被真正走到的次数。increase()==0 且 tasks_total{done}>0
	// → 该层静默失效（配置关掉的层不注册/不打点，不误报）。
	layerExec = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_layer_exec_total", Help: "分层执行探针（活检：名义存在但零执行）",
	}, []string{"layer"})
)

// Init 把指标注册进 ekit Registry 并接通 plugin 观测钩子。幂等。
// metrics.Init 补齐 go/process/buildinfo 通用收集器（此前 /metrics 只有 argus_* 指标）。
func Init() {
	ekitmetrics.Init("argus")
	ekitmetrics.Registry.MustRegister(
		tasksTotal, stageDuration, pluginDuration, pluginFailures, llmTokens, findingsTotal,
		tasksPending, tasksRunning, notifyFailures, reflectDropped, reflectDegraded, layerExec,
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
	plugin.HookFindings = func(severity, name string, n int) {
		findingsTotal.WithLabelValues(severity, name).Add(float64(n))
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
