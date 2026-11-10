// graph_query.go —— 符号邻接图的查询面（v5.9.0）：命中附邻接、单符号上下文、
// 两符号最短调用链。图由 Refresh 收尾恒重建（graph.go），查询只读且持 ix.mu；
// 容量封顶与关系词表同在 graph.go，此处不重复定义。
package semsearch

import "sort"

// resolve 符号名 → 全名候选集：全名精确命中（定义位置或邻接键存在）返回单元素；
// 否则按末段裸名查候选表（已升序、已封顶 graphSeedCap）——同名符号多候选即
// 多种子/多目标。
func (g *symGraph) resolve(name string) []string {
	if g == nil {
		return nil
	}
	if _, ok := g.loc[name]; ok {
		return []string{name}
	}
	if _, ok := g.adj[name]; ok {
		return []string{name}
	}
	if _, ok := g.radj[name]; ok {
		return []string{name}
	}
	return g.bare[lastSeg(name)]
}

// neighborsOf 命中附邻接（Search 内自动附）：调用方标 relCaller、被调标 relCallee，
// 列表已升序直接各取前 graphNeighborsCap 个，合并按 Symbol 排序；未知符号返回 nil。
func (g *symGraph) neighborsOf(symbol string) []SymRef {
	if g == nil {
		return nil
	}
	var out []SymRef
	for i, c := range g.radj[symbol] {
		if i >= graphNeighborsCap {
			break
		}
		out = append(out, SymRef{Symbol: c, Rel: relCaller})
	}
	for i, c := range g.adj[symbol] {
		if i >= graphNeighborsCap {
			break
		}
		out = append(out, SymRef{Symbol: c, Rel: relCallee})
	}
	if len(out) == 0 {
		return nil
	}
	// Symbol 同值时按 Rel 决出（同名互调的对称边顺序确定）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].Rel < out[j].Rel
	})
	return out
}

// refAt 节点的可读引用（loc 缺失——预算截断把 decl 挤出 chunk 表但边残留——
// 填空串，与 Path 输出同款语义）。
func (g *symGraph) refAt(name string) SymRef {
	return SymRef{Symbol: name, Path: g.loc[name].path, Line: g.loc[name].line}
}

// undirected 无向邻接（adj+radj 合并升序去重）——BFS 扩展开序即此序，输出确定。
func (g *symGraph) undirected(n string) []string {
	all := make([]string, 0, len(g.adj[n])+len(g.radj[n]))
	all = append(all, g.adj[n]...)
	all = append(all, g.radj[n]...)
	sort.Strings(all)
	return dedupStrings(all)
}

// stepRel u→v 的边向：v 在 adj[u]（u 调 v）即 calls，否则 called_by（无向 BFS
// 的逆向步）。
func (g *symGraph) stepRel(u, v string) string {
	for _, c := range g.adj[u] {
		if c == v {
			return relCalls
		}
	}
	return relCalledBy
}

// SymbolContext 单符号图上下文：定义位置 + 调用方 + 被调。同名多候选合并邻接
// （词法级裸名匹配的既定边界，见 graph.go 文档），self 取路径序首个候选；候选
// 为空（符号不在图内）返回 ok=false。
func (ix *Index) SymbolContext(symbol string) (self SymRef, in, out []SymRef, ok bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	g := ix.sym
	if g == nil {
		return
	}
	cands := g.resolve(symbol)
	if len(cands) == 0 {
		return
	}
	ok = true
	self = g.refAt(cands[0])
	// 多候选邻接合并去重（与 candidates 排序同款：升序去重后按容量封顶）。
	var callers, callees []string
	seenIn, seenOut := map[string]bool{}, map[string]bool{}
	for _, c := range cands {
		for _, u := range g.radj[c] {
			if !seenIn[u] {
				seenIn[u] = true
				callers = append(callers, u)
			}
		}
		for _, v := range g.adj[c] {
			if !seenOut[v] {
				seenOut[v] = true
				callees = append(callees, v)
			}
		}
	}
	sort.Strings(callers)
	sort.Strings(callees)
	if len(callers) > graphCallersCap {
		callers = callers[:graphCallersCap]
	}
	if len(callees) > graphCalleesCap {
		callees = callees[:graphCalleesCap]
	}
	for _, u := range callers {
		in = append(in, SymRef{Symbol: u, Rel: relCaller})
	}
	for _, v := range callees {
		out = append(out, SymRef{Symbol: v, Rel: relCallee})
	}
	return
}

// Path 两符号间最短调用链（无向 BFS，≤maxHops 跳，maxHops 钳 [1, graphHopsMax]）：
// 两端各解析出种子/目标候选集（同名多候选），多源 BFS；扩展开序=排序邻接序
// （adj+radj 合并升序），输出确定。命中即回溯重构链：链首 Rel 为空，其后每步按
// 方向标 calls/called_by；输出 Symbol/Path/Line 填 loc，不在 loc 的填空串。
// 不可达或任一端未解析返回 (nil, false)。
func (ix *Index) Path(from, to string, maxHops int) ([]SymRef, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	g := ix.sym
	if g == nil {
		return nil, false
	}
	if maxHops < 1 {
		maxHops = 1
	}
	if maxHops > graphHopsMax {
		maxHops = graphHopsMax
	}
	seeds := g.resolve(from)
	if len(seeds) == 0 {
		return nil, false
	}
	targets := g.resolve(to)
	if len(targets) == 0 {
		return nil, false
	}
	targetSet := make(map[string]bool, len(targets))
	for _, t := range targets {
		targetSet[t] = true
	}
	// 单步：种子集与目标集相交（同符号/同名候选）直接返回。
	for _, s := range seeds {
		if targetSet[s] {
			return []SymRef{g.refAt(s)}, true
		}
	}
	// 多源无向 BFS：种子同时入队（深度 0）；visited + parent（父节点与边向）
	// 双表防重入与回溯。深度 ≥ maxHops 不再扩展。
	type queued struct {
		node  string
		depth int
	}
	type parentStep struct {
		node string // 前驱
		rel  string // 前驱 → 本节点的边向
	}
	parent := map[string]parentStep{}
	visited := map[string]bool{}
	queue := make([]queued, 0, 16)
	for _, s := range seeds {
		visited[s] = true
		queue = append(queue, queued{node: s})
	}
	end := ""
	for len(queue) > 0 && end == "" {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxHops {
			continue // 本层只收不扩：更深的边必超界
		}
		for _, next := range g.undirected(cur.node) {
			if visited[next] {
				continue
			}
			visited[next] = true
			parent[next] = parentStep{node: cur.node, rel: g.stepRel(cur.node, next)}
			if targetSet[next] {
				end = next
				break
			}
			queue = append(queue, queued{node: next, depth: cur.depth + 1})
		}
	}
	if end == "" {
		return nil, false
	}
	// 回溯重构：end → 种子，反转成正向链后统一标注边向（链首 Rel 为空）。
	var rev []string
	for n := end; ; {
		rev = append(rev, n)
		p, ok := parent[n]
		if !ok {
			break // 种子无 parent，链头到此
		}
		n = p.node
	}
	chain := make([]SymRef, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		chain = append(chain, g.refAt(rev[i]))
	}
	for i := 1; i < len(chain); i++ {
		chain[i].Rel = g.stepRel(chain[i-1].Symbol, chain[i].Symbol)
	}
	return chain, true
}
