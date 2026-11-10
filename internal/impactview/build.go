// build.go —— 影响面管线编排：L0 文件 → L1 import 反向闭包 → L2 符号调用方
// → L3 测试面 → P2 共变，逐层独立降级（失败记 Notes 继续上层），输出确定化
// 快照。Build 是唯一入口，runner 接线只做类型搬运。

package impactview

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/semsearch"

	"github.com/oagentkit/textx/codeintel"
)

// Build 输入（通用原语，不认识 plugin 类型；接线在 runner）。
type Input struct {
	Dir          string              // 工作副本根（空 = 无工作区，仅 L0 降级面）
	ChangedFiles []string            // 纳入审查的文件（仓库相对路径，/ 分隔）
	HunkRanges   map[string][][2]int // 文件 → 改动行区间（L2 触达符号依据）
	DiffText     string              // 清洗后 diff 文本（签名变更近似判定）
	Index        *codeintel.Index    // 工作副本符号索引（nil = 跳过 L2；runner 复用 codegraph 已建索引）
	Graph        *semsearch.Index    // 消歧符号图（v6.0.0，nil = 走既有 codeintel 词法路径；runner 复用 codegraph 已建图）
	MaxCommits   int                 // P2 共变扫描深度（0 = DefaultCommits；负 = 关闭）
	BaseRef      string              // 函数级 diff 的 base 参照（如 origin/main；空 = 跳过该层）
	InsecureTLS  bool                // funcdiff deepen fetch 的 TLS 跳过校验（与平台克隆同环境）
	Timeout      time.Duration       // 构建总预算（0 = 30s；超时逐层降级不阻断）
}

// Build 构建影响面快照（永不返回 error——影响面是增强产物，失败面由 Notes
// 与降级快照表达）。
func Build(ctx context.Context, in Input) *Snapshot {
	start := time.Now()
	if in.Timeout <= 0 {
		in.Timeout = 30 * time.Second
	}
	deadline := start.Add(in.Timeout)
	s := &Snapshot{
		Version:     SnapshotVersion,
		GeneratedAt: start.UTC(),
		Changed:     dedupSorted(in.ChangedFiles),
		Notes:       []string{},
	}
	if len(s.Changed) == 0 {
		s.Notes = append(s.Notes, "无纳入审查的变更文件，影响面为空")
		return s
	}

	// ---- L0 文件层：变更节点（恒可用） ----
	for _, f := range s.Changed {
		s.Nodes = append(s.Nodes, Node{
			ID: "file:" + f, Kind: KindFile, Label: shortLabel(f), Changed: true, Detail: f,
		})
	}

	// ---- L1 包层 + L3 测试面：工作副本 import 图 ----
	var ws *goMods
	if in.Dir != "" {
		var trunc bool
		ws, trunc = scanWorkspace(in.Dir)
		for _, n := range ws.fileCount {
			s.Stats.GoFiles += n
		}
		for _, n := range ws.testFiles {
			s.Stats.GoFiles += n
		}
		s.Stats.Packages = len(ws.dirs)
		if ws.module == "" {
			s.Notes = append(s.Notes, "未识别 go.mod（非 Go 仓库或子目录布局），包级影响面降级为文件层")
		}
		if trunc {
			s.Stats.Truncated = true
			s.Notes = append(s.Notes, "工作副本 Go 文件超扫描上限，import 图可能不完整")
		}
	} else {
		s.Notes = append(s.Notes, "无工作副本，包级/符号级影响面降级为文件层")
	}
	affected := buildAffectedPackages(s, ws, s.Changed)

	// ---- L2 符号层：触达符号 → 精化调用位（v6.0.0 图优先、逐符号回退） ----
	syms := collectSymbols(in.Index, in.Graph, in.HunkRanges)
	s.Stats.Symbols = len(syms)
	callTotal := appendSymbolNodes(s, syms)
	noteCallerSource(s, syms, in.Graph)

	// ---- P2 共变：git log 历史共现（预算内才做） ----
	commits := in.MaxCommits
	if commits == 0 {
		commits = DefaultCommits
	}
	if commits > 0 && in.Dir != "" && ctx.Err() == nil && time.Now().Before(deadline) {
		couples, n, errNote := collectCoupling(ctx, in.Dir, s.Changed, minInt(commits, MaxCommits), in.Timeout)
		if errNote != "" {
			s.Notes = append(s.Notes, errNote)
		}
		s.Coupling = couples
		s.Stats.Commits = n
		appendCouplingNodes(s, couples)
	} else if commits > 0 && in.Dir == "" {
		s.Notes = append(s.Notes, "无工作副本，历史共变扫描跳过")
	}

	// ---- v3.185.0 函数/方法级图 diff：base 切面对比（预算内才做） ----
	// v5.6.4 起 shell/py 走词法版（funcdiff_script.go），与 Go 版共用 FuncDiffs
	// 出口（mermaid/HTML/prompt 三面零改动生效）；Go 在前脚本在后保持稳定序。
	if in.BaseRef != "" && in.Dir != "" && hasGoChanged(s.Changed) && ctx.Err() == nil && time.Now().Before(deadline) {
		fds, fnNotes := diffFunctions(ctx, in.Dir, in.BaseRef, s.Changed, in.HunkRanges, in.InsecureTLS)
		s.FuncDiffs = fds
		s.Notes = append(s.Notes, fnNotes...)
		s.Stats.FuncDiffs = len(fds)
	} else if in.BaseRef == "" && in.Dir != "" && hasGoChanged(s.Changed) {
		s.Notes = append(s.Notes, "未提供 base 参照，函数级 diff 跳过")
	}
	if in.BaseRef != "" && in.Dir != "" && hasScriptChanged(s.Changed) && ctx.Err() == nil && time.Now().Before(deadline) {
		sfds, snNotes := diffScriptFunctions(ctx, in.Dir, in.BaseRef, s.Changed, in.HunkRanges)
		s.FuncDiffs = append(s.FuncDiffs, sfds...)
		s.Notes = append(s.Notes, snNotes...)
		s.Stats.FuncDiffs = len(s.FuncDiffs)
	}

	// ---- v5.6.0 多语言图层：非 Go 仓词法图（source/函数调用/表血缘；预算内） ----
	// Go 仓不走此层：import 闭包+AST 是更强的真相源，词法图只会添误报。
	scriptAffected, scriptCalls := 0, 0
	if in.Dir != "" && (ws == nil || ws.module == "") && ctx.Err() == nil && time.Now().Before(deadline) {
		scriptAffected, scriptCalls = buildScriptLayer(ctx, in.Dir, s, deadline)
	}

	// ---- 风险分（确定性） ----
	exported := 0
	for _, h := range syms {
		if h.Exported {
			exported++
		}
	}
	strongCoupling := 0
	for _, c := range s.Coupling {
		if c.Count >= MinCoupleCount {
			strongCoupling++
		}
	}
	s.Stats.CallSites = callTotal + scriptCalls
	s.Risk = scoreRisk(riskInput{
		AffectedPackages:  countForeignPackages(affected, s.Changed),
		CallSites:         callTotal + scriptCalls,
		ScriptAffected:    scriptAffected,
		ExportedSymbols:   exported,
		SignatureChanged:  detectSignatureChange(in.DiffText),
		TestsMissing:      testsMissing(affected),
		EntrypointTouched: touchesEntrypoint(s.Changed, syms),
		StrongCoupling:    strongCoupling,
		DepsChanged:       depsChanged(s.Changed),
		TestsOnlyChanged:  testsOnlyChanged(s.Changed),
	})

	finalize(s)
	s.Stats.TookMS = time.Since(start).Milliseconds()
	return s
}

// buildAffectedPackages L1 反向闭包：改动文件 → 改动包 → 谁 import 了它们
// （BFS ≤ MaxImportDepth，封顶 MaxPackages）。节点/边/包聚合一并写入 s，
// 返回包聚合切片（风险分消费）。
func buildAffectedPackages(s *Snapshot, ws *goMods, changed []string) []PkgImpact {
	if ws == nil || ws.module == "" {
		return nil
	}
	changedPkgs := map[string]bool{}
	for _, f := range changed {
		changedPkgs[filepath.ToSlash(filepath.Dir(f))] = true
	}
	// 被依赖目录 → 依赖方目录（importers）
	importers := map[string][]string{}
	for _, d := range ws.dirs {
		for _, imp := range ws.imports[d] {
			importers[imp] = append(importers[imp], d)
		}
	}
	for d := range importers {
		sort.Strings(importers[d])
	}

	// BFS：parent 记来路包（边渲染用），parentChanged 标记来路是改动包
	// （改动包无 pkg: 节点，边改挂其首个改动文件）。
	type qe struct {
		pkg           string
		depth         int
		parent        string
		parentChanged bool
	}
	queue := make([]qe, 0, len(changedPkgs))
	for _, p := range sortedSet(changedPkgs) {
		queue = append(queue, qe{pkg: p, depth: 0})
	}
	type row struct {
		pi            PkgImpact
		parent        string
		parentChanged bool
	}
	seen := map[string]bool{}
	var rows []row
	for len(queue) > 0 && len(rows) < MaxPackages {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur.pkg] {
			continue
		}
		seen[cur.pkg] = true
		rows = append(rows, row{
			pi: PkgImpact{
				Path:   cur.pkg,
				Files:  ws.fileCount[cur.pkg],
				Tests:  ws.testFiles[cur.pkg],
				Depth:  cur.depth,
				Direct: cur.depth == 0,
			},
			parent:        cur.parent,
			parentChanged: cur.parentChanged,
		})
		for _, nx := range sortedStrings(importers[cur.pkg]) {
			if !seen[nx] && cur.depth < MaxImportDepth {
				queue = append(queue, qe{pkg: nx, depth: cur.depth + 1,
					parent: cur.pkg, parentChanged: cur.depth == 0})
			}
		}
	}
	if len(queue) > 0 {
		s.Stats.Truncated = true
		s.Notes = append(s.Notes, "受影响包闭包超上限，深层依赖未展开")
	}
	// 对外暴露面：被受影响闭包之外的包 import（出口风险）
	for i := range rows {
		for _, imp := range importers[rows[i].pi.Path] {
			if !seen[imp] {
				rows[i].pi.Exposed = true
				break
			}
		}
	}
	// 排序展示序：先跳数后路径
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].pi.Depth != rows[j].pi.Depth {
			return rows[i].pi.Depth < rows[j].pi.Depth
		}
		return rows[i].pi.Path < rows[j].pi.Path
	})

	for _, r := range rows {
		s.Packages = append(s.Packages, r.pi)
		if r.pi.Direct {
			continue // 改动包不自建 pkg: 节点（file 节点已表达）
		}
		id := "pkg:" + r.pi.Path
		s.Nodes = append(s.Nodes, Node{
			ID: id, Kind: KindPackage, Label: pkgLabel(r.pi.Path),
			Detail: "依赖跳数 " + strconv.Itoa(r.pi.Depth),
		})
		from := "pkg:" + r.parent
		if r.parentChanged {
			// 改动包无 pkg: 节点，边改挂其首个改动文件（ID 带 file: 前缀）
			from = "file:" + firstChangedFileIn(r.parent, changed)
		}
		if from != "" {
			s.Edges = append(s.Edges, Edge{From: from, To: id, Rel: RelImport})
		}
	}
	// L3 测试面：受影响包的测试节点（包级聚合，不逐文件展开）
	for _, p := range s.Packages {
		if p.Tests == 0 {
			continue
		}
		id := "test:" + p.Path
		s.Nodes = append(s.Nodes, Node{
			ID: id, Kind: KindTest, Label: "tests " + pkgLabel(p.Path),
			Detail: strconv.Itoa(p.Tests) + " 个测试文件",
		})
		src := "pkg:" + p.Path
		if p.Direct {
			src = "file:" + firstChangedFileIn(p.Path, changed)
		}
		if src != "" {
			s.Edges = append(s.Edges, Edge{From: src, To: id, Rel: RelTestOf})
		}
	}
	return s.Packages
}

// pkgLabel 包节点展示标签（module 根目录 "." 不可读，收敛为显式标记）。
func pkgLabel(path string) string {
	if path == "." {
		return "(module 根)"
	}
	return path
}

// firstChangedFileIn 目录内首个改动文件（边端点兜底；无命中返回空）。
func firstChangedFileIn(pkgDir string, changed []string) string {
	for _, f := range changed {
		if filepath.ToSlash(filepath.Dir(f)) == pkgDir {
			return f
		}
	}
	return ""
}

// appendSymbolNodes L2 节点/边：符号节点 + 调用方文件节点（复用已有 file
// 节点）。L1 降级时调用方文件节点照建——文件层独立成立。返回调用位总数。
func appendSymbolNodes(s *Snapshot, syms []symHits) int {
	total := 0
	for _, h := range syms {
		display := h.Name
		if h.Recv != "" {
			display = h.Recv + "." + h.Name
		}
		symID := "sym:" + h.File + ":" + display
		s.Nodes = append(s.Nodes, Node{
			ID: symID, Kind: KindSymbol, Label: display, Changed: true,
			Detail: h.File + ":" + strconv.Itoa(h.Line),
		})
		for _, cs := range h.Callers {
			total++
			callerID := "file:" + cs.File
			if !s.hasNode(callerID) {
				detail := "调用方"
				if cs.Caller != "" && cs.Caller != "(package init)" {
					detail += "：" + cs.Caller
				}
				s.Nodes = append(s.Nodes, Node{
					ID: callerID, Kind: KindFile, Label: shortLabel(cs.File), Detail: detail,
				})
			}
			s.Edges = append(s.Edges, Edge{From: symID, To: callerID, Rel: RelCaller})
		}
	}
	return total
}

// appendCouplingNodes 共变节点/边：other 文件建节点（复用 file 节点），边挂
// 归属改动文件。
func appendCouplingNodes(s *Snapshot, couples []Couple) {
	for _, c := range couples {
		if c.File == "" {
			continue
		}
		id := "file:" + c.Other
		if !s.hasNode(id) {
			s.Nodes = append(s.Nodes, Node{
				ID: id, Kind: KindFile, Label: shortLabel(c.Other),
				Detail: "历史共变 " + strconv.Itoa(c.Count) + " 次",
			})
		}
		s.Edges = append(s.Edges, Edge{From: "file:" + c.File, To: id, Rel: RelCouple})
	}
}

// 容量守卫常量：artifacts_json 是 TEXT 列（MySQL 64KB 上界），超限在严格
// 模式下直接写失败——MCP/API 面会静默失去快照。节点封顶 + JSON 体积守卫
// 双保险（体积守卫兜住 Changed 清单/Notes 等 node 之外的增长）。
const (
	MaxSnapshotNodes = 150
	MaxSnapshotJSON  = 48 << 10 // 与 ArtifactDiffInput 的 48KB 落库纪律对齐
	MaxChangedList   = 500      // Changed 展示清单封顶（千文件级 PR 单清单即可顶爆体积守卫；L0 节点/风险分在封顶前已消费全量）
)

// finalize 确定化排序 + 重复边去重 + 容量守卫（节点按影响权重封顶 →
// 边随端点裁剪 → JSON 体积守卫）。任何截断记 Notes + Truncated。
func finalize(s *Snapshot) {
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	sort.SliceStable(s.Edges, func(i, j int) bool {
		if s.Edges[i].From != s.Edges[j].From {
			return s.Edges[i].From < s.Edges[j].From
		}
		if s.Edges[i].To != s.Edges[j].To {
			return s.Edges[i].To < s.Edges[j].To
		}
		return s.Edges[i].Rel < s.Edges[j].Rel
	})
	dedup := s.Edges[:0]
	for i, e := range s.Edges {
		if i > 0 && e == s.Edges[i-1] {
			continue
		}
		dedup = append(dedup, e)
	}
	s.Edges = dedup

	// Changed 展示清单封顶（L0 节点/风险分已在封顶前消费全量，此处只影响
	// MCP/HTML 的清单展示）
	if len(s.Changed) > MaxChangedList {
		total := len(s.Changed)
		s.Changed = s.Changed[:MaxChangedList]
		s.Stats.Truncated = true
		s.Notes = append(s.Notes,
			"改动清单超 "+strconv.Itoa(MaxChangedList)+" 文件，展示截断 "+strconv.Itoa(total)+"→"+strconv.Itoa(MaxChangedList))
	}

	// 节点封顶（影响权重序：改动 > 包 > 测试 > 其他），边随端点裁剪——
	// 悬挂边违反快照不变量（TestNoDanglingEdges）
	if len(s.Nodes) > MaxSnapshotNodes {
		total := len(s.Nodes)
		s.Nodes = prioritizeNodes(s.Nodes, MaxSnapshotNodes)
		keep := map[string]bool{}
		for _, n := range s.Nodes {
			keep[n.ID] = true
		}
		edges := s.Edges[:0]
		for _, e := range s.Edges {
			if keep[e.From] && keep[e.To] {
				edges = append(edges, e)
			}
		}
		s.Edges = edges
		s.Stats.Truncated = true
		s.Notes = append(s.Notes,
			"影响面节点超上限，按影响权重截断 "+strconv.Itoa(total)+"→"+strconv.Itoa(MaxSnapshotNodes)+"（边随端点裁剪；完整面见交互视图/get_impact）")
	}

	// JSON 体积守卫：超 48KB 逐批降节点重排（Changed 清单是语义源不裁，
	// 由节点封顶先行兜底大 PR 场景）
	for {
		b, err := json.Marshal(s)
		if err == nil && len(b) <= MaxSnapshotJSON {
			break
		}
		if err != nil {
			break // 序列化失败（不该发生）交给上层可见的原样数据
		}
		drop := len(s.Nodes) / 5
		if drop == 0 {
			break // 只剩不可再裁的节点（ Changed 单节点），交由告警面暴露
		}
		kept := prioritizeNodes(s.Nodes, len(s.Nodes)-drop)
		keep := map[string]bool{}
		for _, n := range kept {
			keep[n.ID] = true
		}
		edges := s.Edges[:0]
		for _, e := range s.Edges {
			if keep[e.From] && keep[e.To] {
				edges = append(edges, e)
			}
		}
		s.Nodes, s.Edges = kept, edges
		s.Stats.Truncated = true
		if !containsNote(s.Notes, "体积守卫") {
			s.Notes = append(s.Notes, "快照 JSON 超 48KB 体积守卫，逐批裁节点至达标（完整面见交互视图/get_impact）")
		}
	}
	sort.Strings(s.Notes)
}

// containsNote Notes 精确子串判重（体积守卫循环内只记一次）。
func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// hasNode 节点存在性（小集合线性扫，够用）。
func (s *Snapshot) hasNode(id string) bool {
	for _, n := range s.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// shortLabel 渲染短标签：basename，歧义时补上级目录。
func shortLabel(p string) string {
	base := filepath.Base(p)
	dir := filepath.ToSlash(filepath.Dir(p))
	if dir == "." || dir == "" {
		return base
	}
	return filepath.Base(dir) + "/" + base
}

// dedupSorted 有序去重。
func dedupSorted(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sortedSet map 键的字典序展开（确定性遍历统一入口）。
func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedStrings 切片字典序副本（确定性遍历统一入口）。
func sortedStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// countForeignPackages 受影响包中「非改动包」的数量（风险分口径）。
func countForeignPackages(pkgs []PkgImpact, changed []string) int {
	changedPkgs := map[string]bool{}
	for _, f := range changed {
		changedPkgs[filepath.ToSlash(filepath.Dir(f))] = true
	}
	n := 0
	for _, p := range pkgs {
		if !changedPkgs[p.Path] {
			n++
		}
	}
	return n
}

// depsChanged 改动是否触及依赖清单（go.mod/go.sum——传递影响超出本仓静态图）。
func depsChanged(changed []string) bool {
	for _, f := range changed {
		if f == "go.mod" || f == "go.sum" {
			return true
		}
	}
	return false
}

// testsOnlyChanged 改动是否全部为测试文件（降分口径：不达产线）。
func testsOnlyChanged(changed []string) bool {
	if len(changed) == 0 {
		return false
	}
	for _, f := range changed {
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
	}
	return true
}

// hasGoChanged 改动中是否含 Go 文件（函数级 diff 的前置条件）。
func hasGoChanged(changed []string) bool {
	for _, f := range changed {
		if strings.HasSuffix(f, ".go") {
			return true
		}
	}
	return false
}
