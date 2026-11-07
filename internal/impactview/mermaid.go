// mermaid.go —— 影响面/函数级图 diff 的 mermaid 渲染面（v5.1.0 自 render.go
// 拆出：图语法面被 e2e 快照与模型提示词双重消费，文本面只服务评论/报告，
// 生命周期不同步）。全部纯函数，不触 IO。
package impactview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RenderMermaid 渲染 mermaid flowchart 源（LR 分层：改动居左，影响向右）。
// 节点超 MaxMermaidNodes 先聚合（symbol/test → 所属 file，file → 所属 package），
// 源超 MaxMermaidRunes 二次聚合后仍超则返回空串（调用方降级 RenderList）。
func RenderMermaid(s *Snapshot) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	if src := renderMermaid(s, s.Nodes, s.Edges); src != "" && runeLen(src) <= MaxMermaidRunes {
		return src
	}
	// 一次聚合：非改动 file 节点并入所属 package（或删除孤立文件节点）
	aggNodes, aggEdges := aggregateToPackages(s)
	if src := renderMermaid(s, aggNodes, aggEdges); src != "" && runeLen(src) <= MaxMermaidRunes {
		return src
	}
	return "" // 仍超 → 降级列表
}

// renderMermaid 具体渲染（节点已选定）。无可渲染节点返回空串。
// 可读性纪律（v3.185.1，用户实拍反馈「看不懂」）：孤立符号节点（类型/常量
// 被触达但无调用边）一律摘除——它们在图上是无人连接的噪声列，函数级故事由
// funcdiff 卡片承载；改动符号仅在有调用边时入图并着黄色，与改动文件（红）区分。

// renderMermaid 具体渲染（节点已选定）。无可渲染节点返回空串。
// 可读性纪律（v3.185.1，用户实拍反馈「看不懂」）：孤立符号节点（类型/常量
// 被触达但无调用边）一律摘除——它们在图上是无人连接的噪声列，函数级故事由
// funcdiff 卡片承载；改动符号仅在有调用边时入图并着黄色，与改动文件（红）区分。
func renderMermaid(s *Snapshot, nodes []Node, edges []Edge) string {
	if len(nodes) == 0 {
		return ""
	}
	keep := nodes
	if len(keep) > MaxMermaidNodes {
		// 改动节点优先保留，其余按 (Kind 权重, ID) 截断
		keep = prioritizeNodes(nodes, MaxMermaidNodes)
	}
	keepIDs := map[string]bool{}
	for _, n := range keep {
		keepIDs[n.ID] = true
	}
	// 仅统计 keep 子图内的度；孤立改动符号摘除（类型/常量触达形态）。
	// liveEdges 必须独立分配——若 edges[:0] 原地过滤会污染调用方 s.Edges
	// （len 不变、尾部残留旧边），二次渲染（超长聚合路径）即产生重复边
	// （实弹复现：n9 边 2→4），函数必须保持纯。
	deg := map[string]int{}
	liveEdges := make([]Edge, 0, len(edges))
	for _, e := range edges {
		if keepIDs[e.From] && keepIDs[e.To] {
			liveEdges = append(liveEdges, e)
			deg[e.From]++
			deg[e.To]++
		}
	}
	pruned := keep[:0]
	for _, n := range keep {
		if n.Kind == KindSymbol && deg[n.ID] == 0 {
			continue
		}
		pruned = append(pruned, n)
	}
	keep = pruned
	if len(keep) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for i, n := range keep {
		label := mermaidEscape(n.Label)
		switch {
		case n.Changed && n.Kind == KindSymbol:
			fmt.Fprintf(&b, "  n%d[\"%s\"]:::chgsym\n", i, label)
		case n.Changed:
			fmt.Fprintf(&b, "  n%d[\"%s\"]:::changed\n", i, label)
		case n.Kind == KindTest:
			fmt.Fprintf(&b, "  n%d[\"%s\"]:::testn\n", i, label)
		default:
			fmt.Fprintf(&b, "  n%d[\"%s\"]\n", i, label)
		}
	}
	for _, e := range liveEdges {
		fi, ok := indexOfNode(keep, e.From)
		if !ok {
			continue
		}
		ti, ok := indexOfNode(keep, e.To)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "  n%d -->|%s| n%d\n", fi, mermaidRel(e.Rel), ti)
	}
	b.WriteString("  classDef changed fill:#fecaca,stroke:#dc2626,color:#7f1d1d;\n")
	b.WriteString("  classDef chgsym fill:#fef9c3,stroke:#ca8a04,color:#713f12;\n")
	b.WriteString("  classDef testn fill:#dcfce7,stroke:#16a34a,color:#14532d;\n")
	src := b.String()
	if runeLen(src) > MaxMermaidRunes {
		return ""
	}
	return src
}

// prioritizeNodes 节点超限时的选择：改动节点（file>symbol）→ 依赖方包 →
// 测试 → 调用方/共变文件；同层按 ID 字典序，保确定性。

// prioritizeNodes 节点超限时的选择：改动节点（file>symbol）→ 依赖方包 →
// 测试 → 调用方/共变文件；同层按 ID 字典序，保确定性。
func prioritizeNodes(nodes []Node, max int) []Node {
	weight := func(n Node) int {
		switch {
		case n.Changed && n.Kind == KindFile:
			return 0
		case n.Changed: // 改动符号
			return 1
		case n.Kind == KindPackage:
			return 2
		case n.Kind == KindTable: // v5.6.0 血缘枢纽：与包同级可见性
			return 2
		case n.Kind == KindTest:
			return 3
		default:
			return 4
		}
	}
	sorted := make([]Node, len(nodes))
	copy(sorted, nodes)
	sort.SliceStable(sorted, func(i, j int) bool {
		wi, wj := weight(sorted[i]), weight(sorted[j])
		if wi != wj {
			return wi < wj
		}
		return sorted[i].ID < sorted[j].ID
	})
	if len(sorted) > max {
		sorted = sorted[:max]
	}
	return sorted
}

// aggregateToPackages 一级聚合：非改动的 file/symbol 节点并入其目录 package
// 节点；改动节点、显式包节点与测试节点保留。用于二次尝试控制 mermaid 尺寸。

// aggregateToPackages 一级聚合：非改动的 file/symbol 节点并入其目录 package
// 节点；改动节点、显式包节点与测试节点保留。用于二次尝试控制 mermaid 尺寸。
func aggregateToPackages(s *Snapshot) ([]Node, []Edge) {
	seen := map[string]bool{}
	var out []Node
	addNode := func(n Node) string {
		if !seen[n.ID] {
			seen[n.ID] = true
			out = append(out, n)
		}
		return n.ID
	}
	remap := map[string]string{}
	for _, n := range s.Nodes {
		switch {
		case n.Changed || n.Kind == KindPackage || n.Kind == KindTest:
			remap[n.ID] = addNode(n)
		default: // file / symbol → 并入所在目录的 package 节点
			dir := pkgDirOf(nodePath(n))
			remap[n.ID] = addNode(Node{ID: "pkg:" + dir, Kind: KindPackage, Label: dir})
		}
	}
	var edges []Edge
	edgeSeen := map[string]bool{}
	for _, e := range s.Edges {
		f, okf := remap[e.From]
		t, okt := remap[e.To]
		if !okf || !okt || f == t {
			continue
		}
		key := f + "|" + t + "|" + e.Rel
		if edgeSeen[key] {
			continue
		}
		edgeSeen[key] = true
		edges = append(edges, Edge{From: f, To: t, Rel: e.Rel})
	}
	return out, edges
}

// nodePath 节点对应的文件/目录路径（ID 前缀剥离）。

// nodePath 节点对应的文件/目录路径（ID 前缀剥离）。
func nodePath(n Node) string {
	p := n.ID
	for _, pre := range []string{"file:", "sym:", "test:", "pkg:"} {
		p = strings.TrimPrefix(p, pre)
	}
	if i := strings.Index(p, ":"); i >= 0 { // sym:file:Name 形态取 file 段
		p = p[:i]
	}
	return p
}

// pkgDirOf 路径的包目录（= 所在目录）。

// pkgDirOf 路径的包目录（= 所在目录）。
func pkgDirOf(p string) string {
	dir := p
	if i := strings.LastIndexByte(dir, '/'); i >= 0 {
		dir = dir[:i]
	} else {
		dir = "."
	}
	return dir
}

// indexOfNode keep 序列里的下标（线性，节点 ≤24）。

// indexOfNode keep 序列里的下标（线性，节点 ≤24）。
func indexOfNode(nodes []Node, id string) (int, bool) {
	for i, n := range nodes {
		if n.ID == id {
			return i, true
		}
	}
	return 0, false
}

// mermaidEscape mermaid 双引号标签内的转义（" 与 \；其他字符引号内安全）。

// mermaidEscape mermaid 双引号标签内的转义（" 与 \；其他字符引号内安全）。
func mermaidEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}

// mermaidRel 边关系标签（词表 → 动词短语：读图方向恒为 左=改动 → 右=受影响）。

// mermaidRel 边关系标签（词表 → 动词短语：读图方向恒为 左=改动 → 右=受影响）。
func mermaidRel(rel string) string {
	switch rel {
	case RelImport:
		return "波及依赖包"
	case RelCaller:
		return "波及调用方"
	case RelSource:
		return "波及装载方"
	case RelLineage:
		return "表血缘"
	case RelTestOf:
		return "需跑测试"
	case RelCouple:
		return "历史共变"
	default:
		return rel
	}
}

// runeLen 字符数（mermaid 上限按源字符数，与 Gitea/Forgejo 校验口径一致）。

// RenderFuncDiffMermaid 函数级图 diff（v3.185.0）：改动函数（🟢added/🔴removed/🟡modified）
// 为节点，出边调用增减为有向边（实线+=新增、虚线−=断开）。≤8 函数/≤12 边/
// 字符上限内渲染，无数据返回空串。
func RenderFuncDiffMermaid(s *Snapshot) string {
	if s == nil || len(s.FuncDiffs) == 0 {
		return ""
	}
	fds := s.FuncDiffs
	if len(fds) > 8 {
		fds = fds[:8]
	}
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	nodeID := map[string]string{} // 展示名 → mermaid id
	edges := 0
	ensure := func(name, cls string) string {
		if id, ok := nodeID[name]; ok {
			return id
		}
		id := "g" + strconv.Itoa(len(nodeID))
		nodeID[name] = id
		fmt.Fprintf(&b, "  %s[\"%s\"]%s\n", id, mermaidEscape(truncateRunes(name, 34)), cls)
		return id
	}
	for _, fd := range fds {
		label := fd.Name
		var cls string
		switch fd.Status {
		case FuncAdded:
			cls = ":::fdadd"
		case FuncRemoved:
			cls = ":::fdrem"
		default:
			cls = ":::fdmod"
			if fd.SigSame {
				label += "（调用变化）"
			} else {
				label += "（签名变更）"
			}
		}
		fid := ensure(label, cls)
		if edges < MaxFuncDiffMermaidEdges {
			for _, c := range fd.CallsAdded {
				if edges >= MaxFuncDiffMermaidEdges {
					break
				}
				fmt.Fprintf(&b, "  %s -->|%s| %s\n", fid, "+", ensure(c, ""))
				edges++
			}
		}
		if edges < MaxFuncDiffMermaidEdges {
			for _, c := range fd.CallsRemoved {
				if edges >= MaxFuncDiffMermaidEdges {
					break
				}
				fmt.Fprintf(&b, "  %s -.->|%s| %s\n", fid, "−", ensure(c, ""))
				edges++
			}
		}
	}
	b.WriteString("  classDef fdadd fill:#dcfce7,stroke:#16a34a,color:#14532d;\n")
	b.WriteString("  classDef fdrem fill:#fecaca,stroke:#dc2626,color:#7f1d1d;\n")
	b.WriteString("  classDef fdmod fill:#fef9c3,stroke:#ca8a04,color:#713f12;\n")
	src := b.String()
	if runeLen(src) > MaxMermaidRunes {
		return "" // 超限降级为纯清单（列表形态恒可用）
	}
	return src
}

// MaxFuncDiffMermaidEdges 函数图 diff 的边上限。

// MaxFuncDiffMermaidEdges 函数图 diff 的边上限。
const MaxFuncDiffMermaidEdges = 12

// RenderFuncDiffList 函数级变更清单（紧凑，≤8 条 + 溢出数）。
