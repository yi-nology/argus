// graph.go —— 符号邻接面（v5.9.0，设计借鉴 GitNexus Graph RAG：向量 top-K
// 后沿调用边拉 1-2 跳结构邻居。argus 不引其代码——PolyForm Noncommercial
// 许可证不可商用，边数据自产：chunk 抽取的同一次 AST 解析顺带抽 FuncDecl 体
// CallExpr 赤名边，剪枝到仓库内已定义符号，stdlib/未解析调用剪掉）。
//
// 三个查询面：Neighbors 命中附邻接（Search 内自动附）、SymbolContext 单符号
// 定义+调用方+被调、Path 两符号间 ≤4 跳最短调用链（无向 BFS，扩展开序保证
// 输出确定）。
//
// 边界（与 codeintel/get_callers 同款声明）：词法级边，接口分派/动态派发/
// 方法值不可见；同名符号按裸名匹配可能混入同名异接收者——引用前须 get_file
// 核实。向量与图同索引共存，Refresh 重建 chunk/向量与图两个面。
package semsearch

import (
	"sort"
	"strings"
)

// 图面容量封顶（防大邻接渲染吃爆上下文；与 impactview MaxCallersPerSym 同量级）。
const (
	graphCallersCap   = 8 // SymbolContext 单符号调用方/被调上限
	graphCalleesCap   = 8 // 单符号被调上限
	graphNeighborsCap = 4 // search_sem 命中单侧（调用方/被调）邻居上限
	graphHopsDefault  = 4 // Path 缺省 hops（工具描述「≤4 跳」与本值同源，改此须同步）
	graphHopsMax      = 6 // Path 跳数上界
	graphSeedCap      = 8 // 裸名多候选（同名符号）种子/目标封顶
)

// 关系词表（SymRef.Rel）。
const (
	relCaller   = "caller"    // 调用方（in-neighbor）
	relCallee   = "callee"    // 被调（out-neighbor）
	relCalls    = "calls"     // 本步调用下一步
	relCalledBy = "called_by" // 本步被下一步调用
)

// SymRef 符号引用（图面可读单点：符号+定义位置+边关系）。
type SymRef struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Rel    string `json:"rel,omitempty"` // caller|callee（邻接面）| calls|called_by（Path 方向）
}

// loc 符号定义位置。
type loc struct {
	path string
	line int
}

// symGraph 符号邻接图（Refresh 收尾自 files 快照重建，恒去重排序——遍历序
// 确定即输出确定）。
type symGraph struct {
	adj  map[string][]string // 符号 → 被调（已解析、去重、升序）
	radj map[string][]string // 符号 → 调用方（adj 的逆）
	loc  map[string]loc      // 符号 → 定义位置（同键多文件取路径序首个）
	bare map[string][]string // 裸名 → 全名候选（升序，封顶 graphSeedCap）
}

// rebuildGraphLocked 从当前 files 快照重建符号邻接图（调用方持 ix.mu；Refresh
// 收尾调用，含 todo 空早退路径——删除/预算截断同样改变符号集）。恒重建：成本
// O(符号+边)，秒级索引内存驻留可承受。三步：符号表/位置 → 边解析（callee 限定
// 名按末段归一，解析到仓库符号表的才留）→ 邻接去重排序。
func (ix *Index) rebuildGraphLocked() {
	g := &symGraph{
		adj:  map[string][]string{},
		radj: map[string][]string{},
		loc:  map[string]loc{},
		bare: map[string][]string{},
	}
	// ① 符号表与定义位置：paths 是排序快照，同键多文件取路径序首个（不覆盖已有）。
	//   bare 无条件追加候选，去重统一靠收尾 sort+相邻去重。
	for _, rel := range ix.paths {
		fi := ix.files[rel]
		if fi == nil {
			continue // 快照与文件集的瞬时不一致防御（Search 同款）
		}
		for _, c := range fi.chunks {
			if c.Symbol == "" {
				continue // 文件级 chunk 无符号，不入图
			}
			if _, ok := g.loc[c.Symbol]; !ok {
				g.loc[c.Symbol] = loc{path: rel, line: c.Line}
			}
			g.bare[lastSeg(c.Symbol)] = append(g.bare[lastSeg(c.Symbol)], c.Symbol)
		}
	}
	// ② 边解析：callee 限定名按末段归一到仓库符号表（bare 候选），解析不到的
	//   （stdlib/外部/未定义）即剪掉——图只保留仓库内符号。
	for _, rel := range ix.paths {
		fi := ix.files[rel]
		if fi == nil {
			continue
		}
		for _, e := range fi.edges {
			for _, callee := range g.bare[lastSeg(e.callee)] {
				g.adj[e.caller] = append(g.adj[e.caller], callee)
				g.radj[callee] = append(g.radj[callee], e.caller)
			}
		}
	}
	// ③ 收尾：邻接与裸名候选统一排序+相邻去重（遍历序无关，输出确定）；裸名
	//   候选封顶 graphSeedCap。最后整图原子挂载。
	for _, m := range []map[string][]string{g.adj, g.radj, g.bare} {
		for k, list := range m {
			sort.Strings(list)
			m[k] = dedupStrings(list)
		}
	}
	for k, list := range g.bare {
		if len(list) > graphSeedCap {
			g.bare[k] = list[:graphSeedCap]
		}
	}
	ix.sym = g
}

// lastSeg 限定名末段（"a.b.c" → "c"，无点原文返回）：callee 限定名到裸名候选
// 表的归一键，也是裸名查询的入口。
func lastSeg(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// dedupStrings 已排序列表的相邻去重（复用底层数组，仅重建收尾路径使用）。
func dedupStrings(list []string) []string {
	if len(list) == 0 {
		return list
	}
	out := list[:1]
	for _, s := range list[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
