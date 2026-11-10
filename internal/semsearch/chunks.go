// chunks.go —— 工作副本扫描与 chunk 抽取：Go 走 go/parser 按顶层声明切
// （路径+行号+源码片段，doc 注释天然在内），其余文本文件按文件级首段切。
// 排除集与 codesearch 对齐；二进制（512B 窗口含 NUL）跳过。Go 解析顺带抽
// 符号调用边（v5.9.0，FuncDecl 体 CallExpr 赤名抽取，图面见 graph.go）——
// chunk 与边共用同一次 AST 解析，图增强检索零额外解析成本。
package semsearch

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 扫描与抽取上限（成本闸；MaxTotalChunks 是 embedding 调用预算的硬顶——
// 4000 段 × 一次 HTTP 批量调用，冷建索引在秒级、费用一次性的量级）。
const (
	maxDefaultFiles       = 2000 // 与 codegraph maxFiles 缺省同量级
	maxDefaultTotalChunks = 4000
	maxFileChunks         = 64  // 单文件 decl chunk 上限（巨型生成文件止损）
	fileChunkHeadLines    = 80  // 非 Go 文件级 chunk 摘要行数
	sniffLen              = 512 // 二进制嗅探窗口（与 codesearch 同口径）
)

// excludedDirs 目录排除集（与 codesearch 一致；此处是扫描剪枝，非安全边界）。
var excludedDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "testdata": true,
	"dist": true, "third_party": true, "__pycache__": true,
	".venv": true, "venv": true, "target": true,
}

// codeExts 参与索引的扩展名：代码为主 + 仓库文档（.md 常载规范与架构语义，
// 审查场景价值高）。
var codeExts = map[string]bool{
	".go": true, ".md": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".py": true, ".java": true, ".rs": true, ".c": true, ".cc": true, ".cpp": true,
	".h": true, ".hpp": true, ".rb": true, ".php": true, ".cs": true, ".kt": true,
	".swift": true, ".scala": true, ".sh": true, ".sql": true, ".yaml": true,
	".yml": true, ".toml": true, ".proto": true, ".vue": true,
}

// scannedFile 扫描产物：相对路径 + 指纹 + 内容（抽取与指纹判定共用一次读取）。
type scannedFile struct {
	rel     string
	fp      fileFP
	content []byte
}

// scanDir 收集目录下参与索引的文件（排除集剪枝 + 扩展名过滤 + 二进制跳过），
// 按相对路径排序保证遍历与截断的确定性。
func scanDir(dir string) ([]scannedFile, error) {
	var out []scannedFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单点不可读不阻断扫描（codesearch 同宽容度）
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] && path != dir {
				return filepath.SkipAll
			}
			return nil
		}
		// 安全（v3.133.3）：只读常规文件——软链文件会被 os.ReadFile 跟随，
		// 恶意 PR 提交 link -> /proc/self/environ 即把宿主全部密钥读进索引/
		// 工具输出（实测泄漏确认）。与 codesearch 的 IsRegular 守卫同口径。
		if !d.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !codeExts[ext] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil || isBinary(content) {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		out = append(out, scannedFile{
			rel:     rel,
			fp:      fileFP{size: info.Size(), mtime: info.ModTime().UnixNano()},
			content: content,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 插入排序键：扫描序本身已按字典序（WalkDir 词序），显式排序兜底平台差异。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].rel < out[j-1].rel; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// symEdge 原始调用边（Refresh 中间产物）：caller 是 FuncDecl 全名（Recv.Name
// 或 Name，与 chunk.Symbol 同键），callee 是调用位限定名原文（"fmt.Println"
// / "t.M" / "F"）——到仓库符号表的解析与剪枝在 Refresh 收尾统一做（graph.go）。
type symEdge struct {
	caller string
	callee string
}

// extractChunks 单文件 → (chunk 列表, 调用边)。Go 按顶层声明（含 doc 注释）
// 切并顺带抽边，其余按文件级首段（无边）。Go 解析失败降级文件级 chunk（语法
// 坏文件也有路径与文本可检索，且无边）。
func extractChunks(rel string, content []byte) ([]Chunk, []symEdge) {
	if strings.HasSuffix(rel, ".go") {
		if chunks, edges := goDeclChunks(rel, content); len(chunks) > 0 {
			return chunks, edges
		}
	}
	return []Chunk{fileChunk(rel, content)}, nil
}

// goDeclChunks Go 文件按顶层声明切 chunk 并顺带抽调用边（v5.9.0，同一次解析
// 双产出）：文本 = 「rel:起始行\n」+ 从 doc 注释（如有）到声明结束的源码片段
// ——路径、签名、注释一并进 embedding 输入，这是「结构化摘要」路线的落地；
// 边 = FuncDecl 体 CallExpr 赤名抽取——闭包体归外层函数（随其执行），顶层
// var 初始化器里的 FuncLit 归包级 init、不归属函数符号，不抽。
func goDeclChunks(rel string, content []byte) ([]Chunk, []symEdge) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, content, parser.ParseComments)
	if err != nil || f == nil {
		return nil, nil
	}
	src := content
	var chunks []Chunk
	var edges []symEdge
	for _, decl := range f.Decls {
		startPos := decl.Pos()
		symbol := declSymbol(decl)
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Doc != nil {
			startPos = gd.Doc.Pos()
		}
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Doc != nil {
			startPos = fd.Doc.Pos()
		}
		start := fset.Position(startPos)
		end := fset.Position(decl.End())
		if start.Line <= 0 || end.Offset > len(src) || start.Offset > end.Offset {
			continue
		}
		text := string(src[start.Offset:end.Offset])
		chunks = append(chunks, Chunk{
			Path:   rel,
			Line:   start.Line,
			Symbol: symbol,
			Text:   rel + ":" + itoa(start.Line) + "\n" + clipText(text),
		})
	}
	// 调用边抽取（与 chunk 同一次解析的副产品）：FuncDecl 体 CallExpr 赤名。
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		caller := declSymbol(fd)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if q := calleeQual(call.Fun); q != "" {
					edges = append(edges, symEdge{caller: caller, callee: q})
				}
			}
			return true
		})
	}
	return chunks, edges
}

// calleeQual 调用位限定名（赤名抽取，词法级边界与 codeintel/get_callers 同款
// 声明：无类型信息，接口分派/动态派发不可见）：F() → "F"；pkg.F()/x.M() →
// "pkg.F"/"x.M"（包限定与变量限定不可分——解析阶段按末段归一到仓库符号表，
// 见 graph.go）；链式/字面量/类型转换形态不抽。
func calleeQual(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// declSymbol 顶层声明名（FuncDecl 名 / GenDecl 首个 spec 名；匿名结构取类型名）。
func declSymbol(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil {
			return recvTypeName(d) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				return s.Name.Name
			case *ast.ValueSpec:
				if len(s.Names) > 0 {
					return s.Names[0].Name
				}
			}
		}
	}
	return ""
}

// recvTypeName 方法接收者的基类型名（*T / T → T）。
func recvTypeName(fd *ast.FuncDecl) string {
	expr := fd.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// fileChunk 非 Go 文件的文件级 chunk：路径 + 首 fileChunkHeadLines 行。
func fileChunk(rel string, content []byte) Chunk {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) > fileChunkHeadLines {
		lines = lines[:fileChunkHeadLines]
	}
	return Chunk{
		Path: rel,
		Line: 1,
		Text: rel + ":1\n" + clipText(strings.Join(lines, "\n")),
	}
}

// clipText embedding 输入截断（rune 安全；多字节腰斩会产生非法 UTF-8 进请求）。
func clipText(s string) string {
	const maxEmbedTextRunes = 2000
	runes := []rune(s)
	if len(runes) > maxEmbedTextRunes {
		return string(runes[:maxEmbedTextRunes])
	}
	return s
}

// itoa 避 fmt 的热路径小整数转字符串。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// isBinary 二进制嗅探：窗口内含 NUL 即跳过（与 codesearch 同口径）。
func isBinary(b []byte) bool {
	if len(b) > sniffLen {
		b = b[:sniffLen]
	}
	return bytes.IndexByte(b, 0) >= 0
}
