// compare.go —— 影响面演化对比（v5.8.1）：同一 MR 上一轮快照 vs 本轮，
// 纯函数集合差（节点 ID / 边三元组 / 函数级变更键），供交互图页头演化
// 摘要与 MCP 数据面消费。
//
// 语义口径：节点按 ID 精确匹配（fd:/sym: 等合成节点不在快照内,天然不参与）；
// 边按 From|To|Rel 三元组；FuncDiff 按 file:name 键。风险分只搬运前后值，
// 不做涨跌判定（理由链在各自 Risk.Reasons,由读者对照）。

package impactview

import (
	"html"
	"strconv"
	"strings"
)

// ImpactCompare 两轮快照的集合差（JSON 面;零差异时各切片为 nil）。
type ImpactCompare struct {
	PrevTaskID string `json:"prev_task_id"`
	RiskBefore Risk   `json:"risk_before"`
	RiskAfter  Risk   `json:"risk_after"`
	// AddedNodes 本轮新出现的影响节点（新波及面）
	AddedNodes []Node `json:"added_nodes,omitempty"`
	// RemovedNodes 上一轮有、本轮消失的影响节点(波及面收敛/文件回退)
	RemovedNodes []Node `json:"removed_nodes,omitempty"`
	AddedEdges   []Edge `json:"added_edges,omitempty"`
	RemovedEdges []Edge `json:"removed_edges,omitempty"`
	// NewFuncDiffs 本轮新出现的函数级变更键(file:name)
	NewFuncDiffs []string `json:"new_func_diffs,omitempty"`
	// ResolvedFuncDiffs 上一轮有、本轮消失的函数级变更(已恢复/文件回退)
	ResolvedFuncDiffs []string `json:"resolved_func_diffs,omitempty"`
	PrevNodes         int      `json:"prev_nodes"`
	CurNodes          int      `json:"cur_nodes"`
	PrevEdges         int      `json:"prev_edges"`
	CurEdges          int      `json:"cur_edges"`
}

// CompareSnapshots 两快照集合差。prev 为上一轮(nil 时返回 nil=无对比面)。
func CompareSnapshots(prev, cur *Snapshot) *ImpactCompare {
	if prev == nil || cur == nil {
		return nil
	}
	ic := &ImpactCompare{
		RiskBefore: prev.Risk,
		RiskAfter:  cur.Risk,
		PrevNodes:  len(prev.Nodes),
		CurNodes:   len(cur.Nodes),
		PrevEdges:  len(prev.Edges),
		CurEdges:   len(cur.Edges),
	}
	prevNodes := map[string]Node{}
	for _, n := range prev.Nodes {
		prevNodes[n.ID] = n
	}
	curNodes := map[string]Node{}
	for _, n := range cur.Nodes {
		curNodes[n.ID] = n
	}
	for _, n := range cur.Nodes {
		if _, ok := prevNodes[n.ID]; !ok {
			ic.AddedNodes = append(ic.AddedNodes, n)
		}
	}
	for _, n := range prev.Nodes {
		if _, ok := curNodes[n.ID]; !ok {
			ic.RemovedNodes = append(ic.RemovedNodes, n)
		}
	}
	prevEdges := map[Edge]bool{}
	for _, e := range prev.Edges {
		prevEdges[e] = true
	}
	curEdges := map[Edge]bool{}
	for _, e := range cur.Edges {
		curEdges[e] = true
	}
	for _, e := range cur.Edges {
		if !prevEdges[e] {
			ic.AddedEdges = append(ic.AddedEdges, e)
		}
	}
	for _, e := range prev.Edges {
		if !curEdges[e] {
			ic.RemovedEdges = append(ic.RemovedEdges, e)
		}
	}
	fdKey := func(fd FuncDiff) string { return fd.File + ":" + fd.Name }
	prevFD := map[string]bool{}
	for _, fd := range prev.FuncDiffs {
		prevFD[fdKey(fd)] = true
	}
	curFD := map[string]bool{}
	for _, fd := range cur.FuncDiffs {
		curFD[fdKey(fd)] = true
	}
	for _, fd := range cur.FuncDiffs {
		if !prevFD[fdKey(fd)] {
			ic.NewFuncDiffs = append(ic.NewFuncDiffs, fdKey(fd))
		}
	}
	for _, fd := range prev.FuncDiffs {
		if !curFD[fdKey(fd)] {
			ic.ResolvedFuncDiffs = append(ic.ResolvedFuncDiffs, fdKey(fd))
		}
	}
	return ic
}

// compareSummary 演化对比摘要块（v5.8.1,HTML 页头）：风险分涨跌 + 波及面
// 增减计数 + 可展开明细。cmp=nil 返回空串（无对比面）。
func compareSummary(cmp *ImpactCompare) string {
	if cmp == nil {
		return ""
	}
	delta := cmp.RiskAfter.Score - cmp.RiskBefore.Score
	trend, cls := "持平", "#64748b"
	switch {
	case delta > 0:
		trend, cls = "↑ +"+strconv.Itoa(delta), "#dc2626"
	case delta < 0:
		trend, cls = "↓ "+strconv.Itoa(delta), "#16a34a"
	}
	var b strings.Builder
	b.WriteString(`<div style="margin:8px 20px;padding:8px 12px;border:1px solid #e2e8f0;border-radius:8px;background:#fff;font-size:13px">`)
	b.WriteString(`<b>📈 对比上一轮</b>（task ` + html.EscapeString(cmp.PrevTaskID) + `）：风险 ` +
		strconv.Itoa(cmp.RiskBefore.Score) + `→` + strconv.Itoa(cmp.RiskAfter.Score) +
		` <b style="color:` + cls + `">` + trend + `</b>`)
	b.WriteString(` · 节点 ` + strconv.Itoa(cmp.PrevNodes) + `→` + strconv.Itoa(cmp.CurNodes) +
		` · 边 ` + strconv.Itoa(cmp.PrevEdges) + `→` + strconv.Itoa(cmp.CurEdges))
	b.WriteString(`（<details style="display:inline"><summary style="display:inline;cursor:pointer;color:#2563eb">明细</summary><div style="margin-top:6px;font-size:12px">`)
	list := func(title string, items []Node) {
		if len(items) == 0 {
			return
		}
		b.WriteString(`<div><b>` + title + ` ` + strconv.Itoa(len(items)) + `</b></div>`)
		for _, n := range items {
			b.WriteString(`<div style="padding-left:12px">` + html.EscapeString(n.Kind+" "+n.Label) + `</div>`)
		}
	}
	list("新增波及", cmp.AddedNodes)
	list("移除波及", cmp.RemovedNodes)
	if len(cmp.NewFuncDiffs) > 0 {
		b.WriteString(`<div><b>新增函数级变更 ` + strconv.Itoa(len(cmp.NewFuncDiffs)) + `</b></div>`)
		for _, k := range cmp.NewFuncDiffs {
			b.WriteString(`<div style="padding-left:12px">` + html.EscapeString(k) + `</div>`)
		}
	}
	if len(cmp.ResolvedFuncDiffs) > 0 {
		b.WriteString(`<div><b>已恢复函数级变更 ` + strconv.Itoa(len(cmp.ResolvedFuncDiffs)) + `</b></div>`)
		for _, k := range cmp.ResolvedFuncDiffs {
			b.WriteString(`<div style="padding-left:12px">` + html.EscapeString(k) + `</div>`)
		}
	}
	if len(cmp.AddedEdges) > 0 || len(cmp.RemovedEdges) > 0 {
		b.WriteString(`<div><b>边变化</b> +` + strconv.Itoa(len(cmp.AddedEdges)) + ` / −` + strconv.Itoa(len(cmp.RemovedEdges)) + `</div>`)
	}
	if len(cmp.AddedNodes)+len(cmp.RemovedNodes)+len(cmp.NewFuncDiffs)+len(cmp.ResolvedFuncDiffs)+len(cmp.AddedEdges)+len(cmp.RemovedEdges) == 0 {
		b.WriteString(`<div style="color:#64748b">影响面与上一轮一致</div>`)
	}
	b.WriteString(`</div></details>）</div>`)
	return b.String()
}
