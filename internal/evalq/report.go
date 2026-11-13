package evalq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

// Options 评测跑批配置。
type Options struct {
	// Model 模型标识（报告标注；A/B 对照的组键，与 eval-real 的 model 标签同面）。
	Model string
	// Params 固定参数快照（温度/路由/prompt 版本……可重复跑批的锚）。
	Params map[string]string
	// StabilityRuns 裁决稳定性跑批轮数（0/1 = 不跑稳定性）。
	StabilityRuns int
}

// LanguageReport 分语言指标（issue #25「分仓/分语言/分严重度」的语言维度落地；
// 分严重度钻取由 CaseResult 逐条数据支持）。
type LanguageReport struct {
	Language string  `json:"language"`
	Metrics  Metrics `json:"metrics"`
}

// Report 评测报告（JSON/Markdown 双形态，eval/results artifact 面）。
type Report struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Model       string            `json:"model"`
	Params      map[string]string `json:"params,omitempty"`
	Cases       int               `json:"cases"`
	Errored     int               `json:"errored"`
	Overall     Metrics           `json:"overall"`
	ByLanguage  []LanguageReport  `json:"by_language"`
	// BySeverity 分严重度聚合面（issue #25「分严重度报表」：期望/实际上报/TP·FP·FN
	// 分档）——ScoreSeverity(cases, results)，词表行恒出。
	BySeverity []SeverityReport `json:"by_severity"`
	Stability  float64          `json:"stability"`
	Runs       int              `json:"stability_runs"`
	Results    []CaseResult     `json:"results"`
}

// Evaluate 评测跑批：逐案调 Reviewer（串行——真实模型限速与 token 成本可控，
// 与 eval-real 的 serial 口径一致）、计分、分语言聚合；StabilityRuns>1 时对
// 全集重复 k 轮产出裁决稳定性。出错样例保守计分（verdict 不命中 + 漏检），
// Errored 单列——评测失败也是数据（对比是全量数据，不只 pass/fail 信号）。
func Evaluate(ctx context.Context, cases []Case, rev Reviewer, opts Options) (*Report, error) {
	if rev == nil {
		return nil, fmt.Errorf("evalq: Reviewer 未装配（真实模型接入点）")
	}
	firstPass := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		out, err := rev.Review(ctx, c)
		firstPass = append(firstPass, ScoreResult(c, out, err))
	}

	rep := &Report{
		GeneratedAt: time.Now(),
		Model:       opts.Model,
		Params:      opts.Params,
		Cases:       len(cases),
		Overall:     Score(firstPass),
		Results:     firstPass,
	}
	for _, r := range firstPass {
		if r.Err != "" {
			rep.Errored++
		}
	}

	// 分语言聚合
	byLang := map[string][]CaseResult{}
	for _, r := range firstPass {
		byLang[r.Language] = append(byLang[r.Language], r)
	}
	langs := make([]string, 0, len(byLang))
	for lang := range byLang {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		rep.ByLanguage = append(rep.ByLanguage, LanguageReport{Language: lang, Metrics: Score(byLang[lang])})
	}

	// 分严重度聚合（与分语言同批：首轮结果 + 全量 cases 配对）
	rep.BySeverity = ScoreSeverity(cases, firstPass)

	// 裁决稳定性：k 轮重复跑批（第 1 轮复用 firstPass 结果，省一半成本）
	if opts.StabilityRuns > 1 {
		runs := []map[string]string{verdictMap(firstPass)}
		for range opts.StabilityRuns - 1 {
			rerun := make([]CaseResult, 0, len(cases))
			for _, c := range cases {
				out, err := rev.Review(ctx, c)
				rerun = append(rerun, ScoreResult(c, out, err))
			}
			runs = append(runs, verdictMap(rerun))
		}
		rep.Stability = Stability(runs)
		rep.Runs = opts.StabilityRuns
	} else {
		rep.Stability = 1
	}
	return rep, nil
}

// verdictMap 单轮 caseID → verdict（Stability 输入形态）。
func verdictMap(results []CaseResult) map[string]string {
	m := make(map[string]string, len(results))
	for _, r := range results {
		m[r.CaseID] = r.Outcome.Verdict
	}
	return m
}

// WriteJSON 机器可读报告。
func (rep *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// WriteMarkdown 人读报告：总指标 + 分语言表 + 分严重度表 + 逐案明细。
func (rep *Report) WriteMarkdown(w io.Writer) error {
	o := rep.Overall
	head := fmt.Sprintf(`# 基准集质量评测报告

- 生成时间：%s
- 模型：%s（参数：%s）
- 样例：%d（出错 %d）%s

## 总指标

| 指标 | 值 |
|---|---|
| Verdict 命中率 | %.1f%% |
| Precision | %.3f |
| Recall | %.3f |
| 误报率（FP Rate） | %.3f |
| 漏检率（Miss Rate） | %.3f |
| F1 | %.3f |
| 裁决稳定性（%d 轮） | %.3f |

混淆矩阵（finding 级）：TP=%d FP=%d FN=%d TN=%d

## 分语言指标

| 语言 | 样例 | Verdict 命中 | Precision | Recall | 误报率 | 漏检率 |
|---|---|---|---|---|---|---|
`,
		rep.GeneratedAt.Format(time.RFC3339), rep.Model, paramsWord(rep.Params),
		rep.Cases, rep.Errored, erroredNote(rep.Errored),
		100*o.VerdictAccuracy, o.Precision, o.Recall, o.FPRate, o.MissRate, o.F1,
		max(rep.Runs, 1), rep.Stability,
		o.Confusion.TP, o.Confusion.FP, o.Confusion.FN, o.Confusion.TN,
	)
	if _, err := fmt.Fprint(w, head); err != nil {
		return err
	}
	for _, lr := range rep.ByLanguage {
		m := lr.Metrics
		line := fmt.Sprintf("| %s | %d | %.1f%% | %.3f | %.3f | %.3f | %.3f |\n",
			lr.Language, m.Cases, 100*m.VerdictAccuracy, m.Precision, m.Recall, m.FPRate, m.MissRate)
		if _, err := fmt.Fprint(w, line); err != nil {
			return err
		}
	}
	// 分严重度表（issue #25「分严重度报表」：期望条数/实际上报/TP·FP·FN 分档，
	// 词表行恒出、零分母置 0）
	if _, err := fmt.Fprint(w, "\n## 分严重度指标\n\n| 严重度 | 期望条数 | 实际上报 | TP | FP | FN | Precision | Recall | 漏检率 |\n|---|---|---|---|---|---|---|---|---|\n"); err != nil {
		return err
	}
	for _, s := range rep.BySeverity {
		line := fmt.Sprintf("| %s | %d | %d | %d | %d | %d | %.3f | %.3f | %.3f |\n",
			mdCell(s.Severity), s.Expected, s.Reported, s.TP, s.FP, s.FN,
			s.Precision, s.Recall, s.MissRate)
		if _, err := fmt.Fprint(w, line); err != nil {
			return err
		}
	}
	// 逐案明细（钻取面：漏检/误报归因到样例与类别词）
	if _, err := fmt.Fprint(w, "\n## 逐案明细\n\n| 样例 | 语言 | 标签 | Verdict 命中 | TP | FN | FP | 漏检类别 | 误报类别 | 错误 |\n|---|---|---|---|---|---|---|---|---|---|\n"); err != nil {
		return err
	}
	for _, r := range rep.Results {
		line := fmt.Sprintf("| %s | %s | %s | %t | %d | %d | %d | %s | %s | %s |\n",
			r.CaseID, r.Language, r.Label, r.VerdictHit, len(r.TPs), len(r.FNs), len(r.FPs),
			mdCell(strings.Join(r.MissedKinds, ", ")), mdCell(strings.Join(r.FPKinds, ", ")), mdCell(r.Err))
		if _, err := fmt.Fprint(w, line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n> 口径：clean 基准上报意见全部计误报（golden 北极星口径）；must_find 未命中计漏检；\n> 裁决稳定性 = k 轮同输入裁决的逐案一致率均值（internal/evalq.Stability）。\n")
	return err
}

func paramsWord(params map[string]string) string {
	if len(params) == 0 {
		return "缺省"
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, ", ")
}

func erroredNote(errored int) string {
	if errored == 0 {
		return ""
	}
	return "，出错样例保守计分（verdict 不命中 + 记漏检）"
}

// mdCell 表格单元格转义（竖线与换行）。
func mdCell(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}
