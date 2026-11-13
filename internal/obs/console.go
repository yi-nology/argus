// console.go —— 质量控制台数据面（#27，v7.1 生产运营）：从 Registry Gather
// 进程内指标快照，供 GET /console 渲染「失败原因归类 + 修复闭环有效率」。
// 只读聚合零新表（验收线：数据全部来自既有聚合面）；指标白名单收口——
// 控制台只透出运营视角需要的计数，不整表倾倒 /metrics。

package obs

import (
	"github.com/oekit/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// consoleMetricsWhitelist 控制台透出的指标名（失败原因归类 + 修复有效率 +
// 吞吐）。与 /metrics 全量面隔离——改白名单即改控制台内容，勿塞调试指标。
var consoleMetricsWhitelist = map[string]bool{
	"argus_tasks_total":                 true, // 终态吞吐（status=failed 即失败归类分母）
	"argus_budget_exceeded_total":       true, // 预算超限（ledger 维度）
	"argus_llm_failovers_total":         true, // LLM 主→备切换
	"argus_plugin_failures_total":       true, // 插件失败（plugin 维度）
	"argus_qa_failures_total":           true, // 评论链路失败（reason 维度）
	"argus_fix_verify_total":            true, // 修复验证（有效率分母）
	"argus_fix_push_total":              true, // 修复推送（有效率分子）
	"argus_testgen_verify_total":        true, // testgen 沙箱验证
	"argus_comment_post_failures_total": true, // 回帖失败
}

// ConsoleMetricEnabled 指标是否在控制台白名单（导出供测试对账——白名单与
// 透出面漂移的单源判定）。
func ConsoleMetricEnabled(name string) bool { return consoleMetricsWhitelist[name] }

// ConsoleMetric 控制台指标样本：指标名 + 标签 + 当前值。
type ConsoleMetric struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// ConsoleMetrics 白名单指标的进程内快照（Gather 只读）。标签串行化为
// k=v 逗号拼接的 Key 便于渲染分组；未注册/无样本的指标缺席（不造零行）。
func ConsoleMetrics() []ConsoleMetric {
	fams, err := metrics.Registry.Gather()
	if err != nil {
		return nil
	}
	out := make([]ConsoleMetric, 0, 16)
	for _, fam := range fams {
		if fam.GetName() == "" || !consoleMetricsWhitelist[fam.GetName()] {
			continue
		}
		for _, m := range fam.Metric {
			out = append(out, ConsoleMetric{
				Name:   fam.GetName(),
				Labels: labelMap(m),
				Value:  counterValue(m),
			})
		}
	}
	return out
}

// labelMap dto 标签 → map（跳过空值；prometheus 内部标签不透出）。
func labelMap(m *dto.Metric) map[string]string {
	if len(m.Label) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.Label))
	for _, l := range m.Label {
		if l.GetValue() == "" || l.GetName() == "__name__" {
			continue
		}
		out[l.GetName()] = l.GetValue()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// counterValue 样本当前值（counter/gauge 族；histogram/summary 取 sum——
// 白名单内无 histogram，防御分支）。
func counterValue(m *dto.Metric) float64 {
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	if m.Gauge != nil {
		return m.Gauge.GetValue()
	}
	if m.Histogram != nil {
		return m.Histogram.GetSampleSum()
	}
	if m.Summary != nil {
		return m.Summary.GetSampleSum()
	}
	return 0
}

// 编译期断言：Registry 是 prometheus.Gatherer（Gather 面存在）。
var _ prometheus.Gatherer = metrics.Registry
