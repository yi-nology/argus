// Package qconsole 审查质量控制台（P3-02，#27）：误报反馈/插件表现/质量趋势/
// Token 消费/任务失败原因的只读聚合与输出形态（静态页 + CLI 文本）。
//
// 数据纪律（issue #27 验收口径）：全部来自既有 API 聚合面——/stats/overview、
// /usage/monthly、/metrics（obs 计数器）、P2-02 评测基线报告文件——无新表、
// 无写路径；本包 L0 零 internal 依赖（自带轻量解析，不复用 service DTO，
// 反向依赖不可能发生）。消费形态：
//   - cmd/qconsole：admin 档 token 拉 API → 落静态页/CLI 报告（离线查看）；
//   - service /console（#27 趋势面）：LoadEvalTrend 只读投影直出（数据源同一
//     聚合面；service→qconsole 单向，本包仍零 internal 依赖）。
package qconsole

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// RepoStatsRow /stats/overview 行（服务端 storage.RepoStats 的 JSON 面）。
type RepoStatsRow struct {
	Platform   string `json:"platform"`
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	TasksDone  int64  `json:"tasks_done"`
	Approved   int64  `json:"approved"`
	Commented  int64  `json:"commented"`
	Rejected   int64  `json:"rejected"`
	Tokens     int64  `json:"tokens"`
	Suppressed int64  `json:"suppressed"`
}

// UsageRow /usage/monthly 行（quota 与 real 双账本同构）。
type UsageRow struct {
	Platform string `json:"platform"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	Tokens   int64  `json:"tokens"`
}

// ReasonCount 失败原因/插件表现计数（/metrics 计数器提取）。
type ReasonCount struct {
	Name  string `json:"name"`
	Count uint64 `json:"count"`
}

// EvalTrend 质量趋势点（P2-02 评测基线报告 eval/results/baseline-latest.json
// 的只读投影——字段与 internal/evalq.Report 的 JSON 形态对齐，独立建模避免
// 包耦合；解析容错：缺字段零值安全）。
type EvalTrend struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Model       string         `json:"model"`
	Stability   float64        `json:"stability"`
	Overall     EvalTrendMets  `json:"overall"`
	ByLanguage  []EvalLangMets `json:"by_language"`
}

// EvalTrendMets 评测指标投影。
type EvalTrendMets struct {
	FP   float64 `json:"fp_rate"`
	Miss float64 `json:"miss_rate"`
	P    float64 `json:"precision"`
	R    float64 `json:"recall"`
}

// EvalLangMets 分语言指标投影。
type EvalLangMets struct {
	Language string  `json:"language"`
	FP       float64 `json:"fp_rate"`
	Miss     float64 `json:"miss_rate"`
}

// Snapshot 控制台一次聚合的只读快照。
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Stats 面（/stats/overview）
	WindowDays int            `json:"window_days"`
	RepoRows   []RepoStatsRow `json:"repo_rows"`
	Tasks      int64          `json:"tasks_done"`
	Tokens     int64          `json:"tokens_used"`
	Suppressed int64          `json:"findings_suppressed"`
	Verdicts   struct {
		Approve, Comment, RequestChanges int64
	} `json:"verdicts"`
	// Usage 面（/usage/monthly 双账本）
	Month       string     `json:"month"`
	QuotaTotal  int64      `json:"quota_total"`
	QuotaBudget int64      `json:"quota_budget"`
	RealTotal   int64      `json:"real_total"`
	RealBudget  int64      `json:"real_budget"`
	UsageRows   []UsageRow `json:"usage_rows"`
	// 失败原因归类（/metrics：llmFailovers/pluginFailures/budgetExceeded/qaFailures 等）
	FailReasons []ReasonCount `json:"fail_reasons"`
	// 插件表现（/metrics：pluginFailures/pluginSkipped 按插件名聚合）
	PluginRows []ReasonCount `json:"plugin_rows"`
	// 质量趋势（评测基线报告文件，可选——缺文件为 nil）
	EvalTrend *EvalTrend `json:"eval_trend,omitempty"`
}

// metrics 计数器名 → 展示名（失败原因归类词表；argus_* 是 obs 单源命名）。
var failReasonMetrics = []struct {
	metric string
	label  string
}{
	{"argus_llm_failovers_total", "LLM 降级切换"},
	{"argus_plugin_failures_total", "插件失败"},
	{"argus_qa_failures_total", "QA 校验失败"},
	{"argus_budget_exceeded_total", "预算超限"},
	{"argus_events_dropped_total", "事件丢弃"},
	{"argus_notify_failures_total", "通知失败"},
	{"argus_poller_failures_total", "轮询失败"},
	{"argus_migration_lease_lost_total", "迁移租约失守"},
}

// pluginMetrics 插件表现计数器（按 plugin 标签展开）。
var pluginMetrics = []struct {
	metric string
	label  string
}{
	{"argus_plugin_failures_total", "失败"},
	{"argus_plugin_skipped_total", "跳过"},
}

// Aggregator 聚合接口（cmd/qconsole 用 HTTPSource 装配；测试用桩）。
type Aggregator interface {
	Snapshot(ctx context.Context, days int) (*Snapshot, error)
}

// HTTPSource 既有 API 聚合源（Bearer admin 档 token；/metrics 403/404 容忍
// ——只读控制台不因观测面缺位而不可用）。
type HTTPSource struct {
	BaseURL string       // 如 http://argus.internal:8899
	Token   string       // admin 档（/metrics 需要；stats/usage 需 read+行约束）
	Client  *http.Client // nil = 缺省 30s
	// MetricsOverride 直取函数（测试注入桩用；nil = GET {BaseURL}/metrics）
	MetricsOverride func(ctx context.Context) (string, error)
}

// Snapshot 实现 Aggregator：拉 stats/usage/metrics 聚合成快照。
func (h *HTTPSource) Snapshot(ctx context.Context, days int) (*Snapshot, error) {
	if h.Client == nil {
		h.Client = &http.Client{Timeout: 30 * time.Second}
	}
	snap := &Snapshot{GeneratedAt: time.Now(), WindowDays: days}

	// /stats/overview
	var stats struct {
		WindowDays int              `json:"window_days"`
		Repos      []RepoStatsRow   `json:"repos"`
		TasksDone  int64            `json:"tasks_done"`
		TokensUsed int64            `json:"tokens_used"`
		Suppressed int64            `json:"findings_suppressed"`
		Verdicts   map[string]int64 `json:"verdicts"`
	}
	if err := h.getJSON(ctx, fmt.Sprintf("/stats/overview?days=%d", days), &stats); err != nil {
		return nil, fmt.Errorf("qconsole: stats/overview: %w", err)
	}
	snap.RepoRows = stats.Repos
	snap.Tasks = stats.TasksDone
	snap.Tokens = stats.TokensUsed
	snap.Suppressed = stats.Suppressed
	snap.Verdicts.Approve = stats.Verdicts["approve"]
	snap.Verdicts.Comment = stats.Verdicts["comment"]
	snap.Verdicts.RequestChanges = stats.Verdicts["request_changes"]

	// /usage/monthly（双账本）
	var usage struct {
		Month      string     `json:"month"`
		Total      int64      `json:"total"`
		Budget     int64      `json:"budget"`
		RealTotal  int64      `json:"real_total"`
		RealBudget int64      `json:"real_budget"`
		Repos      []UsageRow `json:"repos"`
	}
	if err := h.getJSON(ctx, "/usage/monthly", &usage); err != nil {
		return nil, fmt.Errorf("qconsole: usage/monthly: %w", err)
	}
	snap.Month = usage.Month
	snap.QuotaTotal, snap.QuotaBudget = usage.Total, usage.Budget
	snap.RealTotal, snap.RealBudget = usage.RealTotal, usage.RealBudget
	snap.UsageRows = usage.Repos

	// /metrics（失败原因 + 插件表现；缺位容忍）
	if text, err := h.metricsText(ctx); err == nil {
		snap.FailReasons = extractCounters(text, failReasonMetrics)
		snap.PluginRows = pluginRows(text, pluginMetrics)
	}
	return snap, nil
}

func (h *HTTPSource) metricsText(ctx context.Context) (string, error) {
	if h.MetricsOverride != nil {
		return h.MetricsOverride(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+"/metrics", nil)
	if err != nil {
		return "", err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (h *HTTPSource) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// extractCounters prom 文本面提取：metric 名匹配（任一 label 组合）→ 按展示
// 名聚合计数；HELP/TYPE 行跳过。返回非零项（零值计数不上屏）。
func extractCounters(text string, defs []struct {
	metric string
	label  string
}) []ReasonCount {
	out := make([]ReasonCount, 0, len(defs))
	for _, d := range defs {
		total := parseMetricSum(text, d.metric)
		if total > 0 {
			out = append(out, ReasonCount{Name: d.label, Count: total})
		}
	}
	return out
}

// pluginRows 插件表现：按 plugin 标签展开成「插件 × 计数」行（失败/跳过等
// 口径合并总量——插件维度的健康一眼面；明细钻取走 /metrics 原面）。
func pluginRows(text string, defs []struct {
	metric string
	label  string
}) []ReasonCount {
	agg := map[string]*ReasonCount{}
	order := []string{}
	for _, d := range defs {
		for name, count := range parseMetricByLabel(text, d.metric, "plugin") {
			if count == 0 {
				continue
			}
			r, ok := agg[name]
			if !ok {
				r = &ReasonCount{Name: name}
				agg[name] = r
				order = append(order, name)
			}
			r.Count += count
		}
	}
	out := make([]ReasonCount, 0, len(order))
	for _, name := range order {
		out = append(out, *agg[name])
	}
	return out
}

// parseMetricSum prom 指标总和（跨 label 组合）。
func parseMetricSum(text, metric string) uint64 {
	var total uint64
	for _, v := range parseMetricByLabel(text, metric, "") {
		total += v
	}
	return total
}

// parseMetricByLabel prom 文本解析：返回 series → label 值（labelKey 为空 =
// series 原行计数）。容错口径：只认 `NAME{...} VALUE` 与 `NAME VALUE` 行。
func parseMetricByLabel(text, metric, labelKey string) map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := cutMetric(line)
		if !ok || name != metric {
			continue
		}
		labelVal := ""
		if strings.HasPrefix(rest, "{") {
			end := strings.Index(rest, "}")
			if end < 0 {
				continue
			}
			if labelKey != "" {
				labelVal = labelValue(rest[1:end], labelKey)
			}
			rest = rest[end+1:]
		} else if labelKey != "" {
			continue // 无标签 series 不归属任何插件
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		out[labelVal] += v
	}
	return out
}

// cutMetric 切出指标名与其余部分。
func cutMetric(line string) (name, rest string, ok bool) {
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i:], true
}

// labelValue label 文本里取指定键的值（naive 解析，prom 文本足够）。
func labelValue(labels, key string) string {
	for _, part := range strings.Split(labels, ",") {
		part = strings.TrimSpace(part)
		if kv, ok := strings.CutPrefix(part, key+`="`); ok {
			if v, ok2 := strings.CutSuffix(kv, `"`); ok2 {
				return v
			}
		}
	}
	return ""
}

// LoadEvalTrend 读评测基线报告（P2-02 artifact，eval/results/baseline-latest.json）；
// 文件不存在返回 nil（趋势面缺位属常态，不构成控制台错误）。
func LoadEvalTrend(path string) *EvalTrend {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var t EvalTrend
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil
	}
	return &t
}
