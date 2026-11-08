// scriptgraph.go —— 非 Go 仓的词法影响图（v5.6.0 多语言图谱引擎首批）。
//
// Go 侧有 go.mod import 闭包与 AST 符号面；shell/SQL 仓过去只剩文件层孤岛
// （deploy-sql 实弹：mermaid 里 4 个红盒之间零条边）。本层用与 codeintel 同
// 纪律的词法启发补三类真边：
//   - source：shell 的 `source x.sh` / `. x.sh` 装载关系（含沿装载链传递）；
//   - caller：shell 函数定义 → 他文件调用位（复用 L2 的 RelCaller 词表）；
//   - lineage：SQL 表写入（CREATE/INSERT/UPDATE/DELETE/ALTER）→ 读方（FROM/JOIN），
//     经 table 枢纽节点两跳表达（改动文件→表→消费方）。
//
// 刻意不引 AST/新依赖：误报由封顶+Notes 表达，价值在"让受影响面可见"而非完备。

package impactview

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 关系词表扩展与节点种类（与 types.go 同一契约面，只加不改）。
const (
	RelSource  = "source"  // shell 装载：改动 → 装载它的文件（含传递）
	RelLineage = "lineage" // SQL 表血缘：改动文件→表（写）、表→消费方（读）
	KindTable  = "table"   // SQL 表枢纽节点
	// v5.7.4 函数级变更一等节点（仅 HTML 渲染层合成,快照与 mermaid 不含）：
	RelFuncOf    = "func_of"  // 归属边：改动文件 → 其函数级变更节点
	KindFuncDiff = "funcdiff" // 函数级变更节点（Detail 前缀=状态词表）
)

// 封顶：非 Go 仓常是巨型脚本仓，扫描与快照两头都要闸。
const (
	MaxScriptFiles     = 3000   // 词法扫描文件上限
	maxScriptReadBytes = 256000 // 单文件读取上限
	MaxScriptTargets   = 400    // 受影响面入快照上限
	MaxDefsPerFile     = 80     // 单 shell 文件入图函数上限
)

type scriptFile struct {
	rel       string
	srcs      []string        // 解析成功的装载目标（shell source / py·js import，相对路径）
	defs      []string        // 定义的函数名（shell fn()/py def；JS 不做函数级）
	tokens    map[string]bool // 词元集（调用检测用）
	writesTbl []string        // SQL 写表（CREATE/INSERT/UPDATE/DELETE/ALTER）
	readsTbl  []string        // SQL 读表（FROM/JOIN，不含自身写表）
}

type scriptGraph struct {
	byRel     map[string]*scriptFile
	sourcedBy map[string][]string // target → 装载它的文件
	callersOf map[string][]string // func name → 调用它的文件（不含定义文件）
	readsBy   map[string][]string // table → 触碰（读/写）它的文件（不含写入方自身）
}

var (
	reShellSrc = regexp.MustCompile(`(?m)^[ \t]*(?:source|\.)[ \t]+([^\n#]+)`)
	reFuncDef  = regexp.MustCompile(`(?m)^[ \t]*(?:function[ \t]+)?([a-zA-Z_][a-zA-Z0-9_]*)[ \t]*\(\s*\)[ \t\r\n]*\{|^[ \t]*function[ \t]+([a-zA-Z_][a-zA-Z0-9_]*)[ \t\r\n]*\{`)
	rePyFunc   = regexp.MustCompile(`(?m)^[ \t]*def[ \t]+([a-zA-Z_][a-zA-Z0-9_]*)[ \t]*\(`)
	// import x.y[, z] / from x.y import …（from 的模块可为相对点前缀）
	rePyImport = regexp.MustCompile(`(?m)^[ \t]*(?:from[ \t]+(\.+[\w.]*|[\w.]+)[ \t]+import|import[ \t]+([\w.]+(?:[ \t]*,[ \t]*[\w.]+)*))`)
	// require("x") / import … from "x" / export … from "x"（CJS+ESM：from 在
	// 路径之后，与 Python 语法相反——首版写反实弹即漏）
	reJSImport = regexp.MustCompile(`(?:require\(\s*["']([^"']+)["']\s*\)|import[^;\n]*?from[ \t]+["']([^"']+)["']|export[^;\n]*?from[ \t]+["']([^"']+)["'])`)
	reToken    = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)
	// Rust：mod 声明（带分号=文件模块；inline `mod x {` 不产生文件不入图）/
	// use 路径（crate::/super::/self::，外部 crate 不入图）/ fn 定义
	reRustMod  = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?mod[ \t]+([a-zA-Z_]\w*)[ \t]*;`)
	reRustUse  = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?use[ \t]+([a-zA-Z_][\w]*::[\w:{}, ]*?)(?:[ \t]+as[ \t]+\w+)?[ \t]*;`)
	reRustFunc = regexp.MustCompile(`(?m)^[ \t]*(?:pub(?:\([\w ]+\))?[ \t]+)?(?:async[ \t]+)?fn[ \t]+([a-zA-Z_]\w*)`)
	// Java/Kotlin：import（通配 .* 不入图；静态导入同解析）
	reJavaImport = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(?:static[ \t]+)?([\w.]+)[ \t]*;`)
	// C/C++：#include "x.h"（双引号=本仓相对路径；<系统头> 不入图）
	reCInclude = regexp.MustCompile(`(?m)^[ \t]*#[ \t]*include[ \t]*"([^"]+)"`)
	// 引号/反引号/方括号起始的表名一律放过（字符类含 ` 不能进反引号原文，改双引号转义）
	reSQLWrite = regexp.MustCompile("(?i)\\b(?:CREATE\\s+TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?|CREATE\\s+(?:OR\\s+REPLACE\\s+)?VIEW|INSERT\\s+INTO|UPDATE|DELETE\\s+FROM|ALTER\\s+TABLE|MERGE\\s+INTO)\\s+[\"`\\[]?([a-zA-Z_][\\w$]*(?:\\.[a-zA-Z_][\\w$]*)?)")
	reSQLRead  = regexp.MustCompile("(?i)\\b(?:FROM|JOIN)\\s+[\"`\\[]?([a-zA-Z_][\\w$]*(?:\\.[a-zA-Z_][\\w$]*)?)")
)

// buildScriptLayer 扫描并回填快照的脚本/血缘层；返回（波及文件边数, 调用位数）。
// 非 Go 仓（go.mod 缺失）时由 Build 调用；预算内执行，永不报错，无边即零副作用。
func buildScriptLayer(ctx context.Context, dir string, s *Snapshot, deadline time.Time) (int, int) {
	sg := scanScripts(dir, deadline)
	if sg == nil || len(sg.byRel) == 0 {
		return 0, 0
	}
	indexReverse(sg)

	nodeSeen := map[string]bool{}
	edgeSeen := map[Edge]bool{}
	for _, n := range s.Nodes {
		nodeSeen[n.ID] = true
	}
	addNode := func(n Node) {
		if !nodeSeen[n.ID] {
			nodeSeen[n.ID] = true
			s.Nodes = append(s.Nodes, n)
		}
	}
	addEdge := func(e Edge) bool {
		if edgeSeen[e] {
			return false
		}
		edgeSeen[e] = true
		s.Edges = append(s.Edges, e)
		return true
	}

	affected, callSites, edges, truncated := 0, 0, 0, false
	for _, c := range s.Changed {
		if ctx.Err() != nil || time.Now().After(deadline) {
			truncated = true
			break
		}
		sf, ok := sg.byRel[c]
		if !ok {
			continue
		}
		cFile := "file:" + c

		// shell 函数 → 调用方（sym 枢纽，与 L2 同形态）
		for _, fn := range sf.defs {
			callers := sg.callersOf[fn]
			if len(callers) == 0 {
				continue
			}
			symID := "sym:" + c + ":" + fn
			addNode(Node{ID: symID, Kind: KindSymbol, Label: fn + "()", Changed: true, Detail: c})
			for i, g := range callers {
				if i >= MaxCallersPerSym {
					truncated = true
					break
				}
				addNode(Node{ID: "file:" + g, Kind: KindFile, Label: shortLabel(g), Detail: g})
				if addEdge(Edge{From: symID, To: "file:" + g, Rel: RelCaller}) {
					callSites++
					edges++
				}
			}
		}

		// source 反向闭包（装载我的文件，以及沿装载链继续上游）
		seen := map[string]bool{c: true}
		queue := []string{c}
		for depth := 0; depth <= MaxImportDepth && len(queue) > 0; depth++ {
			var next []string
			for _, t := range queue {
				for _, up := range sg.sourcedBy[t] {
					if seen[up] {
						continue
					}
					seen[up] = true
					if affected >= MaxScriptTargets {
						truncated = true
						continue
					}
					addNode(Node{ID: "file:" + up, Kind: KindFile, Label: shortLabel(up), Detail: up})
					if addEdge(Edge{From: cFile, To: "file:" + up, Rel: RelSource}) {
						affected++
						edges++
					}
					next = append(next, up)
				}
			}
			queue = next
		}

		// SQL 表血缘：写表 → table 枢纽 → 触碰方
		for _, t := range sf.writesTbl {
			tid := "table:" + t
			addNode(Node{ID: tid, Kind: KindTable, Label: t, Detail: "table"})
			if addEdge(Edge{From: cFile, To: tid, Rel: RelLineage}) {
				edges++
			}
			for i, g := range sg.readsBy[t] {
				if i >= MaxCallersPerSym {
					truncated = true
					break
				}
				addNode(Node{ID: "file:" + g, Kind: KindFile, Label: shortLabel(g), Detail: g})
				if addEdge(Edge{From: tid, To: "file:" + g, Rel: RelLineage}) {
					affected++
					edges++
				}
			}
		}
	}

	if edges == 0 {
		return 0, 0
	}
	note := "词法图补充：shell source / 函数调用 / SQL 表血缘边 " + strconv.Itoa(edges) + " 条"
	if truncated {
		s.Stats.Truncated = true
		note += "（超封顶已截断）"
	}
	s.Notes = append(s.Notes, note)
	return affected, callSites
}

// indexReverse 单遍建反向索引（避免 defs×files 的平方扫描）。
func indexReverse(sg *scriptGraph) {
	sg.sourcedBy = map[string][]string{}
	usersOf := map[string][]string{} // token(仅已知函数名) → 使用文件
	sg.readsBy = map[string][]string{}
	defNames := map[string]bool{}
	tblNames := map[string]bool{}
	for _, sf := range sg.byRel {
		for _, fn := range sf.defs {
			defNames[fn] = true
		}
		for _, t := range sf.writesTbl {
			tblNames[t] = true
		}
		for _, t := range sf.readsTbl {
			tblNames[t] = true
		}
	}
	for f, sf := range sg.byRel {
		for _, t := range sf.srcs {
			if _, ok := sg.byRel[t]; ok && t != f {
				sg.sourcedBy[t] = append(sg.sourcedBy[t], f)
			}
		}
		for tk := range sf.tokens {
			if defNames[tk] {
				usersOf[tk] = append(usersOf[tk], f)
			}
		}
		for _, t := range sf.writesTbl {
			sg.readsBy[t] = append(sg.readsBy[t], f)
		}
		for _, t := range sf.readsTbl {
			sg.readsBy[t] = append(sg.readsBy[t], f)
		}
	}
	sg.callersOf = map[string][]string{}
	for f, sf := range sg.byRel {
		for _, fn := range sf.defs {
			if len(fn) < 3 || fn == "main" {
				continue // 太短的名字误报面失控；main 人人都有
			}
			for _, u := range usersOf[fn] {
				if u == f {
					continue
				}
				sg.callersOf[fn] = append(sg.callersOf[fn], u)
			}
		}
	}
	// readsBy 剔除定义文件自身（写方读自己的表不是"消费方"）
	for t, fs := range sg.readsBy {
		keep := fs[:0]
		writers := map[string]bool{}
		for _, f := range fs {
			if containsStr(sg.byRel[f].writesTbl, t) {
				writers[f] = true
			}
		}
		for _, f := range fs {
			if !writers[f] {
				keep = append(keep, f)
			}
		}
		sg.readsBy[t] = keep
	}
}

// scanScripts 遍历收集 shell/SQL 文件并解析。预算到点即停。
func scanScripts(dir string, deadline time.Time) *scriptGraph {
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".zcode":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".sh", ".bash", ".sql", ".py", ".pyw", ".js", ".mjs", ".cjs", ".ts", ".jsx", ".tsx",
			".rs", ".java", ".kt", ".c", ".h", ".cpp", ".hpp", ".cc", ".cxx":
			if len(paths) >= MaxScriptFiles {
				return nil
			}
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				paths = append(paths, rel)
			}
		}
		return nil
	})
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	fileSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		fileSet[p] = true
	}
	// 段边界后缀索引（v5.6.5）：包路径相对源根而非仓库根（Maven src/main/java、
	// Python src 布局同病）——键=任意段边界起的路径后缀，值=完整 rel（先到先得，
	// paths 已排序保证确定性）。JVM import 与 Python 绝对模块共用。
	suffixIdx := map[string]string{}
	for _, p := range paths {
		segs := strings.Split(p, "/")
		for i := range segs {
			key := strings.Join(segs[i:], "/")
			if _, ok := suffixIdx[key]; !ok {
				suffixIdx[key] = p
			}
		}
	}
	// Rust crate 根：**/src/{lib,main}.rs 所在的 src 目录（use crate:: 解析锚点；
	// workspace 多 crate 各自映射，取最长前缀归属）
	crateSrc := map[string]bool{}
	for _, p := range paths {
		d, b := filepath.Dir(p), filepath.Base(p)
		if (b == "lib.rs" || b == "main.rs") && strings.HasSuffix(d, "/src") {
			crateSrc[d] = true
		}
	}
	sg := &scriptGraph{byRel: map[string]*scriptFile{}}
	for _, rel := range paths {
		if time.Now().After(deadline) {
			break
		}
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || len(b) > maxScriptReadBytes {
			continue
		}
		text := string(b)
		sf := &scriptFile{rel: rel}
		switch ext := filepath.Ext(rel); {
		case ext == ".sh" || ext == ".bash":
			base := filepath.Dir(rel)
			for _, m := range reShellSrc.FindAllStringSubmatch(text, -1) {
				if p := resolveSourcePath(base, m[1]); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
			for _, m := range reFuncDef.FindAllStringSubmatch(text, MaxDefsPerFile) {
				name := m[1]
				if name == "" {
					name = m[2]
				}
				if name != "" {
					sf.defs = appendUnique(sf.defs, name)
				}
			}
			sf.tokens = map[string]bool{}
			for _, tk := range reToken.FindAllString(text, -1) {
				sf.tokens[tk] = true
			}
		case ext == ".py" || ext == ".pyw":
			// import 面（v5.6.3）：相对/绝对模块 → 仓内文件解析（宁缺勿滥，
			// 解析不到（第三方/动态）不入图）；def 进函数级 caller 检测
			for _, m := range rePyImport.FindAllStringSubmatch(text, -1) {
				mods := []string{m[1]}
				if m[2] != "" {
					mods = strings.Split(m[2], ",")
				}
				for _, mod := range mods {
					if p := resolvePyModule(filepath.Dir(rel), strings.TrimSpace(mod), fileSet, suffixIdx); p != "" {
						sf.srcs = appendUnique(sf.srcs, p)
					}
				}
			}
			for _, m := range rePyFunc.FindAllStringSubmatch(text, MaxDefsPerFile) {
				if m[1] != "" {
					sf.defs = appendUnique(sf.defs, m[1])
				}
			}
			sf.tokens = map[string]bool{}
			for _, tk := range reToken.FindAllString(text, -1) {
				sf.tokens[tk] = true
			}
		case ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".ts" || ext == ".jsx" || ext == ".tsx":
			// JS/TS 只做文件级相对 import（裸名=node_modules 域，不入图）；
			// 函数级刻意不做（箭头函数/重导出形态太多，误报面失控）
			for _, m := range reJSImport.FindAllStringSubmatch(text, -1) {
				spec := m[1]
				for _, g := range m[2:] {
					if spec == "" {
						spec = g
					}
				}
				if p := resolveJSRel(rel, spec, fileSet); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
		case ext == ".rs":
			for _, m := range reRustMod.FindAllStringSubmatch(text, -1) {
				if p := resolveRustMod(rel, m[1], fileSet); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
			for _, m := range reRustUse.FindAllStringSubmatch(text, -1) {
				if p := resolveRustUse(rel, m[1], crateSrc, fileSet); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
			for _, m := range reRustFunc.FindAllStringSubmatch(text, MaxDefsPerFile) {
				if m[1] != "" {
					sf.defs = appendUnique(sf.defs, m[1])
				}
			}
			sf.tokens = map[string]bool{}
			for _, tk := range reToken.FindAllString(text, -1) {
				sf.tokens[tk] = true
			}
		case ext == ".java" || ext == ".kt":
			// JVM 系只做文件级跨包 import（方法定义形态太多不做函数级；
			// 同包同目录的隐式耦合不画——会密成毛线团）
			for _, m := range reJavaImport.FindAllStringSubmatch(text, -1) {
				if p := resolveJvmImport(m[1], suffixIdx); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
		case ext == ".c" || ext == ".h" || ext == ".cpp" || ext == ".hpp" || ext == ".cc" || ext == ".cxx":
			for _, m := range reCInclude.FindAllStringSubmatch(text, -1) {
				if p := resolveCInclude(rel, m[1], fileSet); p != "" {
					sf.srcs = appendUnique(sf.srcs, p)
				}
			}
		case ext == ".sql":
			for _, m := range reSQLWrite.FindAllStringSubmatch(text, -1) {
				sf.writesTbl = appendUnique(sf.writesTbl, strings.ToLower(m[1]))
			}
			for _, m := range reSQLRead.FindAllStringSubmatch(text, -1) {
				t := strings.ToLower(m[1])
				if !containsStr(sf.writesTbl, t) {
					sf.readsTbl = appendUnique(sf.readsTbl, t)
				}
			}
		}
		sg.byRel[rel] = sf
	}
	if len(sg.byRel) == 0 {
		return nil
	}
	return sg
}

// resolveSourcePath 归一化 source 行参数：先剥"引号 + 脚本目录惯用前缀"
// （`"$(dirname "$0")/` 族，含空格无法按字段切），再取首个参数剥外层引号，
// 按当前文件目录相对解析。仍残留变量展开或越出仓库根/绝对路径一律放弃
// （宁缺勿滥：错边比无边毒）。
func resolveSourcePath(base, rawLine string) string {
	line := strings.TrimSpace(rawLine)
	for _, frag := range []string{
		`"$(dirname "$0")/`, `$(dirname "$0")/`, `"$(dirname "$0")"`, `$(dirname "$0")`,
		`$(cd "$(dirname "$0")" && pwd)/`, `${SCRIPT_DIR}/`, "${SCRIPT_DIR}/",
		`$SCRIPT_DIR/`, `${BASH_SOURCE%/*}/`,
	} {
		if strings.HasPrefix(line, frag) {
			line = strings.TrimPrefix(strings.TrimPrefix(line, frag), "/")
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	p := strings.Trim(fields[0], `"`)
	p = strings.Trim(p, `'`)
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "$({}") {
		return ""
	}
	clean := filepath.Clean(filepath.Join(base, p))
	// 越过仓库根的相对引用（../../x）不入图：目标在本仓外，无从归属
	if strings.HasPrefix(clean, "..") {
		return ""
	}
	return filepath.ToSlash(clean)
}

// resolvePyModule Python import → 仓内文件。相对点前缀按层级上溯（`.`=当前
// 目录、`..`=父目录）；绝对模块从仓库根解析。仅命中仓内已知文件（x/y.py 或
// x/y/__init__.py）才返回路径——第三方/动态 import 不入图（宁缺勿滥）。
func resolvePyModule(base, mod string, files map[string]bool, suffixIdx map[string]string) string {
	if mod == "" {
		return ""
	}
	rel := mod
	up := 0
	for strings.HasPrefix(rel, ".") {
		rel = rel[1:]
		up++
	}
	p := strings.ReplaceAll(rel, ".", "/")
	var root string
	switch {
	case up > 0:
		// `from . import`（空尾）指包自身目录 __init__；`from .x import` 当前目录
		root = base
		for i := 1; i < up; i++ {
			root = filepath.Dir(root)
		}
		if rel == "" {
			py := pyFileFor(filepath.ToSlash(filepath.Clean(root)), files)
			if py == "" {
				return ""
			}
			// `from . import` 装载的是包 __init__：指向目录自身即够（边语义=装载）
			return py
		}
	default:
		// 绝对模块（无点前缀）：包路径相对源根（src 布局），段边界后缀索引命中
		// 即归属；未命中（第三方/动态）不入图。候选=模块路径本身（包 __init__
		// 形态已在索引键里）+ .py 后缀（模块文件形态）
		if got, ok := suffixIdx[p]; ok {
			return got
		}
		if got, ok := suffixIdx[p+".py"]; ok {
			return got
		}
		return ""
	}
	return pyFileFor(filepath.ToSlash(filepath.Clean(filepath.Join(root, p))), files)
}

func pyFileFor(c string, files map[string]bool) string {
	if strings.HasPrefix(c, "..") {
		return ""
	}
	for _, cand := range []string{c + ".py", filepath.Join(c, "__init__.py")} {
		cand = filepath.ToSlash(filepath.Clean(cand))
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveJSRel JS/TS import spec → 仓内文件。仅相对引用（./ ../）；候选覆盖
// 显式扩展名与 index 文件；裸名（node_modules 域）一律放弃。
func resolveJSRel(relFile, spec string, files map[string]bool) string {
	if spec == "" || (!strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../")) {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), spec)))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	for _, cand := range []string{c, c + ".js", c + ".mjs", c + ".cjs", c + ".ts", c + ".tsx", c + ".jsx",
		filepath.Join(c, "index.js"), filepath.Join(c, "index.mjs"), filepath.Join(c, "index.ts"), filepath.Join(c, "index.tsx")} {
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveRustMod `mod x;` → x.rs 或 x/mod.rs（相对当前文件目录）。
func resolveRustMod(relFile, name string, files map[string]bool) string {
	if name == "" {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), name)))
	for _, cand := range []string{c + ".rs", filepath.Join(c, "mod.rs")} {
		if files[cand] {
			return cand
		}
	}
	return ""
}

// resolveRustUse use 路径 → 仓内文件。crate:: 从归属 crate 的 src 目录解析；
// super::/self:: 相对当前文件；外部 crate 名（首段非关键字）不入图。
// 候选链：全路径与去尾段各试 .rs / mod.rs（尾段可能是类型而非模块）。
func resolveRustUse(relFile, usePath string, crateSrc map[string]bool, files map[string]bool) string {
	segs := strings.Split(usePath, "::")
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	var root string
	rest := segs
	switch segs[0] {
	case "crate":
		root = crateRootFor(relFile, crateSrc)
		rest = segs[1:]
	case "super":
		root = filepath.Dir(filepath.Dir(relFile))
		rest = segs[1:]
	case "self":
		root = filepath.Dir(relFile)
		rest = segs[1:]
	default:
		return "" // 外部 crate（serde::…）或裸 crate 名引用，无从归属
	}
	if len(rest) == 0 || root == "" {
		return ""
	}
	tryCands := func(parts []string) string {
		c := filepath.ToSlash(filepath.Clean(filepath.Join(append([]string{root}, parts...)...)))
		if strings.HasPrefix(c, "..") {
			return ""
		}
		for _, cand := range []string{c + ".rs", filepath.Join(c, "mod.rs")} {
			if files[cand] {
				return cand
			}
		}
		return ""
	}
	if got := tryCands(rest); got != "" {
		return got
	}
	if len(rest) > 1 {
		return tryCands(rest[:len(rest)-1])
	}
	return ""
}

// crateRootFor 归属 crate 的 src 目录（最长前缀匹配；无命中退化当前目录）。
func crateRootFor(relFile string, crateSrc map[string]bool) string {
	d := filepath.Dir(relFile)
	best := ""
	for src := range crateSrc {
		if d == src || strings.HasPrefix(d, src+"/") {
			if len(src) > len(best) {
				best = src
			}
		}
	}
	if best != "" {
		return best
	}
	return d
}

// resolveJvmImport Java/Kotlin import（com.foo.Bar）→ com/foo/Bar.java|.kt。
// 通配 .* 不入图。
func resolveJvmImport(imp string, suffixIdx map[string]string) string {
	if imp == "" || strings.HasSuffix(imp, ".*") {
		return ""
	}
	c := filepath.ToSlash(strings.ReplaceAll(imp, ".", "/"))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	if got, ok := suffixIdx[c+".java"]; ok {
		return got
	}
	if got, ok := suffixIdx[c+".kt"]; ok {
		return got
	}
	return ""
}

// resolveCInclude #include "x.h" → 相对当前文件目录解析（引号形态=本仓头，
// 含 gcc 惯用的 ./ ../ 前缀）；越出仓库根不入图。
func resolveCInclude(relFile, inc string, files map[string]bool) string {
	if inc == "" || strings.HasPrefix(inc, "/") {
		return ""
	}
	c := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(relFile), inc)))
	if strings.HasPrefix(c, "..") {
		return ""
	}
	if files[c] {
		return c
	}
	return ""
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func appendUnique(xs []string, x string) []string {
	if containsStr(xs, x) {
		return xs
	}
	return append(xs, x)
}
