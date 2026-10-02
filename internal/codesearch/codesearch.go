// Package codesearch 工作副本受控全文检索（L0 纯底座，零 internal 依赖）：
// 供审查 agent 的 search_code 工具在只读工作副本上跑正则搜索——跨文件找
// 调用方/相似实现/字符串与配置线索，覆盖符号索引到不了的词法线索（含非
// Go 文件）。扫描全程确定性、零 LLM，命中数/单文件/输出三重封顶，目录
// 排除集与 codeintel 索引对齐。
package codesearch

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 缺省上限（工具侧不再二次钳制；调整阈值须过评测）。
const (
	DefaultMaxHits       = 40
	DefaultMaxFileBytes  = 512 << 10 // 512KB，超过整文件跳过
	DefaultMaxOutputRuns = 6000
	maxPatternLen        = 256
	hitTextMaxRunes      = 240
	binarySniffLen       = 512 // 二进制嗅探窗口（含 NUL 即判二进制）
)

// skipDirs 排除目录（与 codeintel 索引排除集对齐 + 语言栈缓存目录）。
var skipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "testdata": true,
	"dist": true, "third_party": true, "__pycache__": true,
	".venv": true, "venv": true, "target": true,
}

// ToolDescription search_code 工具描述单源（qa 面两处构造器 + 审查面
// r3SearchTool 共用）。描述会进 prompt 影响模型的工具选择，措辞变更须过
// golden 评测。
const ToolDescription = "在仓库工作副本中全文正则搜索代码（跨文件找调用方/相似实现/字符串与配置线索，含非 Go 文件）。pattern 为 Go 正则；glob 可选过滤文件（无 / 时按文件名匹配如 *.go，含 / 时按仓库相对路径匹配）。命中不等于结论：引用前须用 get_file 核实上下文。"

// Hit 单条命中（File 为仓库相对斜杠路径）。
type Hit struct {
	File string
	Line int
	Text string
}

// Options 扫描上限与过滤（零值取缺省）。
type Options struct {
	MaxHits       int      // 命中条数上限（<=0 取缺省）
	MaxFileBytes  int64    // 单文件扫描上限（<=0 取缺省）
	MaxOutputRuns int      // Render 输出 rune 上限（<=0 取缺省）
	Globs         []string // 文件过滤：不含 '/' 按文件名匹配，含 '/' 按相对路径匹配（path.Match 语义，不支持 **）
	IgnoreCase    bool     // 忽略大小写
}

// Result 检索结果（Search 产出，Render 消费；字段冻结后只读）。
type Result struct {
	Hits      []Hit
	Truncated bool // 因 MaxHits/MaxOutputRuns 提前停止
	Scanned   int  // 实际扫描文件数
	Skipped   int  // 跳过文件数（排除目录/二进制/超限/不可读）

	outputMax int
}

// NormalizeGlobs 工具入参的 glob 归一（可选 glob 串 → Options.Globs；空串/纯
// 空白 = 无过滤返回 nil）。R3/R4 与 QA 的 search_code 工具共用单源（v3.98.0，
// 此前 requirement/qa 各持一份同形 helper）。
func NormalizeGlobs(glob string) []string {
	if s := strings.TrimSpace(glob); s != "" {
		return []string{s}
	}
	return nil
}

// ToolIn/ToolOut search_code 工具 wire 契约（v3.102.0 单源：与 ToolDescription
// 同居本包，R3/R4 与 QA 两面共用——此前两包各持同形结构体，schema 描述存在
// 漂移风险）。执行体留在各消费包（R3 两级暂存 / QA 定长截断的包装语义不同）。
type ToolIn struct {
	Pattern    string `json:"pattern" jsonschema:"description=Go 正则表达式（如 MyFunc\\(|TODO|apiKey）"`
	Glob       string `json:"glob,omitempty" jsonschema:"description=可选文件过滤：无 / 按文件名（如 *.go），含 / 按仓库相对路径"`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"description=忽略大小写"`
}

type ToolOut struct {
	Result string `json:"result"`
}

// scanner Search 的遍历状态（re/上限/结果集），WalkDir 回调挂其上。
type scanner struct {
	root    string
	re      *regexp.Regexp
	globs   []string
	maxHits int
	maxFile int64
	res     *Result
}

// visit WalkDir 回调：目录排除 → glob 过滤 → 大小/二进制跳过 → 逐行匹配。
// 单点不可读（权限/竞态删除）跳过，不中断整体扫描。
func (s *scanner) visit(p string, d fs.DirEntry, err error) error {
	if err != nil {
		return nil
	}
	if d.IsDir() {
		if skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		return nil
	}
	if !d.Type().IsRegular() {
		return nil
	}
	rel, err := filepath.Rel(s.root, p)
	if err != nil {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if len(s.globs) > 0 && !matchGlob(s.globs, rel, d.Name()) {
		return nil
	}
	info, err := d.Info()
	if err != nil || info.Size() > s.maxFile {
		s.res.Skipped++
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		s.res.Skipped++
		return nil
	}
	sniff := binarySniffLen
	if len(data) < sniff {
		sniff = len(data)
	}
	if bytes.IndexByte(data[:sniff], 0) >= 0 {
		s.res.Skipped++
		return nil
	}
	s.res.Scanned++
	for i, ln := range bytes.Split(data, []byte{'\n'}) {
		if !s.re.Match(ln) {
			continue
		}
		s.res.Hits = append(s.res.Hits, Hit{
			File: rel,
			Line: i + 1,
			Text: truncRunes(strings.TrimSpace(string(ln)), hitTextMaxRunes),
		})
		if len(s.res.Hits) >= s.maxHits {
			s.res.Truncated = true
			return filepath.SkipAll
		}
	}
	return nil
}

// Search 在 root 下按正则逐文件扫描（字典序遍历，输出顺序确定）。
// pattern 为空/超长、root 非目录、glob 非法返回错误；ctx 取消中止扫描并
// 上抛 ctx.Err()（工具侧按工具错误降级，不产出半截结果）。
func Search(ctx context.Context, root, pattern string, opts Options) (*Result, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("pattern 为空")
	}
	if len(pattern) > maxPatternLen {
		return nil, fmt.Errorf("pattern 超长（>%d 字节）", maxPatternLen)
	}
	for _, g := range opts.Globs {
		if _, err := path.Match(g, ""); err != nil {
			return nil, fmt.Errorf("glob %q 非法: %w", g, err)
		}
	}
	expr := pattern
	if opts.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("pattern 非法: %w", err)
	}
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("工作副本不可用: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("工作副本根不是目录: %s", root)
	}

	maxHits := opts.MaxHits
	if maxHits <= 0 {
		maxHits = DefaultMaxHits
	}
	maxFile := opts.MaxFileBytes
	if maxFile <= 0 {
		maxFile = DefaultMaxFileBytes
	}
	res := &Result{outputMax: opts.MaxOutputRuns}
	if res.outputMax <= 0 {
		res.outputMax = DefaultMaxOutputRuns
	}

	sc := &scanner{root: root, re: re, globs: opts.Globs, maxHits: maxHits, maxFile: maxFile, res: res}
	if walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sc.visit(p, d, err)
	}); walkErr != nil {
		return nil, walkErr
	}
	return res, nil
}

// Render 渲染为工具输出文本：每行 file:line: text，尾部统计与截断注记。
// 输出超过 Options.MaxOutputRuns 时截断并置 Truncated（只影响渲染层，
// Hits 保留全量供调用方自查）。
func (r *Result) Render() string {
	head := make([]string, 0, len(r.Hits))
	used := 0
	truncatedByOutput := false
	for _, h := range r.Hits {
		line := fmt.Sprintf("%s:%d: %s", h.File, h.Line, h.Text)
		need := len([]rune(line)) + 1
		if used+need > r.outputMax && len(head) > 0 {
			truncatedByOutput = true
			break
		}
		head = append(head, line)
		used += need
	}
	var b strings.Builder
	if len(head) == 0 {
		b.WriteString("无命中")
	} else {
		for _, l := range head {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "（扫描 %d 个文件，跳过 %d 个", r.Scanned, r.Skipped)
	switch {
	case truncatedByOutput:
		fmt.Fprintf(&b, "；输出已达上限，仅展示前 %d 条，请缩小 pattern/glob 再搜）", len(head))
	case r.Truncated:
		fmt.Fprintf(&b, "；命中已达上限 %d 条，可能仍有更多，请缩小 pattern/glob 再搜）", len(head))
	default:
		b.WriteString("）")
	}
	return b.String()
}

// matchGlob 任一 glob 命中即通过：不含 '/' 匹配文件名，含 '/' 匹配相对路径。
func matchGlob(globs []string, rel, base string) bool {
	for _, g := range globs {
		if !strings.Contains(g, "/") {
			if ok, _ := path.Match(g, base); ok {
				return true
			}
			continue
		}
		if ok, _ := path.Match(g, rel); ok {
			return true
		}
	}
	return false
}

func truncRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
