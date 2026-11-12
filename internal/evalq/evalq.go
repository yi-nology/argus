// Package evalq 真实模型质量评测体系（P2-02，#25）：Precision/Recall/误报率/
// 漏检率/裁决稳定性的指标计算单源 + 基准集装载 + 评测跑批骨架 + 报告输出。
//
// 分层与边界：
//   - 本包 = 纯指标与编排骨架（L0，零 internal 依赖）；真实模型调用只留
//     Reviewer 接口（接入点），不内置任何 LLM 客户端——依赖政策红线
//     （评测变异不影响在线链路，与 internal/obs evalmetrics 的旁路形态一致）；
//   - eval/baseline/ = 分语言基准集（P3-03 #28 落地，本包装载消费）；
//   - test/evalq = 指标单测 + 数据集完整性门禁 + 打桩跑批回归；报告落
//     eval/results/（CI artifact 面）。
//
// 指标口径（与 golden eval 的 verdict 命中率互补，issue #25 审计原文）：
//   - finding 级混淆矩阵：must_find 命中=TP，未命中=FN（漏检）；
//   - clean 基准（must_find 空）上报的意见全部计 FP（误报，golden 北极星口径）；
//   - must_not_find 命中的实际上报意见亦计 FP；
//   - 裁决稳定性：k 轮同输入裁决的逐案一致率均值。
package evalq

import (
	"context"
	"strings"
)

// Verdict 裁决词表（与任务 verdict 同面）。
const (
	VerdictApprove        = "approve"
	VerdictComment        = "comment"
	VerdictRequestChanges = "request_changes"
)

// CaseLabel 基准样例标签：standard 常规已裁决样例；known_miss 已知漏检正例
// （金标准认定有缺陷、历史模型漏报）；known_fp 已知误报正例（历史模型在干净
// diff 上报出的意见，金标准认定无缺陷）。
const (
	LabelStandard  = "standard"
	LabelKnownMiss = "known_miss"
	LabelKnownFP   = "known_fp"
)

// FindingExpect 期望意见（金标准标注的最小匹配面：类别必填、文件可选收窄）。
type FindingExpect struct {
	File     string `yaml:"file,omitempty"`
	Kind     string `yaml:"kind"`
	Severity string `yaml:"severity,omitempty"`
}

// Expectation 金标准期望。
type Expectation struct {
	Verdict     string          `yaml:"verdict"`
	MustFind    []FindingExpect `yaml:"must_find,omitempty"`
	MustNotFind []FindingExpect `yaml:"must_not_find,omitempty"`
}

// Case 基准样例：一条已裁决 MR 样例（diff + 金标准）。
type Case struct {
	ID       string `yaml:"id"`
	Language string `yaml:"language"`
	// Repo 样例来源仓（现网在役仓，审计口径「现网在役仓的已裁决 MR 样例」）。
	Repo  string `yaml:"repo"`
	Title string `yaml:"title"`
	// Label standard|known_miss|known_fp（词表见常量）。
	Label string `yaml:"label"`
	// Source 样例出处（MR 号/裁决链接），审计可回溯。
	Source string      `yaml:"source,omitempty"`
	Diff   string      `yaml:"diff"`
	Expect Expectation `yaml:"expect"`
}

// Finding 实际产出意见（Reviewer 返回的归一化形态）。
type Finding struct {
	File     string `json:"file"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
}

// Outcome 单次审查产出（Reviewer 的返回值）。
type Outcome struct {
	Verdict    string    `json:"verdict"`
	Findings   []Finding `json:"findings,omitempty"`
	TokensUsed int       `json:"tokens_used,omitempty"`
}

// Reviewer 真实模型调用接入点（骨架不内置实现）：评测跑批面向此接口，
// 真实模型/打桩实现各自装配。k 次稳定性跑批由 Evaluate 的 Repeat 选项承担。
type Reviewer interface {
	Review(ctx context.Context, c Case) (Outcome, error)
}

// ReviewerFunc 适配器。
type ReviewerFunc func(ctx context.Context, c Case) (Outcome, error)

// Review 实现 Reviewer。
func (f ReviewerFunc) Review(ctx context.Context, c Case) (Outcome, error) { return f(ctx, c) }

// CaseResult 单样例评测结果（计分与报告的数据源）。
type CaseResult struct {
	CaseID   string  `json:"case_id"`
	Language string  `json:"language"`
	Label    string  `json:"label"`
	Outcome  Outcome `json:"outcome"`
	Err      string  `json:"error,omitempty"`
	// VerdictHit verdict 与金标准一致。
	VerdictHit bool `json:"verdict_hit"`
	// TPs 命中的 must_find 下标；FNs 漏检下标；FPs 误报意见。
	TPs []int     `json:"tps,omitempty"`
	FNs []int     `json:"fns,omitempty"`
	FPs []Finding `json:"fps,omitempty"`
	// MissedKinds / FPKinds 钻取面：漏检与误报的类别词（报告逐案明细直读，
	// 不必回查金标准）。
	MissedKinds []string `json:"missed_kinds,omitempty"`
	FPKinds     []string `json:"fp_kinds,omitempty"`
}

// Confusion finding 级混淆计数。
type Confusion struct {
	TP int `json:"tp"`
	FP int `json:"fp"`
	FN int `json:"fn"`
	TN int `json:"tn"`
}

// Metrics 评测指标集（issue #25 审计口径全量）。
type Metrics struct {
	Cases      int       `json:"cases"`
	VerdictHit int       `json:"verdict_hit"`
	Confusion  Confusion `json:"confusion"`
	// Precision TP/(TP+FP)；分母 0 时置 0（无样本不算满贯）。
	Precision float64 `json:"precision"`
	// Recall TP/(TP+FN)。
	Recall float64 `json:"recall"`
	// FPRate FP/(FP+TN)：干净基准上的误报率。
	FPRate float64 `json:"fp_rate"`
	// MissRate FN/(TP+FN)：严重缺陷漏检率（known_miss 正例的漏报占比）。
	MissRate float64 `json:"miss_rate"`
	F1       float64 `json:"f1"`
	// VerdictAccuracy verdict 命中率（golden eval 原口径，保持可比）。
	VerdictAccuracy float64 `json:"verdict_accuracy"`
}

// matchFinding 匹配规则：kind 相等（大小写不敏感）且（期望未收窄文件 或
// 文件相等）。文件收窄是可选强化条件，kind 是主键。
func matchFinding(f Finding, e FindingExpect) bool {
	if !strings.EqualFold(strings.TrimSpace(f.Kind), strings.TrimSpace(e.Kind)) {
		return false
	}
	if strings.TrimSpace(e.File) == "" {
		return true
	}
	return strings.TrimSpace(f.File) == strings.TrimSpace(e.File)
}

// ScoreResult 对单样例计分（混淆矩阵归因与报告钻取共用）。
func ScoreResult(c Case, out Outcome, err error) CaseResult {
	res := CaseResult{CaseID: c.ID, Language: c.Language, Label: c.Label}
	if err != nil {
		res.Err = err.Error()
		// 出错样例保守计：verdict 不命中、全部 must_find 记漏检
		res.FNs = make([]int, len(c.Expect.MustFind))
		for i, e := range c.Expect.MustFind {
			res.FNs[i] = i
			res.MissedKinds = append(res.MissedKinds, e.Kind)
		}
		return res
	}
	res.Outcome = out
	res.VerdictHit = strings.EqualFold(strings.TrimSpace(out.Verdict), strings.TrimSpace(c.Expect.Verdict))
	matched := make([]bool, len(out.Findings))
	for i, e := range c.Expect.MustFind {
		hit := false
		for j, f := range out.Findings {
			if !matched[j] && matchFinding(f, e) {
				matched[j] = true
				hit = true
				break
			}
		}
		if hit {
			res.TPs = append(res.TPs, i)
		} else {
			res.FNs = append(res.FNs, i)
			res.MissedKinds = append(res.MissedKinds, e.Kind)
		}
	}
	// FP 口径：clean 基准（无 must_find）上的全部实际上报 + must_not_find 命中。
	clean := len(c.Expect.MustFind) == 0
	for j, f := range out.Findings {
		if matched[j] {
			continue
		}
		forbidden := false
		for _, nf := range c.Expect.MustNotFind {
			if matchFinding(f, nf) {
				forbidden = true
				break
			}
		}
		if clean || forbidden {
			res.FPs = append(res.FPs, f)
			res.FPKinds = append(res.FPKinds, f.Kind)
		}
	}
	return res
}

// Score 全量计分（单次跑批的指标面）。
func Score(results []CaseResult) Metrics {
	var m Metrics
	for _, r := range results {
		m.Cases++
		if r.VerdictHit {
			m.VerdictHit++
		}
		m.Confusion.TP += len(r.TPs)
		m.Confusion.FN += len(r.FNs)
		m.Confusion.FP += len(r.FPs)
	}
	// TN：干净基准（must_find 空）且实际上报为零的样例数。
	for _, r := range results {
		if r.Err == "" && len(r.FPs) == 0 && len(r.TPs) == 0 && len(r.FNs) == 0 {
			m.Confusion.TN++
		}
	}
	m.Precision = ratio(m.Confusion.TP, m.Confusion.TP+m.Confusion.FP)
	m.Recall = ratio(m.Confusion.TP, m.Confusion.TP+m.Confusion.FN)
	m.FPRate = ratio(m.Confusion.FP, m.Confusion.FP+m.Confusion.TN)
	m.MissRate = ratio(m.Confusion.FN, m.Confusion.TP+m.Confusion.FN)
	m.F1 = f1(m.Precision, m.Recall)
	if m.Cases > 0 {
		m.VerdictAccuracy = float64(m.VerdictHit) / float64(m.Cases)
	}
	return m
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func f1(p, r float64) float64 {
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}
