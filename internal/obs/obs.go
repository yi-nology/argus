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
)

// Init 把指标注册进 ekit Registry 并接通 plugin 观测钩子。幂等。
func Init() {
	ekitmetrics.Registry.MustRegister(
		tasksTotal, stageDuration, pluginDuration, pluginFailures, llmTokens, findingsTotal,
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
}
