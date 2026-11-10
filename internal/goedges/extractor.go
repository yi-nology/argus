// Package goedges go/types 精确调用边抽取器（v5.10.0 档二）：实现
// semsearch.EdgeAugmenter，经 golang.org/x/tools/go/packages 全量类型解析
// 产出全仓 caller→callee 调用边，替换 semsearch 图面的词法邻接。接口在
// internal/semsearch（档一钩子），本包是装配侧 opt-in 实现——
// ARGUS_GRAPH_PRECISE 开启时经 bootstrap 以 semsearch.WithEdges 注入。
//
// 抽取口径：pattern "./..." 装载主模块，带 Body 的 FuncDecl 视为 caller；
// body 内 CallExpr 经 typeutil.Callee（types 级解析，比 Uses 手查稳）定位
// 被调对象；两端键一律 semsearch.NodeKey 契约（dir.Recv.Base）——caller
// dir=caller 包 PkgPath 剥 module 前缀（根目录空串），callee dir=被调
// types.Func 所在包同法剥前缀；方法两端取接收者基类型名（指针/泛型实例化
// 剥到 Named 基名），函数取空串。
//
// 边界（如实声明）：
//   - 接口方法调用解析到接口方法声明（types 级事实），而接口方法不是 chunk
//     符号节点——semsearch 侧存在性过滤丢弃，接口分派不可见（具体实现间
//     的边不建，设计权衡：宁缺勿假边）；
//   - 函数字面量内的调用归外层 FuncDecl；包级函数字面量（var x = func(){…}）
//     无外层声明，其调用不产出边；
//   - 测试文件（_test.go）不在 "./..." 装载面（Tests=false），测试专属
//     调用边不可见；
//   - stdlib/外部依赖包（PkgPath 剥不出 module 前缀）剪掉，不建边——图面
//     只表达仓库内符号（与词法边同款纪律）。
//
// 失败语义（静默失败哲学——semsearch 侧已有失败闩，per Index 降级词法边，
// 本包不做重试与日志）：go.mod 缺失/无 module 声明、Load 失败（工作副本
// 不可编译、依赖不可解析——需要 vendor/ 或内部 GOPROXY）、任一仓内包带
// Errors、Go 文件总数超 MaxFiles（DefaultMaxFiles=3000，防超大仓一次全量
// 类型解析拖垮 Refresh 时延），一律返回 error 由调用方降级。
package goedges

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"

	"argus/internal/semsearch"
)

// DefaultMaxFiles Go 源文件总数上限（./... 装载面内的非测试文件）：全量
// go/types 解析的成本与文件数线性相关，超大仓一次解析秒级起步——超限视为
// 成本不可承受，返回 error 走词法降级而非拖垮 Refresh（与 semsearch 图面
// 容量封顶同款哲学：宁可降级不可失控）。
const DefaultMaxFiles = 3000

// Extractor 精确调用边抽取器（零外部状态：无缓存无配置无日志，每次 Edges
// 全量解析；semsearch 侧已有签名去重与失败闩，本结构保持纯净可复用）。
type Extractor struct {
	// MaxFiles Go 源文件总数上限（超限返回 error）。零/负值按
	// DefaultMaxFiles 处理（测试注入小值验证超限路径）。
	MaxFiles int
}

// New 构建缺省上限的抽取器（bootstrap 注入用）。
func New() *Extractor {
	return &Extractor{MaxFiles: DefaultMaxFiles}
}

// 接口契约钉死：本包唯一存在理由是实现 semsearch.EdgeAugmenter。
var _ semsearch.EdgeAugmenter = (*Extractor)(nil)

// limit 生效上限（零值规范）。
func (x *Extractor) limit() int {
	if x.MaxFiles > 0 {
		return x.MaxFiles
	}
	return DefaultMaxFiles
}

// Edges 返回全仓精确调用边（EdgeAugmenter 实现）：caller→callee 有序对，
// 端点键=semsearch.NodeKey 契约格式。失败语义见包文档。
func (x *Extractor) Edges(ctx context.Context, dir string) ([][2]string, error) {
	mod, err := modulePath(dir)
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Mode:    packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedModule,
		Dir:     dir,
		Context: ctx,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("goedges: load %s: %w", dir, err)
	}
	// 仓内包过滤：PkgPath 剥 module 前缀成功才算仓内（stdlib/外部剪掉）；
	// 任一仓内包带 Errors 即整体失败——部分解析的边集是不完整的精确事实，
	// 宁可降级词法也不交半成品。
	type inPkg struct {
		pkg *packages.Package
		dir string
	}
	var inmod []inPkg
	files := 0
	for _, p := range pkgs {
		d, ok := inModule(p.PkgPath, mod)
		if !ok {
			continue
		}
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("goedges: 包 %s 带错误: %v", p.PkgPath, p.Errors[0])
		}
		inmod = append(inmod, inPkg{p, d})
		files += len(p.Syntax)
	}
	if len(inmod) == 0 {
		return nil, fmt.Errorf("goedges: %s 无仓内 Go 包", dir)
	}
	if files > x.limit() {
		return nil, fmt.Errorf("goedges: Go 文件 %d 超上限 %d（%s）", files, x.limit(), dir)
	}
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, ip := range inmod {
		for _, f := range ip.pkg.Syntax {
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue // 非 FuncDecl / 无体外声明（含外部链接）不产边
				}
				caller := semsearch.NodeKey(ip.dir, recvBaseDecl(fd.Recv), fd.Name.Name)
				// ast.Inspect 覆盖嵌套函数字面量——字面量内调用归外层
				// FuncDecl（边界见包文档）。
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					edge, ok := calleeEdge(ip.pkg.TypesInfo, call, caller, mod)
					if !ok || seen[edge] {
						return true
					}
					seen[edge] = true
					out = append(out, edge)
					return true
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out, nil
}

// calleeEdge 单个 CallExpr → 精确边（types 级解析）：typeutil.Callee 定位
// 被调对象（方法调用含接口分派解析——解到接口方法声明即如实产键，由消费侧
// 存在性过滤丢弃）；非 *types.Func（内建/类型转换/函数值变量调用）与
// stdlib/外部被调剪掉不建边。
func calleeEdge(info *types.Info, call *ast.CallExpr, caller, mod string) ([2]string, bool) {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok {
		return [2]string{}, false
	}
	p := fn.Pkg()
	if p == nil {
		return [2]string{}, false // 万能作用域对象（理论上仅内建），防御
	}
	cdir, ok := inModule(p.Path(), mod)
	if !ok {
		return [2]string{}, false // stdlib/外部依赖剪掉
	}
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = recvBaseType(sig.Recv().Type())
	}
	return [2]string{caller, semsearch.NodeKey(cdir, recv, fn.Name())}, true
}

// inModule PkgPath → 仓库相对目录（剥 module 前缀；根包返回空串）。非本
// module 路径（stdlib/外部依赖）返回 false——调用边剪掉的判据。
func inModule(pkgPath, mod string) (string, bool) {
	if pkgPath == mod {
		return "", true
	}
	if rest, ok := strings.CutPrefix(pkgPath, mod+"/"); ok {
		return rest, true
	}
	return "", false
}

// modulePath 读 dir/go.mod 的 module 声明行（go.mod 是文本协议，字段扫描
// 足够——不引 golang.org/x/mod，保持本包除 x/tools 外零依赖）。缺失或无
// module 行返回 error（装配侧降级词法边）。
func modulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("goedges: 读 go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i] // 剥行尾注释（module 值不含 "//"，安全）
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("goedges: %s 无 module 声明", filepath.Join(dir, "go.mod"))
}

// recvBaseDecl 声明侧接收者基类型名：*T / (T) / T[_] / (*T[_]) 等形态剥
// 到 T；无接收者返回空串。AST 侧仅认 Ident 基名——极端形态（点选表达式的
// 非法接收者）返回空串，与词法抽取同款容错。
func recvBaseDecl(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	t := fl.List[0].Type
loop:
	for {
		switch tt := t.(type) {
		case *ast.StarExpr:
			t = tt.X
		case *ast.ParenExpr:
			t = tt.X
		case *ast.IndexExpr:
			t = tt.X // 泛型接收者 List[T]
		case *ast.IndexListExpr:
			t = tt.X // 多类型参数 List[K, V]
		default:
			break loop
		}
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// recvBaseType 类型侧接收者基类型名（callee 端）：*T / 别名 / 泛型实例化
// 剥到 Named 声明名；接口方法（无接收者）不进入本函数——调用方已判空。
func recvBaseType(t types.Type) string {
loop:
	for {
		switch tt := t.(type) {
		case *types.Pointer:
			t = tt.Elem()
		default:
			break loop
		}
	}
	if named, ok := types.Unalias(t).(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}
