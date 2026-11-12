// symbols.go —— L2 符号层：变更触达符号 → 调用方。v3.186.0 收编 textx
// v0.7.0 的 codeintel.PreciseCallers（token 候选 + AST 精化，本包曾自实现等价
// 精化层，上游合并 #15 后下沉删除）：调用位补封闭函数名（Recv.Name / Name，
// 包级初始化记 "(package init)"）与限定名（pkg.F / T.M / F）。
//
// v6.0.0 调用方升级：图优先、逐符号回退——消歧符号图（semsearch.Index，
// NodeKey 契约键 dir.Recv.Name / dir.Name）能解析的触达符号改取图邻接调用方
// （import 限定消歧后，跨目录同名误报自此消除）；符号不在图内（预算截断/非
// Go 等场景）逐符号回退 codeintel.PreciseCallers 既有路径，不整层废弃。边语义
// 零变化：CallerRef 形态不变，appendSymbolNodes 无感。
//
// 边界（上游同款声明）：不做 SSA 类型推断，接口分派/动态派发不可见，词法级
// 邻接仍按边抽取规则解析（qualified 同目录精确、var 限定降裸名回退）。

package impactview

import (
	"cmp"
	"go/ast"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yi-nology/argus/internal/semsearch"

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
	viaGraph bool                  // v6.0.0：调用方来自消歧符号图（false = 回退 codeintel 词法路径）
}

// collectSymbols 从 codeintel 索引取变更触达符号（hunk 行区间 ∩ 符号定义），
// 调用方解析图优先（v6.0.0）：gx 能以 NodeKey 契约键解析该符号即取图邻接
// （in 已封顶 graphCallersCap，与 MaxCallersPerSym 同量级）；ok=false（符号
// 不在图内）该符号回退 PreciseCallers（token 候选 + AST 精化，v0.7.0 上游
// 单点）。idx 为触达符号唯一来源（nil = 整层跳过）；gx nil = 全走既有词法
// 路径。失败/超限降级，不阻断。
func collectSymbols(idx *codeintel.Index, gx *semsearch.Index, ranges map[string][][2]int) []symHits {
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
	slices.SortFunc(toucheds, func(a, b touched) int {
		if c := strings.Compare(a.file, b.file); c != 0 {
			return c
		}
		return cmp.Compare(a.line, b.line)
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
		if gx != nil {
			if _, in, _, _, ok := gx.SymbolContext(semsearch.NodeKey(dirOfKey(t.file), t.recv, t.name)); ok {
				h.viaGraph = true
				for _, r := range in {
					if r.Path == "" {
						continue // loc 缺失（预算截断挤出的边残留）：无文件位不成调用方
					}
					h.Callers = append(h.Callers, codeintel.CallerRef{
						File:   r.Path,
						Line:   r.Line,
						Caller: displayOf(r.Path, r.Symbol),
					})
				}
				out = append(out, h)
				continue
			}
		}
		h.Callers = idx.PreciseCallers(t.name, MaxCallersPerSym)
		out = append(out, h)
	}
	return out
}

// dirOfKey 仓库相对文件路径 → NodeKey 的 dir 段（filepath.Dir 形态；根目录
// "." → 空串）。与 semsearch 包内 dirOf 同款语义（未导出，键契约单源仍在
// semsearch.NodeKey）。
func dirOfKey(rel string) string {
	if d := filepath.Dir(rel); d != "." {
		return d
	}
	return ""
}

// displayOf 图符号键 → 调用方展示名：剥 dir 前缀，对齐 codeintel Caller 的
// 无目录形态（Recv.Name / Name）。根目录键无前缀原样返回（如 Main）；子目录
// 键剥整段 dir（含 dir 内的 "/"，如 internal/app.Store.GetUser → Store.GetUser
// ——dir 与 Recv 段同以 "." 分隔，无法从键串自身可靠切分，故以调用方定义文件
// 反推 dir 前缀精确剥离）。
func displayOf(path, symbol string) string {
	if dir := dirOfKey(path); dir != "" && strings.HasPrefix(symbol, dir+".") {
		return symbol[len(dir)+1:]
	}
	return symbol
}

// noteCallerSource v6.0.0 调用方来源可观测面：Stats.CallerSource 档位 + Notes
// 注明（混合/部分回退记图与回退条数；全图路径按 Precise 标注边档位）。零触达
// 符号不记（字段 omitempty，既有快照零漂移）。
func noteCallerSource(s *Snapshot, syms []symHits, gx *semsearch.Index) {
	graphN, fallbackN := 0, 0
	for _, h := range syms {
		if h.viaGraph {
			graphN++
		} else {
			fallbackN++
		}
	}
	if graphN+fallbackN == 0 {
		return
	}
	precise := gx != nil && gx.Precise()
	s.Stats.CallerSource = classifyCallerSource(graphN, fallbackN, precise)
	switch {
	case graphN > 0 && fallbackN > 0:
		s.Notes = append(s.Notes, strconv.Itoa(graphN)+" 个触达符号经图解析，"+strconv.Itoa(fallbackN)+" 个回退词法")
	case fallbackN == 0:
		note := "L2 调用方来自消歧符号图（词法级）"
		if precise {
			note = "L2 调用方来自消歧符号图（类型解析级）"
		}
		s.Notes = append(s.Notes, note)
	}
}

// classifyCallerSource 调用方来源档位（Stats.CallerSource 词表见 types.go）。
func classifyCallerSource(graphN, fallbackN int, precise bool) string {
	switch {
	case fallbackN == 0 && graphN > 0 && precise:
		return CallerSrcGraphPrecise
	case fallbackN == 0 && graphN > 0:
		return CallerSrcGraphLexical
	case graphN == 0 && fallbackN > 0:
		return CallerSrcCodeintel
	default:
		return CallerSrcMixed
	}
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
