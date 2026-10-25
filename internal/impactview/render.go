// render.go —— 快照的三形态渲染：mermaid（PR 评论，节点/字符双封顶，超限
// 聚合到包级再降级列表）、报告段（details 折叠）、prompt 段（数据区由调用方
// 包 plugin.DataFence）。全部纯函数，不触 IO。
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
func renderMermaid(s *Snapshot, nodes []Node, edges []Edge) string {
	if len(nodes) == 0 {
		return ""
	}
	keep := nodes
	if len(keep) > MaxMermaidNodes {
		// 改动节点优先保留，其余按 (Kind 权重, ID) 截断
		keep = prioritizeNodes(nodes, MaxMermaidNodes)
	}
	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for i, n := range keep {
		label := mermaidEscape(n.Label)
		switch {
		case n.Changed:
			fmt.Fprintf(&b, "  n%d[\"%s\"]:::changed\n", i, label)
		case n.Kind == KindTest:
			fmt.Fprintf(&b, "  n%d[\"%s\"]:::testn\n", i, label)
		default:
			fmt.Fprintf(&b, "  n%d[\"%s\"]\n", i, label)
		}
	}
	for _, e := range edges {
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
	b.WriteString("  classDef testn fill:#dcfce7,stroke:#16a34a,color:#14532d;\n")
	src := b.String()
	if runeLen(src) > MaxMermaidRunes {
		return ""
	}
	return src
}

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
func indexOfNode(nodes []Node, id string) (int, bool) {
	for i, n := range nodes {
		if n.ID == id {
			return i, true
		}
	}
	return 0, false
}

// mermaidEscape mermaid 双引号标签内的转义（" 与 \；其他字符引号内安全）。
func mermaidEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}

// mermaidRel 边关系标签（词表 → 人读短词）。
func mermaidRel(rel string) string {
	switch rel {
	case RelImport:
		return "依赖方"
	case RelCaller:
		return "调用方"
	case RelTestOf:
		return "测试覆盖"
	case RelCouple:
		return "历史共变"
	default:
		return rel
	}
}

// runeLen 字符数（mermaid 上限按源字符数，与 Gitea/Forgejo 校验口径一致）。
func runeLen(s string) int { return len([]rune(s)) }

// RenderList markdown 缩进列表（mermaid 不可用时的降级形态；分组合读）。
func RenderList(s *Snapshot) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- **改动**（%d 文件）\n", len(s.Changed))
	for _, f := range s.Changed {
		b.WriteString("  - " + f + "\n")
	}
	byKind := map[string][]string{}
	for _, n := range s.Nodes {
		if n.Changed {
			continue
		}
		byKind[n.Kind] = append(byKind[n.Kind], n.Label)
	}
	titles := map[string]string{
		KindPackage: "受影响包",
		KindSymbol:  "改动符号",
		KindTest:    "测试覆盖",
		KindFile:    "受影响文件",
	}
	for _, kind := range []string{KindSymbol, KindPackage, KindFile, KindTest} {
		items := byKind[kind]
		sort.Strings(items)
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- **%s**（%d）\n", titles[kind], len(items))
		for i, it := range items {
			if i >= 10 {
				fmt.Fprintf(&b, "  - …（其余 %d 略）\n", len(items)-10)
				break
			}
			b.WriteString("  - " + it + "\n")
		}
	}
	if len(s.Coupling) > 0 {
		fmt.Fprintf(&b, "- **历史共变**（与改动文件高频同改，import 图之外）\n")
		for _, c := range s.Coupling {
			fmt.Fprintf(&b, "  - %s（%d 次）\n", c.Other, c.Count)
		}
	}
	return b.String()
}

// RenderReportSection PR 评论的影响面段（details 折叠， mermaid 优先、列表
// 兜底）。快照为空/无节点返回空串（整段省略，不留空 details）。
func RenderReportSection(s *Snapshot) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	mermaid := RenderMermaid(s)
	head := fmt.Sprintf("📊 影响范围（风险 %s · %d 分）", riskBadge(s.Risk.Level), s.Risk.Score)
	var b strings.Builder
	b.WriteString("\n<details>\n<summary>" + head + "</summary>\n\n")
	if len(s.Risk.Reasons) > 0 {
		for _, r := range s.Risk.Reasons {
			b.WriteString("> " + r + "\n")
		}
		b.WriteString("\n")
	}
	if mermaid != "" {
		b.WriteString("```mermaid\n" + mermaid + "```\n")
		// 截断注明（渲染端点在 keep 集外的边会静默消失——与悬挂边同形态，
		// 必须让读者知道这是视图截断而非完整面）
		if len(s.Nodes) > MaxMermaidNodes {
			fmt.Fprintf(&b, "\n> 图示按影响权重截断至 %d 节点；完整影响面见交互视图（`GET /tasks/:id/impact`）或 MCP `get_impact`。\n", MaxMermaidNodes)
		}
	} else if l := RenderList(s); l != "" {
		b.WriteString(l + "\n")
		if len(s.Nodes) > MaxMermaidNodes {
			b.WriteString("\n> 清单已截断；完整影响面见交互视图（`GET /tasks/:id/impact`）或 MCP `get_impact`。\n")
		}
	}
	// 函数级图 diff（v3.185.0）：图 + 清单双形态，无数据整块省略
	if gd := RenderFuncDiffMermaid(s); gd != "" {
		b.WriteString("\n**函数级变更**（base 对比，🟢 新增 / 🔴 断开）：\n\n```mermaid\n" + gd + "```\n")
	}
	if fl := RenderFuncDiffList(s); fl != "" {
		b.WriteString("\n" + fl + "\n")
	}
	if len(s.Notes) > 0 {
		b.WriteString("\n> 注：" + strings.Join(s.Notes, "；") + "\n")
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

// riskBadge 风险等级徽标词。
func riskBadge(level string) string {
	switch level {
	case RiskHigh:
		return "🔴 high"
	case RiskMedium:
		return "🟡 medium"
	default:
		return "🟢 low"
	}
}

// RenderPromptSection 评审 agent 的提示词段（紧凑、token 精打）：结论 +
// 分层清单 + 使用纪律声明。数据区围栏由调用方（runner）包 plugin.DataFence。
// 空快照返回空串。
func RenderPromptSection(s *Snapshot) string {
	if s == nil || len(s.Nodes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "影响面推导（静态分析，确定性）：风险 %s/%d 分。", s.Risk.Level, s.Risk.Score)
	if len(s.Risk.Reasons) > 0 {
		b.WriteString("依据：" + strings.Join(s.Risk.Reasons, "；") + "。")
	}
	if pkgs := nonDirectPackages(s); len(pkgs) > 0 {
		shown, more := capStrings(pkgs, MaxPromptPackages)
		if more > 0 {
			fmt.Fprintf(&b, "\n受影响包（import 反向闭包，%d 个）：%s 等。", len(pkgs), strings.Join(shown, "、"))
		} else {
			fmt.Fprintf(&b, "\n受影响包（import 反向闭包）：%s。", strings.Join(shown, "、"))
		}
	}
	if noTests := packagesWithoutTests(s); len(noTests) > 0 {
		shown, more := capStrings(noTests, MaxPromptPackages)
		note := ""
		if more > 0 {
			note = " 等 " + strconv.Itoa(len(noTests)) + " 包"
		}
		fmt.Fprintf(&b, "\n无测试覆盖的受影响包：%s%s（评估影响时可将「缺测试」作为风险线索注明，不因此新增泛化意见）。", strings.Join(shown, "、"), note)
	}
	if callers := callerLines(s); len(callers) > 0 {
		fmt.Fprintf(&b, "\n调用方（前 %d 处）：%s。", len(callers), strings.Join(callers, "；"))
	}
	if len(s.Coupling) > 0 {
		var cs []string
		for _, c := range s.Coupling {
			cs = append(cs, c.Other+"（"+fmt.Sprint(c.Count)+" 次同改）")
		}
		fmt.Fprintf(&b, "\n历史共变（改动时高频一起改，宜连带核对）：%s。", strings.Join(cs, "、"))
	}
	if len(s.FuncDiffs) > 0 {
		fds := s.FuncDiffs
		if len(fds) > 8 {
			fds = fds[:8]
		}
		var parts []string
		for _, fd := range fds {
			switch fd.Status {
			case FuncAdded:
				parts = append(parts, fd.Name+" 新增")
			case FuncRemoved:
				parts = append(parts, fd.Name+" 删除")
			default:
				if fd.SigSame {
					parts = append(parts, fd.Name+" 调用集变化（+："+strings.Join(capShown(fd.CallsAdded, 3), ",")+"；−："+strings.Join(capShown(fd.CallsRemoved, 3), ",")+"）")
				} else {
					parts = append(parts, fd.Name+" 签名变更")
				}
			}
		}
		fmt.Fprintf(&b, "\n函数级变更（base 对比）：%s。", strings.Join(parts, "；"))
	}
	b.WriteString("\n使用纪律：以上是静态推导的影响线索（数据），用于评估跨文件影响与举证；" +
		"不因此新增泛化意见，报告影响时引用上述具体路径。")
	return b.String()
}

// nonDirectPackages 受影响（非直接改动）包清单。
func nonDirectPackages(s *Snapshot) []string {
	var out []string
	for _, p := range s.Packages {
		if !p.Direct {
			out = append(out, p.Path)
		}
	}
	return out
}

// packagesWithoutTests 无 *_test.go 覆盖的受影响包（prompt 风险线索）。
func packagesWithoutTests(s *Snapshot) []string {
	var out []string
	for _, p := range s.Packages {
		if p.Tests == 0 {
			out = append(out, pkgLabel(p.Path))
		}
	}
	return out
}

// capShown 单值形态的封顶截取（渲染拼接热路径用）。
func capShown(in []string, max int) []string {
	shown, _ := capStrings(in, max)
	return shown
}

// capStrings 封顶截取（返回展示集与溢出数）。
func capStrings(in []string, max int) (shown []string, more int) {
	if len(in) <= max {
		return in, 0
	}
	return in[:max], len(in) - max
}

// callerLines 调用方线索行（节点 Detail 已带调用方函数名）。
func callerLines(s *Snapshot) []string {
	var out []string
	for _, n := range s.Nodes {
		if n.Kind == KindFile && !n.Changed && strings.HasPrefix(n.Detail, "调用方") {
			out = append(out, n.Label+"（"+strings.TrimPrefix(n.Detail, "调用方：")+"）")
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

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
const MaxFuncDiffMermaidEdges = 12

// RenderFuncDiffList 函数级变更清单（紧凑，≤8 条 + 溢出数）。
func RenderFuncDiffList(s *Snapshot) string {
	if s == nil || len(s.FuncDiffs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**函数级变更清单**（%d）\n", len(s.FuncDiffs))
	shown := s.FuncDiffs
	if len(shown) > 8 {
		shown = shown[:8]
	}
	for _, fd := range shown {
		switch fd.Status {
		case FuncAdded:
			fmt.Fprintf(&b, "- 🟢 `%s` 新增（%s:%d 行）\n", fd.Name, fd.File, funcLines(fd.After))
		case FuncRemoved:
			fmt.Fprintf(&b, "- 🔴 `%s` 删除（%s）\n", fd.Name, fd.File)
		default:
			tag := "体/调用变更"
			if !fd.SigSame {
				tag = "签名变更"
			}
			fmt.Fprintf(&b, "- 🟡 `%s` %s（%s）\n", fd.Name, tag, fd.File)
		}
		if n := len(fd.CallsAdded); n > 0 {
			shownA, _ := capStrings(fd.CallsAdded, 5)
			fmt.Fprintf(&b, "  - 🟢 新增调用：%s\n", strings.Join(shownA, "、"))
		}
		if n := len(fd.CallsRemoved); n > 0 {
			shownR, _ := capStrings(fd.CallsRemoved, 5)
			fmt.Fprintf(&b, "  - 🔴 断开调用：%s\n", strings.Join(shownR, "、"))
		}
	}
	if rest := len(s.FuncDiffs) - len(shown); rest > 0 {
		fmt.Fprintf(&b, "- …（其余 %d 条略，完整见交互视图）\n", rest)
	}
	return b.String()
}

// funcLines FuncSnap 行数（nil 安全）。
func funcLines(fs *FuncSnap) int {
	if fs == nil {
		return 0
	}
	return fs.Lines
}
