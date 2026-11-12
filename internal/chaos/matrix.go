package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"
)

// Cell 矩阵格：一类故障 × 一个注入点及其期望恢复语义（issue #24 验收口径
// 「故障场景矩阵每格至少一条脚本化回归」的机器可读形态）。
type Cell struct {
	Family Family `json:"family"`
	Kind   Kind   `json:"kind"`
	// Point 注入点（db.claim / db.finish / db.write / llm.review / git.api /
	// worker.process …）——test/chaos 逐格落回归时按此对位。
	Point string `json:"point"`
	// Recovery 期望恢复语义（人类可读，报告逐格呈现）。
	Recovery string `json:"recovery"`
}

// Matrix 规范矩阵：五族故障 × 注入点。变更矩阵 = 特性级契约变更，须同步
// test/chaos 的对位回归与 issue #24 验收清单。
func Matrix() []Cell {
	return []Cell{
		{
			Family: FamilyProcessKill, Kind: KillTaskWorker, Point: "worker.process",
			Recovery: "kill -9 后心跳冻结，staleAfter 窗口内 ResetRunningToPending 复位回 pending，可被重新认领执行",
		},
		{
			Family: FamilyNetwork, Kind: NetGitUnreachable, Point: "git.api",
			Recovery: "Git API 不可达为瞬态故障：有界重试；恢复后同调用成功，重试次数有上界（不 hammer）",
		},
		{
			Family: FamilyNetwork, Kind: NetLLMUnreachable, Point: "llm.review",
			Recovery: "LLM 网关不可达为瞬态故障：任务按失败分类落 fail_reason（llm unreachable），ResetFailedForRetry 退避后复活",
		},
		{
			Family: FamilyLeaseExpiry, Kind: LeaseTaskExpired, Point: "db.finish",
			Recovery: "租约过期复位后，旧执行体 FinishTaskFenced 被 fencing 拒绝（不覆盖新执行体结果），新执行体正常落终态",
		},
		{
			Family: FamilyRateLimit, Kind: GitRateLimited, Point: "git.api",
			Recovery: "Git API 429 为瞬态故障：退避重试至成功；非瞬态故障（进程死亡）不重试（fail-fast）",
		},
		{
			Family: FamilyLLM, Kind: LLMTimeout, Point: "llm.review",
			Recovery: "LLM 超时为瞬态故障：有界重试；恢复后产出结构化结果",
		},
		{
			Family: FamilyLLM, Kind: LLMRateLimited, Point: "llm.review",
			Recovery: "LLM 429 为瞬态故障：退避重试至成功",
		},
		{
			Family: FamilyLLM, Kind: LLMEmptyOutput, Point: "llm.review",
			Recovery: "空输出为契约故障（非瞬态）：不盲目重试，任务按失败分类落 fail_reason，ResetFailedForRetry 退避复活",
		},
		{
			Family: FamilyLLM, Kind: LLMMalformedOutput, Point: "llm.review",
			Recovery: "非结构化输出为契约故障（非瞬态）：不盲目重试，按失败分类处理",
		},
		{
			Family: FamilyDBTimeout, Kind: DBWriteTimeout, Point: "db.write",
			Recovery: "写路径超时/回滚后重试不产生重复任务（webhook event_id 幂等键去重兜底）",
		},
	}
}

// Probe 一格矩阵的可执行体：注入故障→驱动被测路径→断言恢复语义。
// 返回 nil = 恢复语义成立；返回 error = 该格回归失败（报告记录）。
type Probe func(ctx context.Context) error

// Scenario 一格的实弹对位：Cell + Probe。
type Scenario struct {
	Cell  Cell  `json:"cell"`
	Probe Probe `json:"-"`
}

// Scenario 构造器（可读性：NewScenario(cell, probe)）。
func NewScenario(c Cell, p Probe) Scenario { return Scenario{Cell: c, Probe: p} }

// CellResult 单格执行结果（报告数据源）。
type CellResult struct {
	Family   Family `json:"family"`
	Kind     Kind   `json:"kind"`
	Point    string `json:"point"`
	Recovery string `json:"recovery"`
	Status   string `json:"status"` // pass | fail
	Error    string `json:"error,omitempty"`
	TookMs   int64  `json:"took_ms"`
}

// Status 词表。
const (
	StatusPass = "pass"
	StatusFail = "fail"
)

// Report 混沌矩阵执行报告（JSON/Markdown 双形态；CI artifact 面与 golden
// eval results 同款约定——结果文件全量归档，不只留 pass/fail 信号）。
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Total       int          `json:"total"`
	Passed      int          `json:"passed"`
	Failed      int          `json:"failed"`
	Cells       []CellResult `json:"cells"`
}

// Pass 全格通过。
func (r *Report) Pass() bool { return r != nil && r.Failed == 0 && r.Total > 0 }

// WriteJSON 输出机器可读报告。
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteMarkdown 输出人读报告（矩阵表格：族/故障/注入点/结果/耗时/错误）。
func (r *Report) WriteMarkdown(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "# 混沌测试矩阵报告\n\n- 生成时间：%s\n- 总格数：%d，通过：%d，失败：%d\n- 结论：%s\n\n",
		r.GeneratedAt.Format(time.RFC3339), r.Total, r.Passed, r.Failed, verdictWord(r.Pass()),
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "| 故障族 | 故障 | 注入点 | 期望恢复语义 | 结果 | 耗时(ms) | 错误 |\n|---|---|---|---|---|---|---|\n"); err != nil {
		return err
	}
	for _, c := range r.Cells {
		line := fmt.Sprintf("| %s | %s | %s | %s | %s | %d | %s |\n",
			c.Family, c.Kind, c.Point, c.Recovery, c.Status, c.TookMs, mdEscape(c.Error))
		if _, err := fmt.Fprint(w, line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n> 注入点抽象：internal/chaos.Injector；实弹回归：test/chaos。\n")
	return err
}

// Run 逐格串行执行（故障注入不并发混线），失败不短路——混沌报告要全量数据。
// 单格 panic 按失败捕获（混沌用例自身不许炸掉整个矩阵）。
func Run(ctx context.Context, scenarios []Scenario) *Report {
	rep := &Report{GeneratedAt: time.Now(), Total: len(scenarios)}
	for _, sc := range scenarios {
		res := CellResult{
			Family:   sc.Cell.Family,
			Kind:     sc.Cell.Kind,
			Point:    sc.Cell.Point,
			Recovery: sc.Cell.Recovery,
			Status:   StatusPass,
		}
		start := time.Now()
		if sc.Probe == nil {
			res.Status = StatusFail
			res.Error = "probe 未装配（矩阵格缺实弹回归）"
		} else {
			res.Error = runProbe(ctx, sc)
			if res.Error != "" {
				res.Status = StatusFail
			}
		}
		res.TookMs = time.Since(start).Milliseconds()
		if res.Status == StatusPass {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Cells = append(rep.Cells, res)
	}
	return rep
}

// runProbe 带 panic 防护执行单格，失败返回错误描述（成功返回空串）。
func runProbe(ctx context.Context, sc Scenario) (errMsg string) {
	defer func() {
		if r := recover(); r != nil {
			errMsg = fmt.Sprintf("probe panic: %v", r)
		}
	}()
	if err := sc.Probe(ctx); err != nil {
		return err.Error()
	}
	return ""
}

// MissingCells 矩阵覆盖检查：scenarios 相对规范矩阵缺的格（test/chaos 验收门
// ——「每格至少一条脚本化回归」的机器断言）。
func MissingCells(scenarios []Scenario) []Cell {
	have := make(map[Kind]bool, len(scenarios))
	for _, sc := range scenarios {
		have[sc.Cell.Kind] = true
	}
	var missing []Cell
	for _, c := range Matrix() {
		if !have[c.Kind] {
			missing = append(missing, c)
		}
	}
	return missing
}

// ExtraCells 越格检查：scenarios 里不在规范矩阵内的格（打错字/矩阵改名后
// 的孤儿回归，显式暴露而不是静默漂移）。
func ExtraCells(scenarios []Scenario) []Cell {
	valid := make(map[Kind]bool)
	for _, c := range Matrix() {
		valid[c.Kind] = true
	}
	var extra []Cell
	for _, sc := range scenarios {
		if !valid[sc.Cell.Kind] {
			extra = append(extra, sc.Cell)
		}
	}
	return extra
}

// verdictWord 结论词。
func verdictWord(pass bool) string {
	if pass {
		return "全绿（恢复语义全部成立）"
	}
	return "存在失败格（见下表）"
}

// mdEscape 表格单元格内的竖线转义。
func mdEscape(s string) string {
	if !slices.Contains([]rune(s), '|') {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '|' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}
