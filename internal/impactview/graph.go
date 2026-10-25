// graph.go —— L1 包依赖图与 L3 测试面：go/parser ImportsOnly 扫工作副本建
// 目录级 import 图，反向闭包推导受影响包。目录=包的映射经 go.mod module 路径
// 前缀换算（import path 剥前缀即仓库相对目录；/vN 后缀剥离）。无 go.mod 的
// 仓库 L1 整层降级（记 Notes，不阻断）。
package impactview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// goMods 仓库扫描产物：目录 → 包信息（import 图与测试面共用一次扫描）。
type goMods struct {
	module    string              // go.mod module 路径（空 = 未识别）
	dirs      []string            // 含 Go 文件的目录（相对路径，有序）
	imports   map[string][]string // 目录 → 该包 import 的仓库内目录（去重有序）
	testFiles map[string]int      // 目录 → *_test.go 数
	fileCount map[string]int      // 目录 → 非 test Go 文件数
}

// scanWorkspace 扫描 dir 下全部 Go 文件（ImportsOnly，只读 import 区）。
// 上限 MaxImportFiles（触发即返回已扫部分 + false，由调用方记 Notes）。
func scanWorkspace(dir string) (*goMods, bool) {
	m := &goMods{
		imports:   map[string][]string{},
		testFiles: map[string]int{},
		fileCount: map[string]int{},
	}
	m.module = readModulePath(filepath.Join(dir, "go.mod"))

	fset := token.NewFileSet()
	count := 0
	truncated := false
	// 目录序遍历保证确定性；单文件解析失败跳过（与 codeintel 同纪律）
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "vendor" || name == "node_modules" || name == ".zcode" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || count >= MaxImportFiles {
			if count >= MaxImportFiles {
				truncated = true
			}
			return nil
		}
		count++
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		pkgDir := filepath.ToSlash(filepath.Dir(rel))
		isTest := strings.HasSuffix(rel, "_test.go")
		if isTest {
			m.testFiles[pkgDir]++
		} else {
			m.fileCount[pkgDir]++
		}
		af, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		if !isTest || m.fileCount[pkgDir] > 0 { // 纯测试包不算独立依赖节点，import 归并主包
			m.imports[pkgDir] = mergeSortedUnique(m.imports[pkgDir], pkgImports(af.Imports, m.module))
		}
		return nil
	})
	for d := range m.fileCount {
		m.dirs = append(m.dirs, d)
	}
	sort.Strings(m.dirs)
	return m, truncated
}

// readModulePath 读 go.mod 的 module 行（无文件/解析不出返回空）。
func readModulePath(gomod string) string {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// pkgImports 提取一次 import 声明里落在本仓内的目录（module 前缀换算 + /vN 剥离）。
func pkgImports(imports []*ast.ImportSpec, module string) []string {
	if module == "" {
		return nil
	}
	var out []string
	for _, imp := range imports {
		if imp.Path == nil {
			continue
		}
		p := imp.Path.Value
		p = strings.Trim(p, `"`)
		rest, ok := strings.CutPrefix(p, module+"/")
		if !ok || rest == "" {
			continue
		}
		out = append(out, stripVersionSuffix(rest))
	}
	return out
}

// stripVersionSuffix 剥 import path 尾部 /vN（Go modules 主版本后缀）。
func stripVersionSuffix(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		if seg := p[i+1:]; len(seg) > 1 && seg[0] == 'v' {
			if v, err := strconv.Atoi(seg[1:]); err == nil && v >= 2 {
				return p[:i]
			}
		}
	}
	return p
}

// mergeSortedUnique 有序去重合并（小集合，O(n²) 可忽略）。
func mergeSortedUnique(base, add []string) []string {
	for _, a := range add {
		found := false
		for _, b := range base {
			if a == b {
				found = true
				break
			}
		}
		if !found {
			base = append(base, a)
		}
	}
	sort.Strings(base)
	return base
}
