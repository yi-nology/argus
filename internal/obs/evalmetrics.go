package obs

// golden 评测结果指标（v3.14.0，G1/G6）：eval-real 在测试进程离线跑，逐轮落盘
// eval/results/latest.json；服务侧周期加载后以 gauge 暴露——审查质量趋势与运行时
// 指标在 /metrics 同面抓取，不再只活在 EXPERIMENTS.md 登记表里。
// eval 不在服务进程内执行，这是刻意的旁路形态（评测变异不影响在线链路）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// 判定辅助：文件不存在（生产未跑过 eval 属常态，调用方静默跳过）/
// 运行中增量快照（同机有 eval-real 正在写，等下一轮完整结果）。
func IsNotExistRefreshErr(err error) bool { return errors.Is(err, fs.ErrNotExist) }
func IsIncompleteRefreshErr(err error) bool {
	return errors.Is(err, ErrEvalIncomplete)
}

var (
	// evalMatchRatio 最近一次完整 eval-real 的 verdict 命中率。model/route 标注
	// A/B 组（route=global 或 r3=…,reviewer=… 非空阶段覆盖），跨组对比同面可见。
	evalMatchRatio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "argus_eval_verdict_match_ratio", Help: "golden 评测 verdict 命中率（最近一次完整 eval-real 落盘，v3.14.0）",
	}, []string{"model", "route"})
	evalCasesTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "argus_eval_cases_total", Help: "golden 评测集用例总数（同上）",
	}, []string{"model", "route"})
	evalCasesPassed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "argus_eval_cases_passed", Help: "golden 评测 verdict 命中用例数（同上）",
	}, []string{"model", "route"})
	// evalResultsTimestamp 最近落盘时间（unix 秒，无标签）——抓取侧用 up 时间差
	// 判断评测是否停跑（CI 定时任务断供的告警锚点）。
	evalResultsTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "argus_eval_results_timestamp_seconds", Help: "golden 评测结果落盘时间（unix 秒）",
	})
)

// ErrEvalIncomplete 结果文件是运行中增量快照（done=false），不计分不覆盖 gauge。
var ErrEvalIncomplete = fmt.Errorf("eval results incomplete（运行中增量快照）")

// RefreshEvalResults 加载 eval/results/latest.json 并刷新评测 gauge。
// 文件不存在返回 fs.ErrNotExist 包装错误（生产未跑 eval 属常态，调用方静默）；
// 增量快照返回 ErrEvalIncomplete。每次刷新整体 Reset 三个 GaugeVec——旧组标签
// 序列不残留，/metrics 上的评测指标始终只反映最近一次完整评测。
func RefreshEvalResults(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读评测结果: %w", err)
	}
	var r struct {
		GeneratedAt string            `json:"generated_at"`
		Model       string            `json:"model"`
		StageModels map[string]string `json:"stage_models"`
		Total       int               `json:"total"`
		Passed      int               `json:"passed"`
		PassRate    float64           `json:"pass_rate"`
		Done        bool              `json:"done"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("解析评测结果: %w", err)
	}
	if !r.Done {
		return ErrEvalIncomplete
	}
	ts, err := time.Parse(time.RFC3339, r.GeneratedAt)
	if err != nil {
		return fmt.Errorf("解析评测时间 %q: %w", r.GeneratedAt, err)
	}
	labels := prometheus.Labels{"model": r.Model, "route": evalRouteLabel(r.StageModels)}
	evalMatchRatio.Reset()
	evalCasesTotal.Reset()
	evalCasesPassed.Reset()
	evalMatchRatio.With(labels).Set(r.PassRate)
	evalCasesTotal.With(labels).Set(float64(r.Total))
	evalCasesPassed.With(labels).Set(float64(r.Passed))
	evalResultsTimestamp.Set(float64(ts.Unix()))
	return nil
}

// evalRouteLabel 阶段覆盖 → route 标签：全空 = global；否则仅列非空阶段
// （固定 r3→r35→reviewer 序，如 "r3=mimo-v2.5-pro,reviewer=mimo-v2.5-pro"）。
func evalRouteLabel(stages map[string]string) string {
	route := ""
	for _, k := range []string{"r3", "r35", "reviewer"} {
		if v := stages[k]; v != "" {
			if route != "" {
				route += ","
			}
			route += k + "=" + v
		}
	}
	if route == "" {
		return "global"
	}
	return route
}
