// graph.go —— 符号邻接面（v5.10.0，设计借鉴 GitNexus Graph RAG：向量 top-K
// 后沿调用边拉 1-2 跳结构邻居。argus 不引其代码——PolyForm Noncommercial
// 许可证不可商用，边数据自产：chunk 抽取的同一次 AST 解析顺带抽调用边，
// import 限定消歧 + 同接收者兄弟方法 + 裸名同目录优先（Go 包级裸调用不跨包），
// stdlib/未解析调用剪掉）。节点键=NodeKey 契约（dir.Base / dir.Recv.Base，
// dir 为仓库相对目录）——v5.9.0 裸 Recv.Name/Name 跨包同名合并成单节点的假边
// 污染自此消除，多候选=同 bare 名跨目录。
//
// 三个查询面：Neighbors 命中附邻接（Search 内自动附）、SymbolContext 单符号
// 定义+调用方+被调（含候选数）、Path 两符号间 ≤4 跳最短调用链（无向 BFS，
// 扩展开序保证输出确定）。
//
// 精确边钩子（v5.10.0 档一接口，档二实现——实现方包在 semsearch 之外）：
// EdgeAugmenter 成功注入全仓精确调用边时，Refresh 尾部以精确边替换词法邻接
// （符号表照建）；失败置闩，本 Index 生命周期内降级词法边不再重试。
//
// 边界（与 codeintel/get_callers 同款声明）：词法级边，接口分派/动态派发/
// 方法值不可见；同名符号多候选合并邻接——引用前须 get_file 核实。向量与图
// 同索引共存，Refresh 重建 chunk/向量与图两个面。
package semsearch

import (
	"context"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strconv"
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

// NodeKey 节点键（全仓唯一身份契约，v5.10.0）：`dir.Base` / `dir.Recv.Base`。
// dir=仓库相对目录（filepath.Dir 形态；根目录文件传空串→无前缀 `Base` /
// `Recv.Base`），recv=接收者基类型名（无接收者传空串），name=声明名。
// chunk.Symbol、图节点键、bare 索引、邻接键、Hit.Symbol、SymRef.Symbol、
// resolve 输入与 EdgeAugmenter 边端点全部使用本格式——EdgeAugmenter 实现方
// 产键须遵守本契约。
func NodeKey(dir, recv, name string) string {
	base := name
	if recv != "" {
		base = recv + "." + name
	}
	if dir == "" {
		return base
	}
	return dir + "." + base
}

// dirOf 仓库相对路径 → 节点键 dir 段（filepath.Dir；根目录 "." → 空串）。
func dirOf(rel string) string {
	if d := filepath.Dir(rel); d != "." {
		return d
	}
	return ""
}

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
	adj  map[string][]string // 节点键 → 被调（已解析、去重、升序）
	radj map[string][]string // 节点键 → 调用方（adj 的逆）
	loc  map[string]loc      // 节点键 → 定义位置（同键多文件取路径序首个）
	bare map[string][]string // 裸名（键末段）→ dir 限定全名候选（升序，封顶 graphSeedCap）
}

// rebuildGraphLocked 从当前 files 快照重建符号邻接图（调用方持 ix.mu；Refresh
// 收尾调用，含 todo 空早退路径——删除/预算截断同样改变符号集）。恒重建：成本
// O(符号+边)，秒级索引内存驻留可承受。三步：符号表/位置（精确/词法两模式同源，
// 照建）→ 边解析（精确模式：augmenter 边两端存在性过滤后直用；词法模式：按
// qualified 语义解析+降级回退，见 resolveEdge）→ 邻接去重排序。
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
	// ② 边解析：精确模式（最近一次 Refresh 成功应用 EdgeAugmenter 边）以精确边
	//   替换词法邻接——augmenter 按 NodeKey 契约产键，两端都在符号表（loc，有
	//   定义位置可渲染）才建边；词法模式按 qualified 语义解析，只保留仓库内符号。
	if ix.precise {
		for _, p := range ix.augEdges {
			if len(p) != 2 {
				continue
			}
			caller, callee := p[0], p[1]
			if _, ok := g.loc[caller]; !ok {
				continue
			}
			if _, ok := g.loc[callee]; !ok {
				continue
			}
			g.adj[caller] = append(g.adj[caller], callee)
			g.radj[callee] = append(g.radj[callee], caller)
		}
	} else {
		for _, rel := range ix.paths {
			fi := ix.files[rel]
			if fi == nil {
				continue
			}
			for _, e := range fi.edges {
				for _, callee := range g.resolveEdge(e) {
					g.adj[e.caller] = append(g.adj[e.caller], callee)
					g.radj[callee] = append(g.radj[callee], e.caller)
				}
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

// resolveEdge 单条词法边 → 仓库内目标键集（可能多条：裸名/降级回退命中多个
// 同名候选）。qualified 边精确存在性过滤——目标在符号表即单命中，不存在降级
// bare 末段回退保召回；裸名边同目录候选优先（Go 包级裸调用不跨包），同目录
// 无候选走全局 bare 回退（dot-import 跨包裸调用与 var 限定降级经此保召回）。
func (g *symGraph) resolveEdge(e symEdge) []string {
	cands := g.bare[lastSeg(e.callee)]
	if e.qualified {
		if g.exists(e.callee) {
			return []string{e.callee}
		}
		return cands // 降级：bare 末段回退
	}
	if same := sameDirCands(cands, e.callerDir); len(same) > 0 {
		return same
	}
	return cands
}

// exists 键是否在符号表（定义位置或任一邻接面出现——预算截断可能把 decl chunk
// 挤出 loc 而边残留，与 resolve 同款三面并查）。
func (g *symGraph) exists(key string) bool {
	if _, ok := g.loc[key]; ok {
		return true
	}
	if _, ok := g.adj[key]; ok {
		return true
	}
	_, ok := g.radj[key]
	return ok
}

// keyDir 节点键的 dir 段（裸名边同目录优先解析用）：末段前的整段，无点即根
// 目录空串。方法键（dir.Recv.Name）的末前段是 dir.Recv ≠ dir——裸名候选按此
// 过滤天然不含方法（Go 裸调用不可达方法）；方法候选经全局 bare 回退保召回。
func keyDir(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[:i]
	}
	return ""
}

// sameDirCands 裸名候选的同目录过滤（callerDir 相等的候选；dir 契约见 keyDir）。
func sameDirCands(cands []string, callerDir string) []string {
	var out []string
	for _, k := range cands {
		if keyDir(k) == callerDir {
			out = append(out, k)
		}
	}
	return out
}

// EdgeAugmenter 精确边注入钩子（v5.10.0 档一接口、档二实现——实现方包在
// semsearch 之外，x/tools 不进本包，L0 纪律 stdlib only）：产出全仓精确调用
// 边供 Refresh 尾部替换词法邻接（符号表照建，Manager.Precise 透出模式）。
type EdgeAugmenter interface {
	// Edges 返回全仓精确调用边（caller→callee 有序对，端点键=NodeKey 契约
	// 格式：dir.Base / dir.Recv.Base，dir 为仓库相对目录，根目录无前缀）。
	// 需要可编译工作副本（go/packages 级解析）；任何失败返回 error——调用方
	// 置失败闩降级词法边，本 Index 生命周期内不再重试（工作副本目录 per
	// head-SHA，新 MR 新 Index 自动重试）。
	Edges(ctx context.Context, dir string) ([][2]string, error)
}

// applyAugLocked 精确边钩子（Refresh 收尾、调用方持 ix.mu）：augmenter 已配置
// 且未失败闩时，对文件指纹集算签名（FNV64a）——签名变了才调 Edges（ctx 超时
// 自然生效）。成功→精确边落地并置 precise（后续 rebuild 以精确边替换词法
// 邻接）；失败→失败闩置位、清精确边回退词法，本 Index 生命周期内不再重试。
// 签名未变沿用既有精确边，零调用成本。
func (ix *Index) applyAugLocked(ctx context.Context, dir string, found []scannedFile) {
	if ix.aug == nil || ix.augFailed {
		return
	}
	sig := augSignature(found)
	if sig == ix.augSig {
		return
	}
	edges, err := ix.aug.Edges(ctx, dir)
	if err != nil {
		ix.augFailed = true
		ix.precise = false
		ix.augEdges = nil
		return
	}
	ix.augSig = sig
	ix.augEdges = edges
	ix.precise = true
}

// augSignature 文件集指纹（FNV64a over 排序 "rel|size|mtime" 行）：扫描序已按
// 相对路径排序（scanDir），行序确定即签名确定。
func augSignature(files []scannedFile) uint64 {
	h := fnv.New64a()
	for _, f := range files {
		h.Write([]byte(f.rel))
		h.Write([]byte{'|'})
		h.Write([]byte(strconv.FormatInt(f.fp.size, 10)))
		h.Write([]byte{'|'})
		h.Write([]byte(strconv.FormatInt(f.fp.mtime, 10)))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
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
