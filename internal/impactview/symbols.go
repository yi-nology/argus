// symbols.go —— L2 符号层：变更触达符号 → 调用方。两级精度：
//  1. codeintel token 级调用位（x.calls：IDENT 后跟 LPAREN——字符串/注释天然
//     排除），全仓覆盖，定位调用方文件与行；
//  2. AST 精化（refineCallSites）：只对命中文件重解析，抓封闭函数名与限定名
//     （pkg.F / T.M）——给出「谁在调用」而非只有「哪里出现」。
//
// 边界（刻意声明）：不做 SSA 类型推断，接口分派/动态派发不可见，方法调用按
// 裸名匹配可能混入同名异接收者的调用——全程序精确调用图留 textx 上游演进；
// 包级初始值/匿名函数里的调用封闭函数名记 "(package init)"。
package impactview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/oagentkit/textx/codeintel"
)

// symHits 单个触达符号的影响线索（Build 中间产物）。
type symHits struct {
	Name     string // 展示名（Recv.Name 或 Name）
	Recv     string
	File     string
	Line     int
	Exported bool
	Callers  []callSite // token 级命中经 AST 精化后的调用位
}

// callSite 精化后的调用位。
type callSite struct {
	File   string
	Line   int
	Caller string // 封闭函数（Recv.Name / Name；包级初始值/匿名函数为 init 语义）
	Callee string // 限定名（pkg.F / T.M / F）
}

// collectSymbols 从 codeintel 索引取变更触达符号（hunk 行区间 ∩ 符号定义），
// 再经 GoCallers（token 级调用位）取候选调用方，最后对候选文件做 AST 精化。
// 任何一级失败/超限降级，不阻断。
func collectSymbols(idx *codeintel.Index, ranges map[string][][2]int, dir string) []symHits {
	if idx == nil || len(ranges) == 0 {
		return nil
	}
	// 触达符号收集：跨文件行区间合并去重，按 (File,Line) 确定化
	type touched struct {
		name, recv, file string
		line             int
	}
	seen := map[string]bool{}
	var toucheds []touched
	for file, rs := range ranges {
		for _, r := range rs {
			for _, s := range idx.TouchedByLines(file, r[0], r[1]) {
				key := s.File + ":" + s.Recv + "." + s.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				toucheds = append(toucheds, touched{name: s.Name, recv: s.Recv, file: s.File, line: s.Line})
			}
		}
	}
	sort.Slice(toucheds, func(i, j int) bool {
		if toucheds[i].file != toucheds[j].file {
			return toucheds[i].file < toucheds[j].file
		}
		return toucheds[i].line < toucheds[j].line
	})
	if len(toucheds) > MaxSymbols {
		toucheds = toucheds[:MaxSymbols]
	}

	var out []symHits
	for _, t := range toucheds {
		h := symHits{
			Name:     t.name,
			Recv:     t.recv,
			File:     t.file,
			Line:     t.line,
			Exported: ast.IsExported(t.name),
		}
		// 候选调用方：token 级调用位（方法按裸名命中所有形态——词法层已知边界）
		raw := idx.GoCallers(t.name, MaxCallersPerSym*4) // 多取候选，精化后截回
		h.Callers = refineCallSites(dir, raw, MaxCallersPerSym)
		out = append(out, h)
	}
	return out
}

// refineCallSites 对候选文件重解析，确认 AST 层「调用位」事实并抓封闭函数名
// 与限定名；返回按 (File,Line) 有序的至多 maxN 个调用位。文件解析失败该文件
// 的候选整体丢弃（token 级结果不再回填——宁缺毋假）。
func refineCallSites(dir string, refs []codeintel.Ref, maxN int) []callSite {
	if len(refs) == 0 {
		return nil
	}
	// 按文件聚合行号，同文件只解析一次
	byFile := map[string][]int{}
	for _, r := range refs {
		if r.Line > 0 {
			byFile[r.File] = append(byFile[r.File], r.Line)
		}
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	var out []callSite
	for _, f := range files {
		abs := filepath.Join(dir, filepath.FromSlash(f))
		af, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		lines := map[int]bool{}
		for _, l := range byFile[f] {
			lines[l] = true
		}
		out = append(out, walkDeclCalls(fset, af, f, lines)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > maxN {
		out = out[:maxN]
	}
	return out
}

// walkDeclCalls 遍历文件顶层声明，收集候选行上的调用位。FuncDecl 的封闭函数
// 为 Recv.Name / Name；其余声明（包级 var/const/init 表达式）记 "(package init)"。
func walkDeclCalls(fset *token.FileSet, af *ast.File, rel string, lines map[int]bool) []callSite {
	var out []callSite
	collect := func(caller string, body ast.Node) {
		ast.Inspect(body, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok || ce.Lparen == token.NoPos {
				return true
			}
			line := fset.Position(ce.Lparen).Line
			if !lines[line] {
				return true
			}
			callee := calleeName(ce.Fun)
			if callee == "" {
				return true
			}
			out = append(out, callSite{File: rel, Line: line, Caller: caller, Callee: callee})
			return true
		})
	}
	for _, decl := range af.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			collect(funcDeclName(fd), fd)
			continue
		}
		collect("(package init)", decl)
	}
	return out
}

// funcDeclName 函数声明的展示名（Recv.Name 或 Name）。
func funcDeclName(fd *ast.FuncDecl) string {
	name := fd.Name.Name
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		if recv := recvTypeName(fd.Recv.List[0].Type); recv != "" {
			name = recv + "." + name
		}
	}
	return name
}

// recvTypeName 接收者类型名（*T / T / pkg.T → T / pkg.T）。
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

// calleeName 调用目标限定名：Ident → F；pkg/T/var 的 SelectorExpr → X.Sel；
// 链式调用（f().g）只取末端 Sel（类型不可知，词法边界）。取不到返回空。
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		sel := f.Sel.Name
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + sel
		}
		return sel
	default:
		return ""
	}
}

// callerLabel 调用方节点标签（"Caller @ file:line" 收敛为短形态）。
func callerLabel(cs callSite) string {
	at := cs.File
	if cs.Caller != "" && cs.Caller != "(package init)" {
		at += " · " + cs.Caller
	}
	return at + ":" + strconv.Itoa(cs.Line)
}
