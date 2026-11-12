// funcdiff_script.go —— 词法面函数级 diff（v5.6.4 起步 shell/py，v5.6.5 rust，
// v6.6.0 补 js/ts）：复用 FuncDiff 契约与三出口渲染（mermaid 图 diff/HTML 双列
// 卡片/prompt 行），数据面走词法定义提取；参与扩展名单源 langcap.FuncDiffExts。
//
// 与 Go 版（funcdiff.go AST 对齐）的刻意差异：
//   - Calls 出边集合留空——词法面语言的"调用关系"是词法级，函数体内的引用
//     需要全仓 def 索引才不误报，边际价值撑不起复杂度；调用位故事由
//     scriptgraph 的 caller 边承载，这里只做"函数增删/签名与体量变化"。
//   - 对齐键=函数名（词法面语言无 Go 式重载；TS 重载签名无函数体不入表）；
//     签名=定义行原文（截断）。
//   - touched 语义与 Go 同：函数定义行不在本轮 hunk 区间内的不报（防
//     大重排文件刷屏）。
//   - js/ts 取保守定义形态集（function/箭头赋值/类方法），多行参数表与
//     复杂装饰器形态刻意不做——图层 v5.6.3「误报面失控」判定的同款宁缺勿滥。

package impactview

import (
	"cmp"
	"context"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/oagentkit/runtime/procx"

	"github.com/yi-nology/argus/internal/langcap"
)

// scriptFuncDef 词法函数定义（起始行/签名头原文/体量近似行数）。
type scriptFuncDef struct {
	name     string
	line     int
	sig      string
	body     int
	bodyHash string // 函数体文本指纹（行数不变体变的感知，v5.6.4）
}

// scriptExts 脚本函数级 diff 的参与扩展名（langcap.FuncDiffExts 单源）。
func scriptExts(f string) bool {
	return langcap.HasExt(langcap.FuncDiffExts, strings.ToLower(filepath.Ext(f)))
}

// hasScriptChanged 改动清单是否含词法面函数级 diff 参与文件。
func hasScriptChanged(changed []string) bool {
	for _, f := range changed {
		if scriptExts(f) {
			return true
		}
	}
	return false
}

// diffScriptFunctions 对 changed 的词法面文件做函数级词法对比。
// ranges 语义与 Go 版一致（hunk 行区间）。永不报错：逐文件降级记 Note。
func diffScriptFunctions(ctx context.Context, dir, baseRef string, changed []string,
	ranges map[string][][2]int) ([]FuncDiff, []string) {
	var notes []string
	files := make([]string, 0, len(changed))
	for _, f := range changed {
		if scriptExts(f) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	if len(files) == 0 {
		return nil, notes
	}
	if len(files) > MaxFuncDiffFiles {
		notes = append(notes, "改动脚本文件超函数级对比上限（"+strconv.Itoa(MaxFuncDiffFiles)+"），仅对比前序部分")
		files = files[:MaxFuncDiffFiles]
	}
	var out []FuncDiff
	for _, f := range files {
		afterDefs, afterOK := diskScriptFuncs(dir, f)
		beforeDefs, beforeOK := gitScriptFuncs(ctx, dir, baseRef, f)
		if !afterOK {
			// 磁盘无此文件 = 已删除：before 全量 removed（与 Go 版同口径）
			for _, d := range beforeDefs {
				out = append(out, FuncDiff{File: f, Name: d.name, Status: FuncRemoved,
					Before: &FuncSnap{Signature: d.sig, Lines: d.body, BodyHash: d.bodyHash}})
			}
			continue
		}
		if !beforeOK {
			notes = append(notes, f+":base 版本不可达（按新文件处理，函数全量新增口径）")
		}
		beforeIdx := map[string]scriptFuncDef{}
		for _, d := range beforeDefs {
			beforeIdx[d.name] = d
		}
		for _, d := range afterDefs {
			prev, existed := beforeIdx[d.name]
			if !existed {
				if !lineTouched(d.line, ranges[f]) {
					continue // 未触达的新函数不收编（与 Go 版同防噪）
				}
				out = append(out, FuncDiff{File: f, Name: d.name, Status: FuncAdded,
					After: &FuncSnap{Signature: d.sig, Lines: d.body, BodyHash: d.bodyHash}})
				continue
			}
			if !lineTouched(d.line, ranges[f]) {
				continue
			}
			if prev.sig == d.sig && prev.body == d.body && prev.bodyHash == d.bodyHash {
				continue // 触达但定义面无变化（注释/空行级）
			}
			out = append(out, FuncDiff{File: f, Name: d.name, Status: FuncModified,
				SigSame: prev.sig == d.sig,
				Before:  &FuncSnap{Signature: prev.sig, Lines: prev.body, BodyHash: prev.bodyHash},
				After:   &FuncSnap{Signature: d.sig, Lines: d.body, BodyHash: d.bodyHash}})
		}
		if beforeOK {
			afterIdx := map[string]bool{}
			for _, d := range afterDefs {
				afterIdx[d.name] = true
			}
			for _, d := range beforeDefs {
				if !afterIdx[d.name] {
					out = append(out, FuncDiff{File: f, Name: d.name, Status: FuncRemoved,
						Before: &FuncSnap{Signature: d.sig, Lines: d.body, BodyHash: d.bodyHash}})
				}
			}
		}
	}
	if len(out) > MaxFuncDiffs {
		out = out[:MaxFuncDiffs]
		notes = append(notes, "函数级 diff 超 "+strconv.Itoa(MaxFuncDiffs)+" 条已截断")
	}
	return out, notes
}

// diskScriptFuncs 工作副本侧定义提取；文件不存在 ok=false（删除口径）。
func diskScriptFuncs(dir, rel string) ([]scriptFuncDef, bool) {
	b, err := os.ReadFile(dir + string(os.PathSeparator) + relJoin(rel))
	if err != nil {
		return nil, false
	}
	return scriptFuncDefs(string(b), extOf(rel)), true
}

// gitScriptFuncs base 侧定义提取；取不到（新文件/深历史缺失）ok=false。
func gitScriptFuncs(ctx context.Context, dir, ref, rel string) ([]scriptFuncDef, bool) {
	req := procx.RunRequest{
		Argv:    []string{"git", "-C", dir, "show", ref + ":" + rel},
		Timeout: gitShowTimeout,
	}
	out, _, code, err := procx.Run(ctx, req)
	if err != nil || code != 0 || len(out) == 0 || len(out) > maxFuncFileBytes {
		return nil, false
	}
	return scriptFuncDefs(string(out), extOf(rel)), true
}

// reJSFunc js/ts 词法函数定义（v6.6.0 保守形态集，三组=三种形态的函数名）：
// 1. function 声明（含 export/async/generator 前缀）；2. const/let/var 箭头或
// function 赋值（参数表单行）；3. 类方法（缩进≥1，含访问性/static/async/泛型/
// 返回类型标注；get/set 访问器，语句关键字名由 jsDefKeyword 滤除）。
// 多行参数表/复杂装饰器形态刻意不做（宁缺勿滥：错函数比漏函数毒）。
var reJSFunc = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+(?:default[ \t]+)?)?(?:async[ \t]+)?function[ \t]*\*?[ \t]*([A-Za-z_$][\w$]*)` +
	`|^[ \t]*(?:export[ \t]+)?(?:const|let|var)[ \t]+([A-Za-z_$][\w$]*)[ \t]*=[ \t]*(?:async[ \t]+)?(?:function\b|\([^)\n]*\)[ \t]*=>|[A-Za-z_$][\w$]*[ \t]*=>)` +
	`|^[ \t]+(?:public[ \t]+|private[ \t]+|protected[ \t]+|static[ \t]+|readonly[ \t]+|abstract[ \t]+|override[ \t]+|async[ \t]+|\*[ \t]*)*(?:get[ \t]+|set[ \t]+)?([A-Za-z_$][\w$]*)[ \t]*(?:<[^>\n]*>)?[ \t]*\([^)\n]*\)[ \t]*(?::[^={]*)?\{[ \t]*$`)

// jsDefKeyword 类方法形态（reJSFunc 分支 3）的语句关键字黑名单。
var jsDefKeyword = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "else": true, "do": true, "try": true, "function": true,
	"new": true, "typeof": true, "await": true, "case": true, "delete": true,
	"void": true, "throw": true, "break": true, "continue": true, "in": true,
	"of": true, "instanceof": true, "finally": true, "with": true,
}

// scriptFuncDefs 定义提取：shell（reFuncDef）/py（rePyFunc）/rust（reRustFunc）/
// js·ts（reJSFunc），按起始行序。
// body=到下一顶层定义的行数近似（函数体量变化感知够用）。
func scriptFuncDefs(text, ext string) []scriptFuncDef {
	type hit struct {
		name, sig string
		line      int
		off       int
	}
	var hits []hit
	total := strings.Count(text, "\n") + 1
	appendHit := func(name string, off int) {
		line := strings.Count(text[:off], "\n") + 1
		sig := defLineAt(text, off)
		hits = append(hits, hit{name: name, sig: truncateRunes(sig, 200), line: line, off: off})
	}
	if ext == ".py" || ext == ".pyw" {
		for _, loc := range rePyFunc.FindAllStringSubmatchIndex(text, MaxDefsPerFile) {
			if loc[2] < 0 {
				continue
			}
			appendHit(text[loc[2]:loc[3]], loc[0])
		}
	} else if ext == ".js" || ext == ".ts" {
		for _, loc := range reJSFunc.FindAllStringSubmatchIndex(text, MaxDefsPerFile) {
			name := ""
			for _, g := range []int{2, 4, 6} {
				if loc[g] >= 0 {
					name = text[loc[g]:loc[g+1]]
					break
				}
			}
			if name == "" || jsDefKeyword[name] {
				continue
			}
			appendHit(name, loc[0])
		}
	} else if ext == ".rs" {
		// rust fn（含 pub/async 形态）；trait impl 与普通 fn 同词法口径
		for _, loc := range reRustFunc.FindAllStringSubmatchIndex(text, MaxDefsPerFile) {
			if loc[2] < 0 {
				continue
			}
			appendHit(text[loc[2]:loc[3]], loc[0])
		}
	} else {
		for _, loc := range reFuncDef.FindAllStringSubmatchIndex(text, MaxDefsPerFile) {
			name := ""
			for _, g := range []int{2, 4} {
				if loc[g] >= 0 {
					name = text[loc[g]:loc[g+1]]
					break
				}
			}
			if name == "" {
				continue
			}
			appendHit(name, loc[0])
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(a.line, b.line) })
	defs := make([]scriptFuncDef, len(hits))
	for i, h := range hits {
		body := 1
		endOff := len(text)
		if i+1 < len(hits) {
			body = hits[i+1].line - h.line
			endOff = hits[i+1].off
		} else {
			body = total - h.line + 1
		}
		defs[i] = scriptFuncDef{name: h.name, line: h.line, sig: h.sig, body: body,
			bodyHash: bodyFingerprint(text[h.off:endOff])}
	}
	return defs
}

// bodyFingerprint 函数体文本指纹（FNV-1a 32 位 hex 截 8——只做"变没变"的
// 等价判定，不做内容还原；空白不归一：缩进变化算变化，与 diff 行口径一致）。
func bodyFingerprint(body string) string {
	h := fnv.New32a()
	h.Write([]byte(body))
	return strconv.FormatUint(uint64(h.Sum32()), 16)
}

// defLineAt 偏移所在行原文（行首到行尾，去空白尾）。
func defLineAt(text string, off int) string {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += off
	}
	if end <= start {
		return ""
	}
	return strings.TrimRight(text[start:end], " \t\r")
}

// lineTouched 定义行是否落在本轮 hunk 行区间内（与 Go 侧 ranges 语义一致）。
func lineTouched(line int, rs [][2]int) bool {
	for _, r := range rs {
		if line >= r[0] && line <= r[1] {
			return true
		}
	}
	return false
}

// extOf 小写扩展名归族（.sh/.bash/.py/.pyw/.rs/.js/.ts；js 族与 ts 族共用
// reJSFunc，按族归一）。未知后缀落 .sh（调用面已由 scriptExts 闸过，仅防御）。
func extOf(rel string) string {
	rel = strings.ToLower(rel)
	switch {
	case strings.HasSuffix(rel, ".bash"):
		return ".bash"
	case strings.HasSuffix(rel, ".pyw"):
		return ".pyw"
	case strings.HasSuffix(rel, ".py"):
		return ".py"
	case strings.HasSuffix(rel, ".rs"):
		return ".rs"
	case strings.HasSuffix(rel, ".ts") || strings.HasSuffix(rel, ".tsx"):
		return ".ts"
	case strings.HasSuffix(rel, ".js") || strings.HasSuffix(rel, ".mjs") ||
		strings.HasSuffix(rel, ".cjs") || strings.HasSuffix(rel, ".jsx"):
		return ".js"
	default:
		return ".sh"
	}
}

// filepath rel 路径安全拼接（防 rel 以 / 开头越出 dir——changed 来源可信，
// 防御性保持与 scanScripts 同水位）。
func relJoin(rel string) string {
	return strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
}
