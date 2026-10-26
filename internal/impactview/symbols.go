// symbols.go —— L2 符号层：变更触达符号 → 调用方。v3.186.0 收编 textx
// v0.7.0 的 codeintel.PreciseCallers（token 候选 + AST 精化，本包曾自实现等价
// 精化层，上游合并 #15 后下沉删除）：调用位补封闭函数名（Recv.Name / Name，
// 包级初始化记 "(package init)"）与限定名（pkg.F / T.M / F）。
//
// 边界（上游同款声明）：不做 SSA 类型推断，接口分派/动态派发不可见，方法调用
// 按裸名匹配可能混入同名异接收者的调用——全程序精确调用图留 textx 后续演进。
package impactview

import (
	"go/ast"
	"sort"

	"github.com/oagentkit/textx/codeintel"
)

// symHits 单个触达符号的影响线索（Build 中间产物）。
type symHits struct {
	Name     string // 展示名（Recv.Name 或 Name）
	Recv     string
	File     string
	Line     int
	Exported bool
	Callers  []codeintel.CallerRef // 上游 PreciseCallers 精化后的调用位
}

// collectSymbols 从 codeintel 索引取变更触达符号（hunk 行区间 ∩ 符号定义），
// 再经 PreciseCallers 取精化调用位（token 候选 + AST 精化，v0.7.0 上游单点）。
// 失败/超限降级，不阻断。
func collectSymbols(idx *codeintel.Index, ranges map[string][][2]int) []symHits {
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
		h.Callers = idx.PreciseCallers(t.name, MaxCallersPerSym)
		out = append(out, h)
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
