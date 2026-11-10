// funcdiff.go —— 函数/方法级图 diff（v3.185.0）：以 base 切面为参照，
// 对改动文件做 BEFORE/AFTER 两版 AST 的函数级对齐——签名变化、函数增删、
// 出边调用集合的增减（图 diff 的边语义：绿=新增调用、红=断开调用）。
//
// BEFORE 取自 `git show <BaseRef>:<file>`（工作副本浅历史由 gitdeepen 保证
// merge-base 可达；取不到该文件的历史版本 = 新文件，全量 added；磁盘上无
// 此文件 = 已删除，全量 removed）。对齐键 = 函数展示名（Recv.Name / Name）；
// 不做签名结构化解析（参数级 diff 留展示原文），不做重载（Go 无重载）。
// 任何取 base/解析失败逐文件降级记 Note，不阻断影响面主体。
package impactview

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oagentkit/runtime/procx"
	"github.com/yi-nology/argus/internal/gitdeepen"
)

// 函数级变更状态词表（FuncDiff.Status）。
const (
	FuncAdded    = "added"
	FuncRemoved  = "removed"
	FuncModified = "modified"
)

// 函数级 diff 层容量封顶（体积/预算双守卫；触发记 Notes）。
const (
	MaxFuncDiffs     = 20      // 入快照的函数级 diff 条数
	MaxCallsDiff     = 10      // 单函数调用边增/减展示数
	MaxFuncDiffFiles = 40      // 参与对比的改动 Go 文件数
	maxFuncFileBytes = 2 << 20 // 单文件字节上限（与 polyglot 同量级）
	gitShowTimeout   = 10 * time.Second
	// funcDiffDeepenTimeout 浅克隆 base 历史补全的独立上限（探测式 deepen 指数
	// 轮次自带上限，预算只兜时间；与 ocrdeleg 的 defaultUnshallowTimeout 同思路，
	// 取其一半——funcdiff 是影响面增强层，不值得占用 OCR 同级预算）。
	funcDiffDeepenTimeout = 90 * time.Second
)

// FuncSnap 单侧函数快照（签名原文 + 出边调用集合）。
type FuncSnap struct {
	Signature string   `json:"signature"`       // 声明头原文（≤200 runes）
	Calls     []string `json:"calls,omitempty"` // 出边调用限定名（有序去重）
	Lines     int      `json:"lines"`           // 函数体行数（体量变化感知）
	// BodyHash 函数体文本指纹（v5.6.4 脚本版专用：行数不变体变的感知，
	// 如 pass→return 1；Go 版不填——AST 口径下 Calls 增减已覆盖）。
	BodyHash string `json:"body_hash,omitempty"`
}

// FuncDiff 函数级 diff 条目。
type FuncDiff struct {
	File         string    `json:"file"`
	Name         string    `json:"name"`     // Recv.Name / Name
	Status       string    `json:"status"`   // added|removed|modified
	SigSame      bool      `json:"sig_same"` // modified 且签名未变（纯体/调用变化）
	Before       *FuncSnap `json:"before,omitempty"`
	After        *FuncSnap `json:"after,omitempty"`
	CallsAdded   []string  `json:"calls_added,omitempty"`
	CallsRemoved []string  `json:"calls_removed,omitempty"`
}

// diffFunctions 主入口：对 changed 中可处理的 Go 文件做函数级对比。
// ranges 决定「本函数是否被本轮触达」（未被触达的同名函数不报，防噪声）。
// insecureTLS 透传 deepen fetch 的 GIT_SSL_NO_VERIFY（与 workcopy 克隆同环境）。
func diffFunctions(ctx context.Context, dir, baseRef string, changed []string,
	ranges map[string][][2]int, insecureTLS bool) ([]FuncDiff, []string) {
	var notes []string
	if dir == "" || baseRef == "" {
		return nil, append(notes, "无 base 参照（无工作副本或 base 分支未知），函数级 diff 跳过")
	}
	// base 参照预检：ref 不可达时逐文件 git show 全会失败,若不拦会把所有函数
	// 误判成「新文件全量新增」（语义完全错误且刷爆封顶）。浅克隆历史未覆盖时
	// 先做探测式 deepen（v5.4.7：funcdiff 此前注释声称「浅历史由 gitdeepen 保证」
	// 实则 impactview 路径从未消费过——只有 OCR 通道调了 EnsureMergeBase；MR 审查
	// 时 impactview 先于插件执行，历史无人补全 → 大仓必降级）。
	if !refResolves(ctx, dir, baseRef) {
		if b := strings.TrimPrefix(baseRef, "origin/"); b != "" && b != baseRef {
			dctx, dcancel := context.WithTimeout(ctx, funcDiffDeepenTimeout)
			derr := gitdeepen.EnsureMergeBaseTLS(dctx, dir, b, funcDiffDeepenTimeout, insecureTLS)
			dcancel()
			switch {
			case derr == nil && refResolves(ctx, dir, baseRef):
				// 补全成功且 ref 现在可达：继续函数级 diff
			case derr != nil:
				notes = append(notes, "base 参照 "+baseRef+" deepen 补全失败（"+derr.Error()+"），函数级 diff 跳过")
				return nil, notes
			default:
				notes = append(notes, "base 参照 "+baseRef+" 在工作副本不可达（deepen 后仍未覆盖），函数级 diff 跳过")
				return nil, notes
			}
		} else {
			return nil, append(notes, "无 base 参照（无工作副本或 base 分支未知），函数级 diff 跳过")
		}
	}
	files := make([]string, 0, len(changed))
	for _, f := range changed {
		if strings.HasSuffix(f, ".go") {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	if len(files) > MaxFuncDiffFiles {
		notes = append(notes, "改动 Go 文件超函数级对比上限（"+strconv.Itoa(MaxFuncDiffFiles)+"），仅对比前序部分")
		files = files[:MaxFuncDiffFiles]
	}

	fset := token.NewFileSet()
	var out []FuncDiff
	trunc := false
	for _, f := range files {
		after, touched, ok := parseFuncsSide(fset, dir, f, ranges[f])
		if !ok {
			// 磁盘无此文件/解析失败 = 视作已删除：before 全量 removed
			before := gitFileFuncs(ctx, dir, baseRef, f)
			for name, snap := range before {
				out = append(out, FuncDiff{
					File: f, Name: name, Status: FuncRemoved,
					Before: snap, CallsRemoved: capCalls(snap.Calls),
				})
			}
			continue
		}
		before := gitFileFuncs(ctx, dir, baseRef, f)
		if len(before) == 0 {
			notes = append(notes, f+":base 版本不可达（按新文件处理，函数全量新增口径）")
		}
		for _, name := range sortedFuncNames(after) {
			as := after[name]
			bs, existed := before[name]
			switch {
			case !existed:
				if !touched[name] {
					continue // 未触达的新函数不收编，防大新增文件刷屏
				}
				out = append(out, FuncDiff{
					File: f, Name: name, Status: FuncAdded, After: as,
					CallsAdded: capCalls(as.Calls),
				})
			case touched[name]:
				fd := FuncDiff{File: f, Name: name, Status: FuncModified, Before: bs, After: as}
				fd.SigSame = bs.Signature == as.Signature
				fd.CallsAdded = setDiff(as.Calls, bs.Calls)
				fd.CallsRemoved = setDiff(bs.Calls, as.Calls)
				if fd.SigSame && len(fd.CallsAdded) == 0 && len(fd.CallsRemoved) == 0 && bs.Lines == as.Lines {
					continue // 触达但语义面无变化（注释/空行级改动）
				}
				out = append(out, fd)
			}
		}
		for _, name := range sortedFuncNames(before) {
			if _, still := after[name]; !still {
				out = append(out, FuncDiff{
					File: f, Name: name, Status: FuncRemoved,
					Before: before[name], CallsRemoved: capCalls(before[name].Calls),
				})
			}
		}
		if len(out) >= MaxFuncDiffs {
			trunc = true
			break
		}
	}
	// 排序 + 封顶（(File,Name,Status) 确定化）
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Status < out[j].Status
	})
	if len(out) > MaxFuncDiffs {
		out = out[:MaxFuncDiffs]
	}
	if trunc {
		notes = append(notes, "函数级 diff 超上限（"+strconv.Itoa(MaxFuncDiffs)+"），已截断")
	}
	sort.Strings(notes)
	return out, notes
}

// refResolves base 参照可达性预检（git cat-file -e，本地零网络）。
func refResolves(ctx context.Context, dir, ref string) bool {
	req := procx.RunRequest{
		Argv:    []string{"git", "-C", dir, "cat-file", "-e", ref + "^{commit}"},
		Timeout: gitShowTimeout,
	}
	_, _, code, err := procx.Run(ctx, req)
	return err == nil && code == 0
}

// parseFuncsSide 解析工作副本文件 → 函数快照集 + 触达函数集（同一趟 AST，
// 行区间覆盖 FuncDecl 即触达；无 ranges = 全触达降级口径，宁多报不漏报）。
// 文件不可读/完全解析失败返回 !ok（调用方按删除分流）。
func parseFuncsSide(fset *token.FileSet, dir, rel string, rs [][2]int) (
	funcs map[string]*FuncSnap, touched map[string]bool, ok bool) {
	src, err := os.ReadFile(dir + "/" + rel)
	if err != nil || len(src) > maxFuncFileBytes {
		return nil, nil, false
	}
	af, perr := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if perr != nil && af == nil {
		return nil, nil, false
	}
	funcs = map[string]*FuncSnap{}
	touched = map[string]bool{}
	if len(rs) == 0 {
		touched["_all"] = true
	}
	line := func(p token.Pos) int {
		if p == token.NoPos {
			return 0
		}
		return fset.Position(p).Line
	}
	for _, decl := range af.Decls {
		fd, fdok := decl.(*ast.FuncDecl)
		if !fdok || fd.Name == nil {
			continue
		}
		name := funcDeclName(fd)
		start, end := line(fd.Pos()), line(fd.End())
		for _, r := range rs {
			if r[0] <= end && r[1] >= start {
				touched[name] = true
				break
			}
		}
		snap := &FuncSnap{Lines: end - start + 1}
		if fd.Body != nil {
			snap.Signature = truncateRunes(srcSeg(fset, src, fd.Pos(), fd.Body.Lbrace), 200)
		} else {
			snap.Signature = truncateRunes(srcSeg(fset, src, fd.Pos(), fd.End()), 200)
		}
		seen := map[string]bool{}
		ast.Inspect(fd, func(n ast.Node) bool {
			ce, ceok := n.(*ast.CallExpr)
			if !ceok {
				return true
			}
			if c := calleeName(ce.Fun); c != "" && !seen[c] {
				seen[c] = true
				snap.Calls = append(snap.Calls, c)
			}
			return true
		})
		sort.Strings(snap.Calls)
		funcs[name] = snap
	}
	// 全触达口径展开
	if touched["_all"] {
		delete(touched, "_all")
		for name := range funcs {
			touched[name] = true
		}
	}
	return funcs, touched, true
}

// gitFileFuncs `git show <ref>:<file>` 取 base 侧函数快照。取不到返回空表
// （调用方以空表区分「新文件/不可达」与「磁盘侧删除」）。
func gitFileFuncs(ctx context.Context, dir, ref, rel string) map[string]*FuncSnap {
	req := procx.RunRequest{
		Argv:    []string{"git", "-C", dir, "show", ref + ":" + rel},
		Timeout: gitShowTimeout,
	}
	out, _, code, err := procx.Run(ctx, req)
	if err != nil || code != 0 || len(out) == 0 || len(out) > maxFuncFileBytes {
		return map[string]*FuncSnap{}
	}
	fset := token.NewFileSet()
	af, perr := parser.ParseFile(fset, rel, out, parser.SkipObjectResolution)
	if perr != nil && af == nil {
		return map[string]*FuncSnap{}
	}
	funcs := map[string]*FuncSnap{}
	line := func(p token.Pos) int {
		if p == token.NoPos {
			return 0
		}
		return fset.Position(p).Line
	}
	for _, decl := range af.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil {
			continue
		}
		start, end := line(fd.Pos()), line(fd.End())
		snap := &FuncSnap{Lines: end - start + 1}
		if fd.Body != nil {
			snap.Signature = truncateRunes(srcSeg(fset, []byte(out), fd.Pos(), fd.Body.Lbrace), 200)
		} else {
			snap.Signature = truncateRunes(srcSeg(fset, []byte(out), fd.Pos(), fd.End()), 200)
		}
		seen := map[string]bool{}
		ast.Inspect(fd, func(n ast.Node) bool {
			ce, ceok := n.(*ast.CallExpr)
			if !ceok {
				return true
			}
			if c := calleeName(ce.Fun); c != "" && !seen[c] {
				seen[c] = true
				snap.Calls = append(snap.Calls, c)
			}
			return true
		})
		sort.Strings(snap.Calls)
		funcs[funcDeclName(fd)] = snap
	}
	return funcs
}

// srcSeg 声明头原文（pos 区间切片，越界防御）。
func srcSeg(fset *token.FileSet, src []byte, from, to token.Pos) string {
	a, b := fset.Position(from).Offset, fset.Position(to).Offset
	if a < 0 || b < 0 || a >= len(src) || b > len(src) || a >= b {
		return ""
	}
	return string(src[a:b])
}

// sortedFuncNames 确定化遍历序。
func sortedFuncNames(m map[string]*FuncSnap) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// setDiff a - b（保持 a 序）。
func setDiff(a, b []string) []string {
	bs := map[string]bool{}
	for _, x := range b {
		bs[x] = true
	}
	var out []string
	for _, x := range a {
		if !bs[x] {
			out = append(out, x)
		}
	}
	return out
}

// capCalls 调用清单封顶。
func capCalls(in []string) []string {
	if len(in) > MaxCallsDiff {
		return in[:MaxCallsDiff]
	}
	return in
}
