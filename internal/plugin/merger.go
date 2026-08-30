package plugin

import (
	"context"
	"sort"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
)

// PrintStore 指纹持久化的最小接口（由 storage.Store 实现；Merger 不 import storage，
// 避免 plugin ↔ storage 循环依赖）。
type PrintStore interface {
	// GetPrintState 返回既有指纹状态；不存在返回 (nil, nil)。
	GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*PrintState, error)
	// UpsertPrint 跨轮次合并入库；返回该指纹当前是否 suppressed（true 时本轮报告剔除该条）。
	UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
		f Finding, taskID string, at time.Time) (suppressed bool, err error)
}

// PrintState 既有指纹状态。
type PrintState struct {
	Status    string // open | suppressed
	Line      int
	Severity  string
	TimesSeen int
}

// MergedFindings Merger 产出。
type MergedFindings struct {
	Findings   []Finding // 归一化 + 轮内去重 + 抑制剔除后，severity 降序
	Suppressed int       // 因误报反馈被抑制的条数（报告脚注提示）
	Degraded   []string  // 降级注明（插件失败/超时/裁剪）
}

// Merger 合并器（design §3.3，全部确定性）：
//  1. severity 入口归一化（越界归 low + 告警）；
//  2. 轮内精确指纹去重（规范化评论文本哈希 + file；冲突 severity 取高）；
//  3. 跨轮次指纹入库/更新，suppressed 的不进报告；
//  4. 插件失败/跳过 → 降级注明，不拖垮整体。
type Merger struct {
	store PrintStore
	log   logx.Logger
}

func NewMerger(store PrintStore, log logx.Logger) *Merger {
	return &Merger{store: store, log: log}
}

// Merge 执行合并。pluginNameOf 提供 finding 归属（ Outcome 遍历时注入）。
func (m *Merger) Merge(ctx context.Context, pr PRContext, taskID string, outcomes []Outcome, at time.Time) (*MergedFindings, error) {
	out := &MergedFindings{}
	type acc struct {
		finding Finding
		bys     map[string]bool
	}
	byFP := map[string]*acc{}
	var order []string

	for _, o := range outcomes {
		if note := o.Notes(); note != "" {
			out.Degraded = append(out.Degraded, note)
		}
		if o.Result == nil {
			continue
		}
		for _, f := range o.Result.Findings {
			sev, ok := NormalizeSeverity(f.Severity)
			if !ok {
				m.log.Warn("argus.merger.severity_unknown",
					"plugin", o.Name, "raw", f.Severity, "mapped", sev)
			}
			f.Severity = sev
			f.By = o.Name
			obsFindings(sev, o.Name, 1)

			fp := Fingerprint(f.File, f.Comment)
			if a, dup := byFP[fp]; dup {
				// 轮内去重：severity 冲突取高；行号 ±2 内采纳新行号（匹配辅助）；
				// by 追加来源插件。
				a.finding.Severity = HigherSeverity(a.finding.Severity, f.Severity)
				if abs(a.finding.Line-f.Line) <= 2 && f.Line > 0 {
					a.finding.Line = f.Line
				}
				a.bys[f.By] = true
				continue
			}
			byFP[fp] = &acc{finding: f, bys: map[string]bool{f.By: true}}
			order = append(order, fp)
		}
	}

	// 跨轮次入库与抑制过滤
	for _, fp := range order {
		a := byFP[fp]
		f := a.finding
		if len(a.bys) > 1 { // 多插件报了同一问题：by 合并展示
			var names []string
			for n := range a.bys {
				names = append(names, n)
			}
			sort.Strings(names)
			f.By = joinNames(names)
		}
		if m.store != nil {
			suppressed, err := m.store.UpsertPrint(ctx,
				pr.Platform, pr.Owner, pr.Repo, pr.Number, fp, f, taskID, at)
			if err != nil {
				m.log.Error("argus.merger.print_upsert_failed", "error", err.Error())
			} else if suppressed {
				out.Suppressed++
				continue
			}
		}
		out.Findings = append(out.Findings, f)
	}

	sort.SliceStable(out.Findings, func(i, j int) bool {
		if SeverityRank(out.Findings[i].Severity) != SeverityRank(out.Findings[j].Severity) {
			return SeverityRank(out.Findings[i].Severity) > SeverityRank(out.Findings[j].Severity)
		}
		if out.Findings[i].File != out.Findings[j].File {
			return out.Findings[i].File < out.Findings[j].File
		}
		return out.Findings[i].Line < out.Findings[j].Line
	})
	return out, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		out := names[0]
		for _, n := range names[1:] {
			out += "," + n
		}
		return out
	}
}
