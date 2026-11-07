// repomap.go —— 仓库级代码地图（v5.6.2，图谱可视化 D 项）：整仓目录依赖图。
//
// 与 PR 影响面（Build）共用 Snapshot 契约与渲染面（HTML/JSON 同构），差异：
// Changed 为空、不做风险评分、粒度收敛到目录。Go 目录=包（import 聚合）；
// shell source 与 SQL 表血缘跨目录聚合。封顶+Notes 纪律与 Build 一致，
// 巨型仓输出"够看的地形图"而非完备数据集。

package impactview

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 封顶：整仓图按"地形图"定位，超限截断+注明，不做无限展开。
const (
	MaxMapDirs  = 300  // 目录节点上限
	MaxMapEdges = 1500 // 目录依赖边上限
)

// BuildRepoMap 扫描工作副本产出整仓目录依赖图。永不报错：失败面由 Notes
// 与空图表达（与 Build 同纪律）。
func BuildRepoMap(dir string, timeout time.Duration) *Snapshot {
	start := time.Now()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := start.Add(timeout)
	s := &Snapshot{
		Version:     SnapshotVersion,
		GeneratedAt: start.UTC(),
		Changed:     []string{},
		Risk: Risk{Level: RiskLow, Score: 0,
			Reasons: []string{"仓库全图（目录级）不做风险评分——评分语义属于变更影响面"}},
		Notes: []string{},
	}
	truncated := false

	edgeCount := map[Edge]int{}
	addDir := func(d string) {
		if _, ok := dirNode(s, d); !ok && len(s.Nodes) < MaxMapDirs {
			label := d
			if label == "." {
				label = "(根)"
			}
			s.Nodes = append(s.Nodes, Node{ID: "pkg:" + d, Kind: KindPackage, Label: label, Detail: d})
		}
	}
	addEdge := func(rel, from, to string) bool {
		if from == to {
			return false
		}
		e := Edge{From: "pkg:" + from, To: "pkg:" + to, Rel: rel}
		if edgeCount[e] == 0 {
			if len(edgeCount) >= MaxMapEdges {
				truncated = true
				return false
			}
			s.Edges = append(s.Edges, e)
		}
		edgeCount[e]++
		return true
	}

	// ---- Go import 图（目录=包） ----
	if ws, trunc := scanWorkspace(dir); ws != nil {
		if trunc {
			s.Stats.Truncated = true
			s.Notes = append(s.Notes, "Go 文件超扫描上限，import 图可能不完整")
		}
		if ws.module == "" {
			s.Notes = append(s.Notes, "未识别 go.mod，Go 包图缺失（非 Go 仓属正常）")
		}
		for fromDir, imports := range ws.imports {
			for _, imp := range imports {
				if addEdge(RelImport, fromDir, imp) {
					addDir(fromDir)
					addDir(imp)
				}
			}
		}
	}

	// ---- shell source（文件对→目录对聚合） ----
	sg := scanScripts(dir, deadline)
	if sg != nil {
		indexReverse(sg) // sourcedBy/readsBy 反向索引由共享的 indexReverse 单源产出
		for target, files := range sg.sourcedBy {
			td := relDir(target)
			for _, f := range files {
				if addEdge(RelSource, relDir(f), td) {
					addDir(relDir(f))
					addDir(td)
				}
			}
		}
		// ---- SQL 表血缘（写方目录→读方目录聚合，多表合并为一条目录对边） ----
		for t, readers := range sg.readsBy {
			for _, w := range sg.byRel {
				if !containsStr(w.writesTbl, t) {
					continue
				}
				for _, r := range readers {
					if addEdge(RelLineage, relDir(w.rel), relDir(r)) {
						addDir(relDir(w.rel))
						addDir(relDir(r))
					}
				}
			}
		}
	}

	if len(s.Nodes) == 0 {
		s.Notes = append(s.Notes, "未扫描到 Go/shell/SQL 依赖，地图为空（纯文档/纯 yaml 仓？）")
		return s
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID < s.Nodes[j].ID })
	sort.Slice(s.Edges, func(i, j int) bool {
		if s.Edges[i].From != s.Edges[j].From {
			return s.Edges[i].From < s.Edges[j].From
		}
		return s.Edges[i].To < s.Edges[j].To
	})
	s.Notes = append(s.Notes, "仓库全图（目录级）：节点 "+strconv.Itoa(len(s.Nodes))+
		"、依赖边 "+strconv.Itoa(len(s.Edges))+"（import/source/血缘聚合）")
	if truncated {
		s.Stats.Truncated = true
		s.Notes = append(s.Notes, "目录/边超封顶已截断（MaxMapDirs/MaxMapEdges）")
	}
	s.Stats.TookMS = time.Since(start).Milliseconds()
	return s
}

// dirNode 查目录节点是否已入图（O(n) 直查：节点≤300，无需索引）。
func dirNode(s *Snapshot, d string) (Node, bool) {
	id := "pkg:" + d
	for _, n := range s.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// relDir 文件相对路径 → 目录（根文件为 "."）。
func relDir(rel string) string {
	d := filepath.Dir(filepath.ToSlash(rel))
	if d == "/" || d == "" {
		return "."
	}
	return strings.TrimSuffix(d, "/")
}
