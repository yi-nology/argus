// Package langcap —— 语言能力矩阵单一事实源（v6.6.0）。
//
// 「哪个能力覆盖哪些语言（哪些扩展名、什么精度档）」的程序面唯一出处：
// 词法扫描三面（图层 / funcdiff 词法面 / semsearch chunk 面）的参与扩展名集
// 由本包声明，impactview 与 semsearch 直接消费；工具描述口径（R3 工具、
// MCP get_impact/get_repo_map/search_sem）经 LangsOf 派生；README
// 「支持语言矩阵」节与本表同源。
//
// 族（Family）与能力面（*Exts）分层声明：能力面可由族并集派生的（图层/
// funcdiff）走派生保持真单源；semsearch chunk 面是独立历史面（.pyw/.mjs/.cjs
// 等不在索引内），刻意不并集——同一语言在不同能力的参与面不同，收敛的是
// 「声明处」，不是覆盖面本身。语言族内部的解析器（正则 / go/parser）留在各
// 消费包，本包只管覆盖面与口径。
package langcap

import "strings"

// 语言族扩展名（词法面的最小声明单元）。
var (
	GoExts    = []string{".go"}
	ShellExts = []string{".sh", ".bash"}
	PyExts    = []string{".py", ".pyw"}
	JSTSExts  = []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx"}
	RustExts  = []string{".rs"}
	JVMExts   = []string{".java", ".kt"}
	CExts     = []string{".c", ".h", ".cpp", ".hpp", ".cc", ".cxx"}
	SQLExts   = []string{".sql"}
)

// 词法扫描扩展名三面（能力面单一事实源）。
var (
	// GraphScanExts 图层（impactview scriptgraph）参与扫描的扩展名：
	// shell source / fn 调用、py·js·ts import、rust mod/use、jvm import、
	// c-family include、SQL 表血缘；Go 走 go.mod import 闭包与 AST（不在此列）。
	GraphScanExts = concat(ShellExts, SQLExts, PyExts, JSTSExts, RustExts, JVMExts, CExts)

	// FuncDiffExts 词法函数级 diff 参与面（impactview funcdiff_script）：
	// shell fn / py def / rust fn / js·ts 函数与类方法（v6.6.0 补齐，保守定义
	// 形态见 funcdiff_script）。Go 走 go/parser AST 版（funcdiff.go），不在此列。
	FuncDiffExts = concat(ShellExts, PyExts, RustExts, JSTSExts)

	// SemChunkExts semsearch chunk 索引参与面：Go=声明级 chunk+调用边；其余=
	// 文件级摘要 chunk（代码为主 + 仓库文档——.md 常载规范与架构语义）。
	// go.mod 不在集内（只供 modulePathOf 读 module 声明，不进索引）。
	// 独立历史面：.pyw/.mjs/.cjs/.bash/.cxx 等不在索引内，扩面前先评估 chunk 预算。
	SemChunkExts = []string{
		".go", ".md", ".js", ".ts", ".jsx", ".tsx",
		".py", ".java", ".rs", ".c", ".cc", ".cpp",
		".h", ".hpp", ".rb", ".php", ".cs", ".kt",
		".swift", ".scala", ".sh", ".sql", ".yaml",
		".yml", ".toml", ".proto", ".vue",
	}
)

// PolySymbolExts get_poly_symbol 词法函数表参与面（textx codeintel 上游
// scanPolyFiles：.py/.js/.ts/.tsx/.jsx）。上游面只声明不消费——此处与
// 上游保持一致是文档口径责任，上游扩表后同步本集。
var PolySymbolExts = []string{".py", ".js", ".ts", ".tsx", ".jsx"}

// PreciseEdgeLangs ARGUS_GRAPH_PRECISE 类型解析级边的覆盖语言（go/types
// 唯一路径，见 v5.10.0 真调用边翻案）。
var PreciseEdgeLangs = []string{"Go"}

// extLang 扩展名 → 语言展示名（描述口径派生用）。
var extLang = map[string]string{
	".go": "Go", ".py": "Python", ".pyw": "Python",
	".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript",
	".sh": "shell", ".bash": "shell", ".sql": "SQL", ".rs": "Rust",
	".java": "Java", ".kt": "Kotlin",
	".c": "C/C++", ".h": "C/C++", ".cpp": "C/C++", ".hpp": "C/C++", ".cc": "C/C++", ".cxx": "C/C++",
	".rb": "Ruby", ".php": "PHP", ".cs": "C#", ".swift": "Swift", ".scala": "Scala",
	".vue": "Vue", ".proto": "protobuf",
	".md": "Markdown", ".yaml": "YAML", ".yml": "YAML", ".toml": "TOML",
}

// HasExt ext（含点）是否在集内。
func HasExt(set []string, ext string) bool {
	for _, e := range set {
		if e == ext {
			return true
		}
	}
	return false
}

// JoinLangs 集内扩展名对应语言展示名的斜杠拼接（工具描述口径派生）。
func JoinLangs(set []string) string { return strings.Join(LangsOf(set), "/") }

// LangsOf 集内扩展名对应的去重语言展示名（保持集内首见顺序，确定性输出）。
// 未知扩展名按原样透出（新扩展名入集未登记展示名时描述面不静默丢）。
func LangsOf(set []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(set))
	for _, e := range set {
		name, ok := extLang[e]
		if !ok {
			name = e
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// concat 族并集（保序去重）。
func concat(sets ...[]string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, set := range sets {
		for _, e := range set {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}
