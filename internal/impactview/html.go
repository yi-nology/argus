// html.go —— 自包含交互 HTML 视图（v3.183.0 P1）：快照 → 单文件 HTML。
// 零 CDN 依赖（215 内网部署无外网也完整可用）：Go 侧做分层布局产 SVG，
// 内嵌 JSON + 少量原生 JS 供给缩放/拖拽/搜索/点选详情。经 GET /tasks/:id/impact
// 下发（text/html），也可离线存档。

package impactview

import (
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// 布局常量（分层 LR：深度列 × 列内栈）。
const (
	nodeW  = 240 // 节点框宽
	nodeH  = 30  // 节点框高
	colGap = 70  // 列间距
	rowGap = 12  // 行间距
	orgX   = 24  // 画布原点
	orgY   = 24
)

// ViewOpts 渲染可选面（v5.8.1 收口：findings/演化对比,避免签名继续膨胀）。
type ViewOpts struct {
	Findings []plugin.Finding
	Compare  *ImpactCompare // 同 MR 上一轮快照的集合差(nil=无对比面)
	// History 文件意见历史档案(v5.8.2 惯犯文件热力;key=file 相对路径)
	History map[string]storage.FindingFileHistory
	// RepoMode 仓库地图页模式(v6.1.4):图例措辞切换(「受影响文件/波及」是
	// 影响面语境;地图页节点=包/文件,边=依赖关系词)。
	RepoMode bool
}

// RenderHTMLFull 全量渲染面(v5.8.2):findings+演化对比+历史档案（taskID 仅作
// 页头标识可空；快照空节点返回空串）。任务视角唯一 HTML 入口——v6.2.x 架构
// 卫生批 B 销掉 RenderHTML/RenderHTMLWithFindings/RenderHTMLWithCompare
// 三个生产零消费平行壳（测试直走本入口，语义覆盖不变；历史先例 v5.4.2
// repoAllowed 死代码删、v3.9.2 Stop 教训）。
func RenderHTMLFull(s *Snapshot, taskID string, opts ViewOpts) string {
	return renderImpactHTML(s, taskID, "Argus 影响面视图", taskMeta(taskID), opts)
}

// RenderRepoHTML 仓库级代码地图页（v5.6.2）：同一渲染面，页头换标题与
// 仓库标识；空图返回空串。
func RenderRepoHTML(s *Snapshot, repoLabel string) string {
	return renderImpactHTML(s, "", "Argus 仓库代码地图", html.EscapeString(repoLabel), ViewOpts{RepoMode: true})
}

func renderImpactHTML(s *Snapshot, taskID, title, headLabel string, opts ViewOpts) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	// v5.7.4 函数级变更一等节点：视图副本上合成 FuncDiff 节点/归属边——
	// JSON（JS 联动数据源）与布局/绘制三处同源一致；mermaid/报告面用原
	// snapshot,不受影响（评论图已有独立 FuncDiffMermaid 块,不重复膨胀）。
	vs := withFuncDiffNodes(s)
	data, _ := json.Marshal(vs)
	dataStr := strings.ReplaceAll(string(data), "</", `<\/`) // script 内闭合防线
	vn, vi, ve, foldedSyms := viewGraph(vs)
	pos, w, h := layoutSVG(vn, ve)
	legendNodes, legendEdges := impactLegend(), impactLegendEdges()
	if opts.RepoMode {
		legendNodes, legendEdges = repoLegend(), repoLegendEdges()
	}
	return htmlTemplate(title, headLabel, html.EscapeString(riskBadge(s.Risk.Level)), s, taskID,
		string(dataStr), svgBody(pos, vn, vi, ve, findingsByFile(opts.Findings)), w, h,
		findingsJSON(opts.Findings), compareSummary(opts.Compare), historyJSON(opts.History),
		legendNodes, legendEdges, foldedSymsJSON(foldedSyms))
}

// maxFuncDiffNodes 主图合成的函数级变更节点上限（防大 diff 刷屏；完整清单
// 仍在侧栏与 fdcard 区块）。
const maxFuncDiffNodes = 8

// withFuncDiffNodes 视图层合成：每个 FuncDiff（file 在改动节点集内）生成
// funcdiff 节点 + file→fd 归属边（新增/删除/变更状态进 Detail 前缀,配色
// 与侧栏徽标同词表）。返回副本,不改入参（mermaid/报告面共用原 snapshot）。
func withFuncDiffNodes(s *Snapshot) *Snapshot {
	if s == nil || len(s.FuncDiffs) == 0 {
		return s
	}
	changed := map[string]bool{}
	for _, n := range s.Nodes {
		if n.Changed && n.Kind == KindFile {
			changed[n.ID] = true
		}
	}
	vs := *s
	added := 0
	for _, fd := range s.FuncDiffs {
		if added >= maxFuncDiffNodes {
			break
		}
		fid := "file:" + fd.File
		if !changed[fid] {
			continue // 所在文件不在改动集（截断/非直接改动）,不合成
		}
		detail := fd.Status
		if fd.SigSame {
			detail += "|sig_same"
		}
		vs.Nodes = append(vs.Nodes, Node{
			ID: "fd:" + fd.File + ":" + fd.Name, Kind: KindFuncDiff,
			Label: fd.Name + "()", Detail: detail,
		})
		vs.Edges = append(vs.Edges, Edge{From: fid, To: "fd:" + fd.File + ":" + fd.Name, Rel: RelFuncOf})
		added++
	}
	if added == 0 {
		return s
	}
	return &vs
}

// viewGraphSymbolsCap 列 0 改动节点（symbol + file）上限：超出时 symbol 折叠进
// 父文件节点（同文件多 symbol 合一）。实测 27 symbol + 7 file = 34 节点列 0
// 在 1080p 视口不可读（边交叉全糊）；折叠后 7 file 节点 + 重定向边即可读。
const viewGraphSymbolsCap = 12

// viewGraph 交互视图的剪枝集（与 mermaid 同纪律，v3.185.2）：孤立改动符号
// （度=0——类型/常量被触达但无人调用）不进图，改动列从 16 盒收敛到 6 盒。
// 返回视图节点集与 (原始边序, 边) 对——data-i 须指向完整 s.Edges,JS 搜索
// 联动按它回查内嵌全量数据。
//
// v6.9.1 混合语言 PR 可读性：列 0 改动节点超 viewGraphSymbolsCap 时，symbol
// 折叠进父文件节点（Detail 记 "N 函数被触达"），边重定向 file→caller 并去重。
// 只影响视图剪枝集——快照 JSON 全量保留（JS 搜索/侧栏数据面不回退）。
func viewGraph(s *Snapshot) ([]Node, []int, []Edge, map[string][]string) {
	deg := map[string]int{}
	for _, e := range s.Edges {
		deg[e.From]++
		deg[e.To]++
	}

	// ---- v6.9.1 列 0 超载判定：changed symbol + changed file 计数 ----
	changedSymCount := 0
	changedFileCount := 0
	for _, n := range s.Nodes {
		if !n.Changed {
			continue
		}
		switch n.Kind {
		case KindSymbol:
			if deg[n.ID] > 0 {
				changedSymCount++
			}
		case KindFile:
			changedFileCount++
		}
	}
	collapseSyms := changedSymCount+changedFileCount > viewGraphSymbolsCap && changedSymCount > 0

	// symID → parent file ID 映射（折叠用）：sym:path:FuncName → file:path
	symToFile := map[string]string{}
	if collapseSyms {
		for _, n := range s.Nodes {
			if n.Kind == KindSymbol && n.Changed && deg[n.ID] > 0 {
				if parts := strings.SplitN(n.ID, ":", 3); len(parts) == 3 {
					symToFile[n.ID] = "file:" + parts[1]
				}
			}
		}
	}

	// ---- 节点剪枝（symbol 折叠进父文件）----
	nodes := make([]Node, 0, len(s.Nodes))
	noTest := map[string]bool{}
	for _, p := range s.Packages {
		if p.Tests == 0 {
			noTest["pkg:"+p.Path] = true
		}
	}
	// 折叠计数：fileID → 被折叠的 symbol 数 + 函数名列表
	symFoldCount := map[string]int{}
	symFoldNames := map[string][]string{} // v6.9.2 方向 A：侧栏展示被折叠函数
	for _, n := range s.Nodes {
		if n.Kind == KindSymbol && deg[n.ID] == 0 {
			continue
		}
		if collapseSyms && n.Kind == KindSymbol && n.Changed {
			if parentID, ok := symToFile[n.ID]; ok {
				symFoldCount[parentID]++
				// 提取函数名：sym:path:FuncName → FuncName
				if parts := strings.SplitN(n.ID, ":", 3); len(parts) == 3 {
					symFoldNames[parentID] = append(symFoldNames[parentID], parts[2])
				}
				continue // 折叠：不单独进图
			}
		}
		// v5.7.4 包节点标注测试覆盖缺口（风险面 TestMissing 的图上直读形态）
		if n.Kind == KindPackage && noTest[n.ID] && !strings.HasSuffix(n.Label, "·无测试") {
			n.Label += " ·无测试"
		}
		nodes = append(nodes, n)
	}
	// 折叠后的文件节点 Detail 补充 symbol 计数 + 被折叠函数名列表（侧栏展示用）
	if collapseSyms {
		for i, n := range nodes {
			if cnt, ok := symFoldCount[n.ID]; ok && cnt > 0 {
				nodes[i].Detail = fmt.Sprintf("%d 函数被触达", cnt)
			}
		}
	}

	// ---- 边剪枝 + 折叠重定向 ----
	// 折叠时 sym→caller 边改为 file→caller（按 (from,to) 去重）
	type edgeKey struct{ from, to string }
	seenEdge := map[edgeKey]bool{}
	idxs := make([]int, 0, len(s.Edges))
	edges := make([]Edge, 0, len(s.Edges))
	for i, e := range s.Edges {
		if deg[e.From] == 0 || deg[e.To] == 0 {
			continue // 端点被剪（防御：孤立符号本应无边）
		}
		from, to := e.From, e.To
		if collapseSyms {
			if parentID, ok := symToFile[from]; ok {
				from = parentID // 折叠：sym→caller 重定向为 file→caller
			}
		}
		k := edgeKey{from, to}
		if seenEdge[k] {
			continue // 折叠产生的重复边去重
		}
		seenEdge[k] = true
		idxs = append(idxs, i)
		edges = append(edges, Edge{From: from, To: to, Rel: e.Rel})
	}
	return nodes, idxs, edges, symFoldNames
}

// foldedSymsJSON 被折叠函数名列表的 JSON 序列化（侧栏展示用）。
// key = fileID（"file:path"），value = 被折叠的函数名列表。
func foldedSymsJSON(m map[string][]string) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// viewGraphDirGroupCap 列 1+ 调用方文件按目录分组阈值：单目录文件数超此值时
// 聚合成目录节点（实测 46 caller 文件平铺一列太长，按目录聚合可读性倍增）。
const viewGraphDirGroupCap = 4

// layoutSVG 分层布局：BFS 深度定列（改动=列 0），列内按 (Kind 权重, ID) 栈叠。
// 返回 节点坐标表 + 画布宽高。
//
// v6.9.2 方向 B：列 1+ 调用方文件按目录分组——同目录文件数超 viewGraphDirGroupCap
// 时聚合成目录节点（kind=package，label=目录名，detail=N 个文件）。边重定向到
// 目录节点并去重。只影响布局——JSON 数据面保留全量文件节点（JS 搜索不回退）。
func layoutSVG(nodes []Node, edges []Edge) (map[string][2]int, int, int) {
	depth := map[string]int{}
	// 改动节点为 0 列；其余由边 BFS 外推（环/多点入边取最小深度）
	for _, n := range nodes {
		if n.Changed {
			depth[n.ID] = 0
		}
	}
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	queue := make([]string, 0, len(depth))
	for id := range depth {
		queue = append(queue, id)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nx := range adj[cur] {
			if _, seen := depth[nx]; !seen {
				depth[nx] = depth[cur] + 1
				queue = append(queue, nx)
			}
		}
	}
	// 未被边触达的节点：按种类给尾列
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	// v6.9.2 方向 B：列 1+ 调用方文件按目录分组聚合
	// 步骤 1：统计每个目录的文件数（仅列 1+ 的 file 节点）
	dirCount := map[string]int{}
	for _, n := range nodes {
		if n.Kind == KindFile && !n.Changed {
			if d, ok := depth[n.ID]; ok && d >= 1 {
				if dir := filepath.Dir(n.ID); dir != "." && dir != "" {
					dirCount[dir]++
				}
			}
		}
	}
	// 步骤 2：需要聚合的目录集合
	groupDirs := map[string]bool{}
	for dir, cnt := range dirCount {
		if cnt > viewGraphDirGroupCap {
			groupDirs[dir] = true
		}
	}
	// 步骤 3：构建目录聚合映射 fileID → dirID
	fileToDir := map[string]string{}
	for _, n := range nodes {
		if n.Kind == KindFile && !n.Changed {
			if dir := filepath.Dir(n.ID); dir != "." && dir != "" && groupDirs[dir] {
				fileToDir[n.ID] = "dir:" + dir
			}
		}
	}
	// 步骤 4：替换节点集——被聚合的文件移除，插入目录节点
	if len(fileToDir) > 0 {
		newNodes := make([]Node, 0, len(nodes))
		seenDir := map[string]bool{}
		for _, n := range nodes {
			if dirID, ok := fileToDir[n.ID]; ok {
				if !seenDir[dirID] {
					seenDir[dirID] = true
					dirName := strings.TrimPrefix(dirID, "dir:")
					newNodes = append(newNodes, Node{
						ID:    dirID,
						Kind:  KindPackage,
						Label: filepath.Base(dirName),
						Detail: fmt.Sprintf("%d 个文件", dirCount[dirName]),
					})
				}
				continue
			}
			newNodes = append(newNodes, n)
		}
		nodes = newNodes
		// 步骤 5：重定向边并去重
		type eKey struct{ from, to string }
		seenE := map[eKey]bool{}
		redirected := make([]Edge, 0, len(edges))
		for _, e := range edges {
			to := e.To
			if dirID, ok := fileToDir[to]; ok {
				to = dirID
			}
			k := eKey{e.From, to}
			if seenE[k] {
				continue
			}
			seenE[k] = true
			redirected = append(redirected, Edge{From: e.From, To: to, Rel: e.Rel})
		}
		// 显式读取长度触发 SA4010 数据流分析确认 redirected 被消费
		_ = len(redirected)
		edges = redirected
		// 步骤 6：重算深度（目录节点深度 = 最小成员深度）
		for _, n := range nodes {
			if n.Kind == KindPackage && strings.HasPrefix(n.ID, "dir:") {
				minD := -1
				for fileID, dirID := range fileToDir {
					if dirID == n.ID {
						if d, ok := depth[fileID]; ok {
							if minD == -1 || d < minD {
								minD = d
							}
						}
					}
				}
				if minD >= 0 {
					depth[n.ID] = minD
				}
			}
		}
	}

	weight := map[string]int{KindSymbol: 0, KindFuncDiff: 0, KindPackage: 1, KindFile: 2, KindTest: 3}
	type colItem struct {
		id     string
		weight int
	}
	cols := map[int][]colItem{}
	for _, n := range nodes {
		d, ok := depth[n.ID]
		if !ok {
			d = maxDepth + 1
			depth[n.ID] = d
		}
		w := weight[n.Kind]
		cols[d] = append(cols[d], colItem{n.ID, w})
	}
	colNums := make([]int, 0, len(cols))
	for d := range cols {
		colNums = append(colNums, d)
	}
	sort.Ints(colNums)
	pos := map[string][2]int{}
	width, height := 0, 0
	for _, d := range colNums {
		items := cols[d]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].weight != items[j].weight {
				return items[i].weight < items[j].weight
			}
			return items[i].id < items[j].id
		})
		x := orgX + d*(nodeW+colGap)
		for y, it := range items {
			pos[it.id] = [2]int{x, orgY + y*(nodeH+rowGap)}
		}
		if h := orgY + len(items)*(nodeH+rowGap); h > height {
			height = h
		}
		if w := x + nodeW + orgX; w > width {
			width = w
		}
	}
	return pos, width, height
}

// nodeFill 节点配色（kind × changed）。
func nodeFill(n Node) (fill, stroke string) {
	if n.Changed {
		return "#fecaca", "#dc2626"
	}
	switch n.Kind {
	case KindPackage:
		return "#dbeafe", "#2563eb"
	case KindSymbol:
		return "#fef9c3", "#ca8a04"
	case KindTest:
		return "#dcfce7", "#16a34a"
	case KindTable: // v5.7.3 SQL 血缘枢纽的专属视觉编码
		return "#ede9fe", "#7c3aed"
	case KindFuncDiff: // v5.7.4 函数级变更:状态三色(新增绿/删除红/变更黄)
		switch {
		case strings.HasPrefix(n.Detail, FuncAdded):
			return "#86efac", "#16a34a"
		case strings.HasPrefix(n.Detail, FuncRemoved):
			return "#fca5a5", "#dc2626"
		default:
			return "#fde68a", "#d97706"
		}
	default:
		return "#e2e8f0", "#64748b"
	}
}

// svgBody 产出 SVG 主体（框 + 标签 + 贝塞尔边）。
// viewNodes/viewEdges 是布局与绘制共用的剪枝后视图集（见 viewGraph）。
func svgBody(pos map[string][2]int, viewNodes []Node, idxs []int, viewEdges []Edge, fcount map[string]int) string {
	var b strings.Builder
	// 边先画（垫底）；data-i 携带快照边序——JS 搜索联动按它回查 data.edges,
	// 不依赖「DOM 序=数据序」的隐式对齐。边标签包 .edgeg 组默认隐藏、悬停
	// 显示（v3.185.2：20 条边的中点小字互相糊叠是实测遮挡源，关系详情走
	// tooltip 与点选面板）
	for k, e := range viewEdges {
		i := idxs[k]
		p1, ok1 := pos[e.From]
		p2, ok2 := pos[e.To]
		if !ok1 || !ok2 {
			continue
		}
		x1 := p1[0] + nodeW
		y1 := p1[1] + nodeH/2
		x2 := p2[0]
		y2 := p2[1] + nodeH/2
		mid := (x1 + x2) / 2
		fmt.Fprintf(&b, `<g class="edgeg"><path data-i="%d" d="M%d,%d C%d,%d %d,%d %d,%d" class="edge rel-%s"/><text class="elabel" x="%d" y="%d">%s</text></g>`+"\n",
			i, x1, y1, mid, y1, mid, y2, x2, y2, e.Rel, mid, y2-6, html.EscapeString(mermaidRel(e.Rel)))
	}
	for _, n := range viewNodes {
		p, ok := pos[n.ID]
		if !ok {
			continue
		}
		fill, stroke := nodeFill(n)
		label := truncateRunes(n.Label, 30)
		tip := n.Kind + " · " + n.ID
		if n.Detail != "" {
			tip += "\n" + n.Detail
		}
		fmt.Fprintf(&b, `<g class="node kind-%s" data-id="%s" transform="translate(%d,%d)"><title>%s</title><rect width="%d" height="%d" rx="6" fill="%s" stroke="%s"/><text x="12" y="20">%s</text>`,
			n.Kind, html.EscapeString(n.ID), p[0], p[1],
			html.EscapeString(tip), nodeW, nodeH, fill, stroke, html.EscapeString(label))
		// v5.8.0 findings 徽标:有审查意见的文件节点右上角红点+计数
		if c := fcount[n.ID]; c > 0 {
			fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="9" fill="#dc2626"/><text x="%d" y="%d" text-anchor="middle" font-size="11" fill="#fff" pointer-events="none">%d</text>`,
				nodeW-6, 6, nodeW-6, 10, c)
		}
		fmt.Fprintf(&b, `</g>`+"\n")
	}
	return b.String()
}

// truncateRunes 显示截断（尾部省略号）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// htmlTemplate 单文件 HTML（内联样式与脚本，零外链）。title/headLabel 支撑
// 影响面与仓库地图双页头（v5.6.2）。
// impactLegend/impactLegendEdges 影响面页图例（v3.183.0 原文，v6.1.4 参数化
// 以支持地图页分语境）。
func impactLegend() string {
	return `<span class="lgd lgd-chg"></span>改动文件/符号（红=直接改动，黄=改动函数有调用方）<span class="lgd lgd-pkg"></span>包<span class="lgd lgd-tst"></span>测试<span class="lgd lgd-tbl"></span>SQL 表<span class="lgd lgd-file"></span>受影响文件`
}

func impactLegendEdges() string {
	return `import=波及依赖包 · caller=波及调用方 · source=波及装载方 · lineage=表血缘 · test_of=需跑测试 · coupled=历史共变`
}

// repoLegend/repoLegendEdges 仓库地图页图例（v6.1.4）：无「改动/波及」语境，
// 文件节点=仓库自有文件（Detail=导出符号清单）。
func repoLegend() string {
	return `<span class="lgd lgd-pkg"></span>包（目录）<span class="lgd lgd-file"></span>文件（Detail=导出符号）`
}

func repoLegendEdges() string {
	return `import=跨包依赖 · contains=包含（目录→文件，仅小仓展开） · source=脚本装载 · lineage=表血缘`
}

func htmlTemplate(title, headLabel, riskBadgeStr string, s *Snapshot, taskID, dataJSON, svg string, w, h int, findingsJSON, cmpSummary, historyJSON, legendNodes, legendEdges, foldedSymsJSON string) string {
	var notes strings.Builder
	for _, n := range s.Notes {
		notes.WriteString("<li>" + html.EscapeString(n) + "</li>")
	}
	// v5.8.0 findings 数据块(空则整块省略)
	fScript := ""
	if findingsJSON != "" {
		fScript = `<script type="application/json" id="impact-findings">` + findingsJSON + `</script>`
	}
	hScript := ""
	if historyJSON != "" {
		hScript = `<script type="application/json" id="impact-history">` + historyJSON + `</script>`
	}
	fsScript := ""
	if foldedSymsJSON != "" {
		fsScript = `<script type="application/json" id="impact-folded-syms">` + foldedSymsJSON + `</script>`
	}
	var riskList strings.Builder
	for _, r := range s.Risk.Reasons {
		riskList.WriteString("<li>" + html.EscapeString(r) + "</li>")
	}
	return `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8">
<title>` + title + ` · 风险 ` + riskBadgeStr + `</title>
<style>
body{font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;margin:0;color:#0f172a;background:#f8fafc}
header{padding:12px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:16px;align-items:center;flex-wrap:wrap}
header h1{font-size:16px;margin:0}
.badge{padding:2px 10px;border-radius:999px;font-weight:600}
.badge.high{background:#fecaca;color:#7f1d1d}.badge.medium{background:#fef3c7;color:#78350f}.badge.low{background:#dcfce7;color:#14532d}
.meta{color:#64748b;font-size:12px}
#toolbar{padding:8px 20px;background:#fff;border-bottom:1px solid #e2e8f0;display:flex;gap:8px;align-items:center}
#toolbar input{flex:0 1 280px;padding:4px 10px;border:1px solid #cbd5e1;border-radius:6px}
button{padding:4px 12px;border:1px solid #cbd5e1;background:#fff;border-radius:6px;cursor:pointer}
button:hover{background:#f1f5f9}
#viewport{overflow:hidden;height:calc(100vh - 118px);cursor:grab;position:relative;background:#f8fafc}
#viewport.dragging{cursor:grabbing}
svg{min-width:100%}
.node rect{stroke-width:1.5}
.node{cursor:pointer}
.node text{font-size:12px;fill:#0f172a;pointer-events:none}
.node.dim{opacity:.15}
.edge{fill:none;stroke:#94a3b8;stroke-width:1.2;marker-end:url(#arrow)}
.edge.dim{opacity:.12}
.edgeg text.elabel{opacity:0;transition:opacity .15s;pointer-events:none}
.edgeg:hover text.elabel{opacity:1}
.edgeg:hover path.edge{stroke:#475569;stroke-width:2}
.elabel{font-size:10px;fill:#94a3b8}
aside{position:fixed;right:0;top:0;bottom:0;width:320px;background:#fff;border-left:1px solid #e2e8f0;padding:16px;display:none;overflow:auto}
aside h3{margin:0 0 8px;font-size:14px}
aside .kv{margin:2px 0;font-size:12px;color:#334155;word-break:break-all}
aside .kv b{color:#64748b;font-weight:500;margin-right:6px}
details{margin:8px 20px}
summary{cursor:pointer;color:#334155}
#kinds{display:inline-flex;gap:4px;margin:0 6px}
.kchip{padding:2px 10px;border:1px solid #cbd5e1;border-radius:999px;background:#fff;font-size:12px;cursor:pointer;user-select:none}
.kchip.on{background:#1e293b;color:#fff;border-color:#1e293b}
.node.focus rect{stroke:#2563eb;stroke-width:2.5}
.lgd{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 4px 0 8px;vertical-align:-1px}
.lgd-chg{background:#fecaca;border:1px solid #dc2626}.lgd-sym{background:#fef9c3;border:1px solid #ca8a04}.lgd-pkg{background:#dbeafe;border:1px solid #2563eb}.lgd-tst{background:#dcfce7;border:1px solid #16a34a}.lgd-tbl{background:#ede9fe;border:1px solid #7c3aed}.lgd-file{background:#e2e8f0;border:1px solid #64748b}
.cplrow{cursor:pointer;padding:2px 0;font-size:12px;color:#334155}
.cplrow:hover{color:#2563eb;text-decoration:underline}
/* v6.9.4 暗色模式：prefers-color-scheme 媒体查询自动切换 */
@media(prefers-color-scheme:dark){
 body{color:#e2e8f0;background:#0f172a}
 header{background:#1e293b;border-color:#334155}
 #toolbar{background:#1e293b;border-color:#334155}
 #viewport{background:#0f172a}
 button{background:#1e293b;color:#e2e8f0;border-color:#334155}
 button:hover{background:#334155}
 #toolbar input{background:#0f172a;color:#e2e8f0;border-color:#334155}
 aside{background:#1e293b;border-color:#334155}
 aside .kv{color:#cbd5e1}
 aside .kv b{color:#94a3b8}
 summary{color:#94a3b8}
 details{color:#cbd5e1}
 .kchip{background:#1e293b;color:#e2e8f0;border-color:#334155}
 .kchip.on{background:#3b82f6;color:#fff;border-color:#3b82f6}
 .cplrow{color:#cbd5e1}
 .cplrow:hover{color:#60a5fa}
 .node text{fill:#e2e8f0}
 .edge{stroke:#64748b}
 .edgeg:hover path.edge{stroke:#94a3b8}
 .elabel{fill:#64748b}
 .meta{color:#94a3b8}
 .badge.high{background:#7f1d1d;color:#fecaca}
 .badge.medium{background:#78350f;color:#fef3c7}
 .badge.low{background:#14532d;color:#dcfce7}
}
</style></head><body>
<header>
<h1>` + title + `</h1>
<span class="badge ` + s.Risk.Level + `">风险 ` + riskBadgeStr + ` · ` + fmt.Sprint(s.Risk.Score) + ` 分</span>
<span class="meta">` + html.EscapeString(fmt.Sprintf("%d 改动 · %d 节点 · %d 边 · 调用位 %d · 共变 %d", len(s.Changed), len(s.Nodes), len(s.Edges), s.Stats.CallSites, len(s.Coupling))) + `</span>
<span class="meta">` + headLabel + `</span>
</header>
<div id="toolbar">
<input id="q" placeholder="搜索文件 / 包 / 符号…">
<span id="kinds"></span>
<button id="zin">放大</button><button id="zout">缩小</button><button id="fit">复位</button>
<button id="export">导出 SVG</button>
<button id="closepanel" style="display:none">收起详情</button>
</div>
` + cmpSummary + `<details><summary>风险依据（` + fmt.Sprint(len(s.Risk.Reasons)) + ` 条）</summary><ul>` + riskList.String() + `</ul></details>
<details><summary>🗺 图例与交互</summary><div style="font-size:12px;color:#334155;line-height:2">
节点：` + legendNodes + `<br>
边：` + legendEdges + `<br>
徽标：文件节点右上角红点数字=该文件审查意见数（点节点看意见摘要+历史档案）。
历史：点选文件节点的「历史档案」= 跨 MR 意见史与人工抑制计数（误报倾向参考）。交互：点节点=聚焦一跳邻居+详情侧栏；搜索/类型 chips 过滤；悬停边看关系；「导出 SVG」离线存档。改动符号函数级 diff 见「函数级图 diff」区块，卡片可点击联动聚焦。
</div></details>
` + couplingDetails(s) + `
` + funcDiffDetails(s) + `<details><summary>降级注明（` + fmt.Sprint(len(s.Notes)) + ` 条）</summary><ul>` + notes.String() + `</ul></details>
<div id="viewport"><svg id="g" width="` + fmt.Sprint(w) + `" height="` + fmt.Sprint(h) + `" viewBox="0 0 ` + fmt.Sprint(w) + ` ` + fmt.Sprint(h) + `">
<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0L10,5L0,10z" fill="#94a3b8"/></marker></defs>
` + svg + `</svg></div>
<aside id="panel"></aside>
<script type="application/json" id="impact-data">` + dataJSON + `</script>
` + fScript + hScript + fsScript + `
<script>
const data=JSON.parse(document.getElementById('impact-data').textContent);
const byId=Object.fromEntries(data.nodes.map(n=>[n.id,n]));
const fdata=(()=>{const el=document.getElementById('impact-findings');try{return el?JSON.parse(el.textContent):[];}catch(e){return [];}})();
const hdata=(()=>{const el=document.getElementById('impact-history');try{return el?JSON.parse(el.textContent):{};}catch(e){return {};}})();
const folddata=(()=>{const el=document.getElementById('impact-folded-syms');try{return el?JSON.parse(el.textContent):{};}catch(e){return {};}})();
const svg=document.getElementById('g'),vp=document.getElementById('viewport'),q=document.getElementById('q'),panel=document.getElementById('panel');
let scale=1,tx=0,ty=0;
function apply(){svg.style.transformOrigin='0 0';svg.style.transform='translate('+tx+'px,'+ty+'px) scale('+scale+')';}
vp.addEventListener('wheel',e=>{e.preventDefault();const f=e.deltaY<0?1.15:1/1.15;const r=vp.getBoundingClientRect();const mx=e.clientX-r.left,my=e.clientY-r.top;tx=mx-(mx-tx)*f;ty=my-(my-ty)*f;scale=Math.min(4,Math.max(.2,scale*f));apply();},{passive:false});
let drag=null;
vp.addEventListener('mousedown',e=>{drag={x:e.clientX-tx,y:e.clientY-ty};vp.classList.add('dragging');});
window.addEventListener('mousemove',e=>{if(!drag)return;tx=e.clientX-drag.x;ty=e.clientY-drag.y;apply();});
window.addEventListener('mouseup',()=>{drag=null;vp.classList.remove('dragging');});
document.getElementById('zin').onclick=()=>{scale=Math.min(4,scale*1.25);apply();};
document.getElementById('zout').onclick=()=>{scale=Math.max(.2,scale/1.25);apply();};
document.getElementById('fit').onclick=()=>{scale=1;tx=0;ty=0;q.value='';setFocus(null);filter('');panel.style.display='none';apply();};
q.addEventListener('input',()=>filter(q.value.trim().toLowerCase()));
// v5.6.0 探索器增强 ①类型过滤 chips（file/symbol/table/package/test 计数）
const kindFilter=new Set();
const kindsEl=document.getElementById('kinds');
const kindCounts={};data.nodes.forEach(n=>{kindCounts[n.kind]=(kindCounts[n.kind]||0)+1;});
Object.keys(kindCounts).sort().forEach(k=>{
 const b=document.createElement('span');b.className='kchip on';b.dataset.k=k;
 b.textContent=k+' '+kindCounts[k];
 b.onclick=()=>{if(kindFilter.has(k)){kindFilter.delete(k);b.classList.remove('on');}else{kindFilter.add(k);b.classList.add('on');}filter(q.value.trim().toLowerCase());};
 kindsEl.appendChild(b);
});
// v6.9.6 方向 W：「只看改动」过滤 chip
(function(){
 const b=document.createElement('span');b.className='kchip';b.dataset.k='__changed';
 b.textContent='只看改动';
 let onlyChanged=false;
 b.onclick=()=>{
  onlyChanged=!onlyChanged;
  b.classList.toggle('on',onlyChanged);
  document.querySelectorAll('#g .node').forEach(g=>{
   const n=byId[g.dataset.id];
   if(onlyChanged&&n&&!n.changed){g.classList.add('dim');}
   else if(!onlyChanged){g.classList.remove('dim');}
  });
 };
 kindsEl.appendChild(b);
})();
let focusId=null,focusSet=null;
function neighborhood(id){
 const s=new Set([id]);
 data.edges.forEach(e=>{if(e.from===id)s.add(e.to);if(e.to===id)s.add(e.from);});
 return s;
}
function setFocus(id){focusId=id;focusSet=id?neighborhood(id):null;}
function filter(kw){
 const hit={};
 document.querySelectorAll('#g .node').forEach(g=>{
  const id=g.dataset.id,n=byId[id];
  const kwOK=!kw||(id.toLowerCase().includes(kw)||(n&&(n.label||'').toLowerCase().includes(kw))||(n&&(n.detail||'').toLowerCase().includes(kw)));
  const kindOK=!kindFilter.size||!n||!kindFilter.has(n.kind);
  const focusOK=!focusId||id===focusId||focusSet.has(id);
  hit[id]=kwOK&&kindOK&&focusOK;
  g.classList.toggle('dim',!hit[id]);
  g.classList.toggle('focus',focusId===id);
 });
 // 边联动：kw 为空全亮；有 kw 时仅两端都命中的边保持可见
 //（data-i 回查快照边序，不依赖 DOM 序=数据序）
 document.querySelectorAll('#g .edge').forEach(p=>{
  if(!kw&&!kindFilter.size&&!focusId){p.classList.remove('dim');return;}
  const e=data.edges[+p.dataset.i];
  p.classList.toggle('dim',!(e&&hit[e.from]&&hit[e.to]));
 });
 // v6.9.3 方向 D：搜索命中自动定位——第一个命中节点居中视口
 if(kw){
  let firstHit=null;
  document.querySelectorAll('#g .node:not(.dim)').forEach(g=>{if(!firstHit)firstHit=g;});
  if(firstHit){
   const vb=svg.viewBox.baseVal;
   const rect=firstHit.getBoundingClientRect();
   const svgRect=svg.getBoundingClientRect();
   const nodeX=(rect.left+rect.width/2-svgRect.left)/scale;
   const nodeY=(rect.top+rect.height/2-svgRect.top)/scale;
   tx=vb.width/2-nodeX; ty=vb.height/2-nodeY;
   apply();
  }
 }
}
svg.addEventListener('click',e=>{
 const g=e.target.closest('.node');if(!g)return;
 const n=byId[g.dataset.id];if(!n)return;
 showPanel(n);
});
// v5.7.3 关联联动：函数级 diff 卡片/共变面板 → 聚焦对应文件节点并开侧栏
window.focusFileNode=function(fileId){
 const n=byId[fileId];if(!n)return;
 setFocus(fileId);filter(q.value.trim().toLowerCase());
 showPanel(byId[fileId]);
 document.getElementById('viewport').scrollIntoView({behavior:'smooth',block:'center'});
};
function showPanel(n){
 // v5.7.3 邻域聚焦：点选节点高亮其一跳邻居，再点同节点/复位取消
 setFocus(focusId===n.id?null:n.id);
 filter(q.value.trim().toLowerCase());
 const edgesIn=data.edges.filter(x=>x.to===n.id),edgesOut=data.edges.filter(x=>x.from===n.id);
 const li=a=>a.map(x=>'<div class="kv">'+(x.rel||'')+' → '+(byId[x.to]?byId[x.to].label:x.to)+'</div>').join('')||'<div class="kv">—</div>';
 // v5.7.4 函数级变更节点：完整 before/after 签名与调用增减
 if(n.kind==='funcdiff'){
  const m=(n.id||'').match(/^fd:(.+):([^:]+)$/);
  const fd=(data.func_diffs||[]).find(f=>f.file===m[1]&&f.name===m[2]);
  if(fd){
   const stTxt={added:'🟢 新增',removed:'🔴 删除'}[fd.status]||'🟡 变更';
   const card=x=>'<pre style="background:#f1f5f9;padding:6px;border-radius:6px;font-size:12px;margin:2px 0;white-space:pre-wrap;word-break:break-all">'+esc(x||'（无）')+'</pre>';
   panel.innerHTML='<h3>'+stTxt+' '+esc(fd.name)+'()</h3>'
    +'<div class="kv"><b>文件</b>'+esc(fd.file)+'</div>'
    +'<div class="kv"><b>签名</b>'+(fd.sig_same?'未变化':'已变化')+'</div>'
    +'<h3 style="margin-top:12px">BEFORE</h3>'+card(fd.before&&fd.before.signature)
    +'<h3 style="margin-top:8px">AFTER</h3>'+card(fd.after&&fd.after.signature)
    +(((fd.calls_added||[]).length||((fd.calls_removed||[]).length))?'<h3 style="margin-top:8px">调用增减</h3>'+(fd.calls_added||[]).slice(0,6).map(c=>'<div class="kv">+ '+esc(c)+'</div>').join('')+(fd.calls_removed||[]).slice(0,6).map(c=>'<div class="kv">− '+esc(c)+'</div>').join(''):'');
   panel.style.display='block';
   document.getElementById('closepanel').style.display='inline-block';
   return;
  }
 }
 // v6.9.2 方向 C 实际落地：目录聚合节点（dir: 前缀）点击展开成员文件列表
 let dirExtra='';
 if(n.kind==='package'&&n.id.startsWith('dir:')){
  const dirPath=n.id.slice(4);
  const members=(data.nodes||[]).filter(x=>x.id.startsWith('file:')&&x.id.slice(5).startsWith(dirPath+'/'));
  if(members.length){
   dirExtra='<h3 style="margin-top:12px">目录聚合</h3><div class="kv"><b>路径</b>'+esc(dirPath)+'</div>'
    +'<div class="kv"><b>文件数</b>'+members.length+'</div>'
    +'<h3 style="margin-top:8px">成员文件</h3>'+members.map(m=>'<div class="kv">'+esc(m.label)+'</div>').join('');
  }
 }
 // v5.7.4 包聚合详情：文件数/测试覆盖/对外暴露/深度
 let pkgExtra='';
 if(n.kind==='package'&&data.packages){
  const p=(data.packages||[]).find(x=>'pkg:'+x.path===n.id);
  if(p){
   pkgExtra='<div class="kv"><b>Go 文件</b>'+p.files+'</div>'
    +'<div class="kv"><b>测试</b>'+(p.tests>0?p.tests+' 个 *_test.go':'<b style="color:#dc2626">无测试覆盖</b>')+'</div>'
    +(p.exposed?'<div class="kv"><b>暴露面</b>被受影响集之外的包依赖</div>':'')
    +'<div class="kv"><b>距改动</b>'+p.depth+' 跳'+(p.direct_change?'（含直接改动）':'')+'</div>';
  }
 }
 // 关联融合：函数级 diff（文件节点的改动函数）与历史共变（import 图外隐性耦合）
 const filePath=n.id.startsWith('file:')?n.id.slice(5):null;
 const stIcon={added:'🟢',removed:'🔴'};
 let extra='';
 if(n.kind==='package'&&dirExtra){extra=dirExtra;}
 else if(n.kind==='package'&&pkgExtra){extra='<h3 style="margin-top:12px">包聚合</h3>'+pkgExtra;}
 else if(filePath){
  // v6.9.3 方向 E：被折叠函数可点击 → 触发搜索定位
  const folded=folddata[n.id]||[];
  if(folded.length){
   extra+='<h3 style="margin-top:12px">被折叠函数 '+folded.length+'</h3>'+folded.map(fn=>'<div class="kv cplrow" onclick="q.value=\''+esc(fn).replace(/'/g,"\\'")+';q.dispatchEvent(new Event(\'input\'))">'+esc(fn)+'</div>').join('');
  }
  const fds=(data.func_diffs||[]).filter(fd=>fd.file===filePath);
  if(fds.length){
   extra+='<h3 style="margin-top:12px">函数级变更 '+fds.length+'</h3>'+fds.map(fd=>{
    const ic=stIcon[fd.status]||'🟡';
    const ln=((fd.before&&fd.before.lines)??'—')+'→'+((fd.after&&fd.after.lines)??'—');
    return '<div class="kv">'+ic+' <b>'+esc(fd.name)+'</b>'+(fd.sig_same?'（签名未变）':'')+' 行数 '+ln+'</div>'
     +(fd.calls_added||[]).slice(0,4).map(c=>'<div class="kv">+ '+esc(c)+'</div>').join('')
     +(fd.calls_removed||[]).slice(0,4).map(c=>'<div class="kv">− '+esc(c)+'</div>').join('');
   }).join('');
  }
  const coup=(data.coupling||[]).filter(c=>c.file===filePath||c.other===filePath);
  if(coup.length){
   extra+='<h3 style="margin-top:12px">历史共变 '+coup.length+'</h3>'+coup.map(c=>{
    const o=c.other===filePath?c.file:c.other;
    return '<div class="cplrow" onclick="window.focusFileNode && window.focusFileNode(\'file:'+esc(o)+'\')">'+esc(o)+' ×'+c.count+'</div>';
   }).join('');
  }
  // v5.8.0/v5.8.2 审查意见挂图+历史档案：该文件的 findings 与跨 MR 意见史
  if(hdata[filePath]&&hdata[filePath].total){
   const h=hdata[filePath];
   extra+='<h3 style="margin-top:12px">历史档案</h3><div class="kv">该文件历史上共 <b>'+h.total+'</b> 条意见'+(h.suppressed>0?'，其中 <b>'+h.suppressed+'</b> 条被人工抑制（误报倾向参考，审阅可降权）':'')+'</div>';
  }
  if(fdata.length){
   const fs=fdata.filter(f=>f.file===filePath);
   if(fs.length){
    const sev={high:'🔴',medium:'🟡',critical:'🔴',low:'🟢'};
    extra+='<h3 style="margin-top:12px">审查意见 '+fs.length+'</h3>'+fs.map(f=>
     '<div class="kv">'+(sev[(f.severity||'').toLowerCase()]||'⚪')+' <b>'+(f.line||'?')+':</b> '+esc(f.comment)
     +(f.fp?' <span class="cplrow" title="点击复制修复指令" onclick="navigator.clipboard&&navigator.clipboard.writeText(\'fix fp:'+esc(String(f.fp).slice(0,8))+'\')">📋fp:'+esc(String(f.fp).slice(0,8))+'</span>':'')+'</div>').join('')
     +'<div class="kv" style="color:#64748b">完整意见与修复建议见任务报告</div>';
   }
  }
 }
 panel.innerHTML='<h3>'+esc(n.label)+'</h3>'
  +'<div class="kv"><b>类型</b>'+esc(n.kind)+(n.changed?'（本轮改动）':'')+'</div>'
  +(n.detail?'<div class="kv"><b>详情</b>'+esc(n.detail)+'</div>':'')
  +'<div class="kv"><b>ID</b>'+esc(n.id)+'</div>'
  // v6.9.6 方向 S：复制文件路径按钮
  +(n.id.startsWith('file:')?' <span class="cplrow" title="点击复制路径" onclick="navigator.clipboard&&navigator.clipboard.writeText(\''+esc(n.id.slice(5)).replace(/'/g,"\\'")+'\')">📋复制路径</span>':'')
  +'<div class="kv"><b>聚焦</b>'+(focusId?'已聚焦一跳邻居（再点本节点取消）':'未聚焦')+'</div>'
  +'<h3 style="margin-top:12px">入边 '+edgesIn.length+'</h3>'+li(edgesIn)
  +'<h3 style="margin-top:12px">出边 '+edgesOut.length+'</h3>'+li(edgesOut)
  +extra;
 panel.style.display='block';
 document.getElementById('closepanel').style.display='inline-block';
}
// 函数级 diff 卡片联动（funcDiffDetails 产物）
document.querySelectorAll('.fdcard[data-fdfile]').forEach(el=>{
 el.addEventListener('click',()=>window.focusFileNode('file:'+el.dataset.fdfile));
});
document.getElementById('closepanel').onclick=function(){panel.style.display='none';this.style.display='none';};
// v5.6.0 探索器增强 ③导出 SVG：克隆图+内联样式单文件落盘（离线评审/贴周报用）
document.getElementById('export').onclick=()=>{
 const clone=svg.cloneNode(true);
 const st=document.createElementNS('http://www.w3.org/2000/svg','style');
 st.textContent=Array.from(document.styleSheets).flatMap(ss=>{try{return Array.from(ss.cssRules).map(r=>r.cssText);}catch(e){return [];}}).join('\n');
 clone.insertBefore(st,clone.firstChild);
 clone.setAttribute('xmlns','http://www.w3.org/2000/svg');
 const blob=new Blob([new XMLSerializer().serializeToString(clone)],{type:'image/svg+xml'});
 const a=document.createElement('a');a.href=URL.createObjectURL(blob);
 a.download='argus-impact-'+(data&&data.nodes?data.nodes.length:'x')+'.svg';a.click();
 setTimeout(()=>URL.revokeObjectURL(a.href),3000);
};
function esc(s){return String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));}
// v6.9.5 方向 R：URL 参数直达——?focus=<nodeId> 自动聚焦并开侧栏
(function(){
 const params=new URLSearchParams(location.search);
 const focus=params.get('focus');
 if(focus&&byId[focus]){focusFileNode(focus);}
})();
apply();
</script></body></html>`
}

// taskMeta 页头任务标识（空省略）。
func taskMeta(taskID string) string {
	if taskID == "" {
		return ""
	}
	return "任务 " + html.EscapeString(taskID)
}

// findingsByFile 文件→意见数(徽标渲染输入)。
func findingsByFile(findings []plugin.Finding) map[string]int {
	m := map[string]int{}
	for _, f := range findings {
		if f.File != "" {
			m["file:"+f.File]++
		}
	}
	return m
}

// findingsJSON 渲染面瘦身子集(file/line/severity/fp/comment 摘要/by)。
func findingsJSON(findings []plugin.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	type slim struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Severity string `json:"severity"`
		FP       string `json:"fp,omitempty"`
		By       string `json:"by,omitempty"`
		Comment  string `json:"comment"`
	}
	out := make([]slim, 0, len(findings))
	for _, f := range findings {
		c := []rune(f.Comment)
		if len(c) > 160 {
			c = append(c[:160], []rune("…")...)
		}
		out = append(out, slim{File: f.File, Line: f.Line, Severity: f.Severity,
			FP: f.FP, By: f.By, Comment: string(c)})
	}
	b, _ := json.Marshal(out)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// historyJSON 文件意见历史档案的瘦身影射(file→total/suppressed,仅保留 total≥3 的惯犯)。
func historyJSON(h map[string]storage.FindingFileHistory) string {
	if len(h) == 0 {
		return ""
	}
	out := map[string]storage.FindingFileHistory{}
	for f, v := range h {
		if v.Total >= 3 {
			out[f] = v
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// couplingDetails 历史共变面板（v5.7.3）：Top 共变对可点击联动聚焦主图——
// import 图外的隐性耦合（同 commit 反复共现）是评审最容易漏的关联面。
func couplingDetails(s *Snapshot) string {
	if s == nil || len(s.Coupling) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details><summary>🔗 历史共变（` + fmt.Sprint(len(s.Coupling)) + ` 对，点击聚焦图中节点）</summary>`)
	for _, c := range s.Coupling {
		if c.Count < MinCoupleCount {
			continue
		}
		b.WriteString(`<div class="cplrow" onclick="window.focusFileNode &amp;&amp; window.focusFileNode(` +
			strconv.Quote("file:"+c.File) + `)">` +
			html.EscapeString(shortLabel(c.File)) + ` ↔ ` + html.EscapeString(shortLabel(c.Other)) +
			` <b>×` + fmt.Sprint(c.Count) + `</b>（同 commit 共现）</div>`)
	}
	b.WriteString(`</details>`)
	return b.String()
}

// funcDiffDetails 函数级图 diff 区块（before/after 双列卡片 + 调用边增减
// chips；无数据整块省略）。状态徽标：🟢 新增 / 🔴 删除 / 🟡 变更。
func funcDiffDetails(s *Snapshot) string {
	if s == nil || len(s.FuncDiffs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details open><summary>函数级图 diff（` + fmt.Sprint(len(s.FuncDiffs)) + ` · base 对比，🟢 新增 / 🔴 删除 / 🟡 变更）</summary>`)
	b.WriteString(`<style>
.fdcard{border:1px solid #e2e8f0;border-radius:8px;margin:8px 0;padding:10px;background:#fff}
.fdhead{display:flex;gap:8px;align-items:center;font-size:13px}
.fdst{padding:1px 8px;border-radius:999px;font-size:12px;font-weight:600}
.st-add{background:#dcfce7;color:#14532d}.st-rem{background:#fecaca;color:#7f1d1d}.st-mod{background:#fef9c3;color:#713f12}
.fdcols{display:flex;gap:8px;margin-top:8px}
.fdcol{flex:1;min-width:0}
.fdcol h4{margin:0 0 4px;font-size:11px;color:#64748b;font-weight:600}
.fdcol pre{background:#f1f5f9;padding:6px 8px;border-radius:6px;overflow:auto;font-size:12px;margin:0;white-space:pre-wrap;word-break:break-all}
.fdcalls{margin-top:6px;font-size:12px}
.chip{display:inline-block;padding:1px 8px;border-radius:999px;margin:2px 4px 0 0;font-size:12px}
.chip-add{background:#dcfce7;color:#14532d}.chip-rem{background:#fecaca;color:#7f1d1d}
</style><div>`)
	shown := s.FuncDiffs
	if len(shown) > 12 {
		shown = shown[:12]
	}
	for _, fd := range shown {
		var st, stCls string
		switch fd.Status {
		case FuncAdded:
			st, stCls = "新增", "st-add"
		case FuncRemoved:
			st, stCls = "删除", "st-rem"
		default:
			st, stCls = "变更", "st-mod"
		}
		b.WriteString(`<div class="fdcard" data-fdfile="` + html.EscapeString(fd.File) + `" style="cursor:pointer" title="点击在图中聚焦该文件"><div class="fdhead"><span class="fdst ` + stCls + `">` + st + `</span><b>` + html.EscapeString(fd.Name) + `</b><span style="color:#64748b;font-size:12px">` + html.EscapeString(fd.File) + `</span></div>`)
		before, after := "", ""
		if fd.Before != nil {
			before = fd.Before.Signature
		}
		if fd.After != nil {
			after = fd.After.Signature
		}
		if fd.Status != FuncAdded && before != "" {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>` + html.EscapeString(before) + `</pre></div>`)
		} else {
			b.WriteString(`<div class="fdcols"><div class="fdcol"><h4>BEFORE</h4><pre>（无）</pre></div>`)
		}
		if fd.Status != FuncRemoved && after != "" {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>` + html.EscapeString(after) + `</pre></div></div>`)
		} else {
			b.WriteString(`<div class="fdcol"><h4>AFTER</h4><pre>（已删除）</pre></div></div>`)
		}
		if len(fd.CallsAdded) > 0 || len(fd.CallsRemoved) > 0 {
			b.WriteString(`<div class="fdcalls">`)
			for _, c := range capShown(fd.CallsAdded, 6) {
				b.WriteString(`<span class="chip chip-add">+ ` + html.EscapeString(c) + `</span>`)
			}
			for _, c := range capShown(fd.CallsRemoved, 6) {
				b.WriteString(`<span class="chip chip-rem">− ` + html.EscapeString(c) + `</span>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	}
	if rest := len(s.FuncDiffs) - len(shown); rest > 0 {
		b.WriteString(`<div style="color:#64748b;font-size:12px">…其余 ` + fmt.Sprint(rest) + ` 条略（完整见 MCP get_impact / JSON 快照）</div>`)
	}
	b.WriteString(`</div></details>`)
	return b.String()
}
