package argusctx

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oagentkit/textx/diffx"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
)

// 噪声路径规则（复用 v1 原型实现，design §4 术语：diffprep）。
// 命中者不进 diff 文本，但保留在 Files 元信息里（模型仍知道该文件被动过）。
var noisePatterns = []string{
	"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock",
	"poetry.lock", "Gemfile.lock", "composer.lock", "Podfile.lock",
	"vendor/", "third_party/", "node_modules/", "dist/", "build/",
	".pb.go", "_pb2.py", ".min.js", ".min.css", ".map", ".snap", ".svg", ".gif", ".png", ".jpg", ".jpeg", ".webp",
}

// SingleFileMaxChars 单文件 diff 字符截断下限（防单文件刷爆预算）。
// 实际生效值随总量缩放：max(下限, 总量/50)——大窗口模型下大文件不提前腰斩。
const SingleFileMaxChars = 8000

// totalTruncNote 总量预算触顶时的行内截断注记（Prepare 与 PrepareIncremental 共用）。
const totalTruncNote = "\n…（总量截断）"

// singleFileLimit 按总量推导单文件上限：总量/50，不低于全局下限。
func singleFileLimit(totalLimit int) int {
	if totalLimit <= 0 {
		return SingleFileMaxChars
	}
	n := totalLimit / 50
	if n < SingleFileMaxChars {
		n = SingleFileMaxChars
	}
	return n
}

// noisePatKind 预分类后的噪声模式类别（init 一次，免每次调用重新分派）。
type noisePatKind byte

const (
	noiseDir    noisePatKind = 'd' // 目录段（词表带尾斜杠）
	noiseSuffix noisePatKind = 's' // 后缀（. 开头 / .go / .py 结尾）
	noiseBase   noisePatKind = 'b' // 精确文件名
)

var noisePre []struct {
	kind noisePatKind
	val  string
}

func init() {
	for _, pat := range noisePatterns {
		switch {
		case strings.HasSuffix(pat, "/"):
			noisePre = append(noisePre, struct {
				kind noisePatKind
				val  string
			}{noiseDir, strings.TrimSuffix(pat, "/")})
		case strings.HasPrefix(pat, ".") || strings.HasSuffix(pat, ".go") || strings.HasSuffix(pat, ".py"):
			noisePre = append(noisePre, struct {
				kind noisePatKind
				val  string
			}{noiseSuffix, pat})
		default:
			noisePre = append(noisePre, struct {
				kind noisePatKind
				val  string
			}{noiseBase, pat})
		}
	}
}

// noise 判定路径是否为噪声。
// 目录模式按路径段精确匹配（深挖修复）：旧实现 strings.Contains 无边界，
// "cargo-build/"、"ui-dist/"、"myvendor/" 下的合法源码会被 "build/"、"dist/"、
// "vendor/" 误伤静默剔除出审查文本。v3.25.1：模式分类 hoist 到 init + 目录段
// 边界扫描免 Split 分配（profile 显示本函数占 Prepare CPU ~23%）。
func noise(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(p)
	for _, np := range noisePre {
		switch np.kind {
		case noiseSuffix:
			if strings.HasSuffix(base, np.val) {
				return true
			}
		case noiseBase:
			if base == np.val {
				return true
			}
		default: // noiseDir：段边界扫描（等价逐段比对，免 Split 分配）
			if segInPath(p, np.val) {
				return true
			}
		}
	}
	return false
}

// segInPath seg 是否作为完整路径段出现（左侧为开头或 '/'，右侧为结尾或 '/'）。
func segInPath(p, seg string) bool {
	for i := 0; i+len(seg) <= len(p); {
		j := strings.Index(p[i:], seg)
		if j < 0 {
			return false
		}
		i += j
		if (i == 0 || p[i-1] == '/') && (i+len(seg) == len(p) || p[i+len(seg)] == '/') {
			return true
		}
		i++
	}
	return false
}

// truncationNoteMaxPaths 截断清单最多列出的文件数（超出只给计数，防清单本身吃预算）。
const truncationNoteMaxPaths = 30

// PrepareOptions 仓库级过滤选项（v3.15.0 P0-2：.reviewagents.yaml review 段）。
// Exclude/Include 为 glob 词表（.gitignore 语义，复用 plugin.GlobMatch）：
// Exclude 命中者排除；Include 非空 = 白名单模式（未命中者排除）。
// 被排除文件保留元信息（RepoExcluded=true），不进 diff 文本、不进截断清单。
type PrepareOptions struct {
	Exclude []string
	Include []string
}

// repoFilter 从变参选项取过滤器（无选项返回 nil = 永不排除）。
func repoFilter(opts []PrepareOptions) func(string) bool {
	for _, o := range opts {
		if len(o.Exclude) > 0 || len(o.Include) > 0 {
			return plugin.RepoSettingsFilter(&plugin.RepoConfig{Loaded: true,
				Review: plugin.ReviewSettings{ExcludePaths: o.Exclude, IncludePaths: o.Include}})
		}
	}
	return nil
}

// appendTruncationNote 截断透出（深挖修复）：Truncated 置位此前全仓零消费，
// 被预算排除的文件对 R2/R3 完全不可见——审查覆盖不完整却表现为完整。
// 在 diff 文本尾部以数据区形式列出被排除文件清单（路径源自 git，包 DataFence
// 与防线 #1 一致；文件名理论上可伪造围栏标记，中和交给 DataFence）。
func appendTruncationNote(dc *plugin.DiffContext) {
	if dc == nil || !dc.Truncated {
		return
	}
	var excluded []string
	for _, f := range dc.Files {
		// 噪声/二进制/仓库级排除文件本就不进文本（按规则排除），不算"因预算被排除"
		if !f.Included && !f.AlreadyReviewed && !f.IsBinary && !f.RepoExcluded && !noise(f.Path) {
			excluded = append(excluded, f.Path)
		}
	}
	if len(excluded) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "以下 %d 个变更文件因长度预算未纳入本次审查（模型不可见，仅列清单），审查结论不覆盖它们：\n", len(excluded))
	for i, p := range excluded {
		if i >= truncationNoteMaxPaths {
			fmt.Fprintf(&b, "…（其余 %d 个略）\n", len(excluded)-truncationNoteMaxPaths)
			break
		}
		b.WriteString("- " + p + "\n")
	}
	dc.Text += "\n" + plugin.DataFence("截断说明（未纳入审查的文件清单）", strings.TrimRight(b.String(), "\n"))
}

// resolveFiles RawDiff 兜底回填后返回文件清单（gitea 后端 ChangedFile.Diff 为空、
// 内容在 RawDiff——BuildRawDiff 拼接；Prepare 与 PrepareIncremental 共用单源）。
func resolveFiles(md *provider.MergeDiff) []*provider.ChangedFile {
	files := md.Files
	if len(files) > 0 && files[0].Diff == "" && strings.TrimSpace(md.RawDiff) != "" {
		files = fillFromRawDiff(files, md.RawDiff)
	}
	return files
}

// diffBuild DiffContext 装配器（Prepare 与 PrepareIncremental 单源，v3.95.0 收敛
// 此前两份逐行复制的装配循环）。管线四阶段：仓库级排除 → 噪声/二进制 →
// 总量预算 → 文本追加（含单文件截断与注记），调用方只保留自己的分支语义。
type diffBuild struct {
	dc        *plugin.DiffContext
	b         strings.Builder
	total     int
	remaining int
	singleMax int
	exclude   func(string) bool
}

func newDiffBuild(totalLimit, capFiles int, opts []PrepareOptions) *diffBuild {
	d := &diffBuild{dc: &plugin.DiffContext{Files: make([]plugin.DiffFile, 0, capFiles)},
		total: totalLimit, remaining: totalLimit, singleMax: singleFileLimit(totalLimit)}
	if totalLimit > 0 {
		d.b.Grow(totalLimit / 2)
	}
	d.exclude = repoFilter(opts)
	return d
}

// entryOf 由 ChangedFile 构建 DiffFile 元信息（路径 NewPath 优先，删除回退 OldPath）。
func entryOf(f *provider.ChangedFile) plugin.DiffFile {
	entry := plugin.DiffFile{
		Path:      f.NewPath,
		Additions: f.Additions,
		Deletions: f.Deletions,
		IsBinary:  f.IsBinary,
		IsNew:     f.IsNew,
		IsDeleted: f.IsDeleted,
		IsRenamed: f.IsRenamed,
	}
	if f.NewPath == "" {
		entry.Path = f.OldPath
	}
	return entry
}

// metaOnly 只留元信息类文件（仓库级排除/二进制/噪声）：入列后返回 true，
// 不进 diff 文本、仓库级排除不进截断清单。
func (d *diffBuild) metaOnly(entry plugin.DiffFile, f *provider.ChangedFile) bool {
	if d.exclude != nil && d.exclude(entry.Path) {
		entry.RepoExcluded = true
		d.dc.Files = append(d.dc.Files, entry)
		return true
	}
	if f.IsBinary || noise(entry.Path) {
		d.dc.Files = append(d.dc.Files, entry)
		return true
	}
	return false
}

// skipByBudget 总量预算耗尽：入列并标记未纳入（模型不可见），置 Truncated。
func (d *diffBuild) skipByBudget(entry plugin.DiffFile) bool {
	if d.total > 0 && d.remaining <= 0 {
		entry.Included = false
		d.dc.Files = append(d.dc.Files, entry)
		d.dc.Truncated = true
		return true
	}
	return false
}

// addText 追加一段 diff 文本：单文件截断 + 总量预算扣减。
// rune 计数只做一遍全量（v3.25.1，profile 显示 RuneCountInString 占 Prepare CPU
// ~25%）：截断发生时 rc 由「上限 + 注记」算术推导，不再重数全文——remaining
// 语义与逐次重数逐位一致（TruncRunes 精确切到 n）。
func (d *diffBuild) addText(entry plugin.DiffFile, f *provider.ChangedFile) {
	text := f.Diff
	rc := utf8.RuneCountInString(text)
	if rc > d.singleMax {
		text, _ = textutil.TruncRunes(text, d.singleMax)
		note := fmt.Sprintf("\n…（%s 截断，原 %d 字符）", entry.Path, len(f.Diff))
		text += note
		rc = d.singleMax + utf8.RuneCountInString(note)
	}
	if d.total > 0 {
		if rc > d.remaining {
			text, _ = textutil.TruncRunes(text, d.remaining)
			text += totalTruncNote
			rc = d.remaining + utf8.RuneCountInString(totalTruncNote)
			d.dc.Truncated = true
		}
		d.remaining -= rc
	}
	if d.b.Len() > 0 {
		d.b.WriteString("\n")
	}
	d.b.WriteString(text)
	entry.Included = true
	d.dc.Files = append(d.dc.Files, entry)
}

// finish 收尾：回填文本 + 截断清单数据区。
func (d *diffBuild) finish() *plugin.DiffContext {
	d.dc.Text = d.b.String()
	appendTruncationNote(d.dc)
	return d.dc
}

// Prepare 从 SDK 的 MergeDiff 构建 plugin.DiffContext：
// 去 lockfile/vendor/二进制噪声 + 单文件与总量截断 + Truncated 标记。
// totalLimit <=0 表示不截断总量（单测用）。opts 为仓库级过滤选项（v3.15.0，
// 变参向后兼容既有调用方）。
func Prepare(md *provider.MergeDiff, totalLimit int, opts ...PrepareOptions) *plugin.DiffContext {
	files := resolveFiles(md)
	d := newDiffBuild(totalLimit, len(files), opts)
	for _, f := range resolveFiles(md) {
		entry := entryOf(f)
		if d.metaOnly(entry, f) || d.skipByBudget(entry) {
			continue
		}
		d.addText(entry, f)
	}
	return d.finish()
}

// minIncrementalChars 增量 diff 的最小有效长度。
// 低于此阈值视为太短（可能是纯空白/上下文行），回退到全量审查。
const minIncrementalChars = 100

// PrepareIncremental 增量 diffprep：对比当前 diff 与上次审查的 diff，只保留新增/变更部分。
// lastDiffText 为空时等价于全量审查（首次审查场景）。
// 返回的 DiffContext 中：
//   - Text: 仅包含有变化的文件 diff
//   - Files: 全部文件（未变文件标记 AlreadyReviewed=true）
//   - NoNewChanges: true 表示 diff 完全一致，无需审查
func PrepareIncremental(md *provider.MergeDiff, lastDiffText string, totalLimit int, opts ...PrepareOptions) *plugin.DiffContext {
	// 无上次审查记录 → 全量审查
	if lastDiffText == "" {
		return Prepare(md, totalLimit, opts...)
	}

	// 解析上次 diff 为 per-file map
	lastByPath := diffx.SplitUnifiedDiff(lastDiffText)

	files := resolveFiles(md)
	d := newDiffBuild(totalLimit, len(files), opts)
	hasChanges := false
	for _, f := range files {
		entry := entryOf(f)
		if d.metaOnly(entry, f) {
			continue
		}
		// 比对：当前文件 diff 与上次完全一致 → 标记已审查，不进文本
		if lastDiff, existed := lastByPath[entry.Path]; existed && f.Diff == lastDiff {
			entry.AlreadyReviewed = true
			d.dc.Files = append(d.dc.Files, entry)
			continue
		}
		// 文件有变化（新增/修改/删除），或上次不存在该文件
		hasChanges = true
		if d.skipByBudget(entry) {
			continue
		}
		d.addText(entry, f)
	}
	dc := d.finish()

	// 增量 diff 太短 → 回退全量审查（避免 LLM 拿到无上下文的碎片）
	if hasChanges && utf8.RuneCountInString(dc.Text) < minIncrementalChars {
		return Prepare(md, totalLimit, opts...)
	}

	if !hasChanges {
		dc.NoNewChanges = true
	}
	return dc
}

// fillFromRawDiff 把拼接的 unified diff 按文件块回填到各 ChangedFile.Diff。
// 路径对齐优先 NewPath（含 +++ b/<path> 行），删除文件回退 OldPath；未匹配的
// 块忽略。v3.79.0 解析下沉 diffx：SplitChunks 保序切块（同路径块后者覆盖的
// 确定性语义保持），DiffPath 提路径；存储值与 ChangedFile.Diff 一致不带
// "diff --git " 前缀，故切块后剥前缀。
func fillFromRawDiff(files []*provider.ChangedFile, raw string) []*provider.ChangedFile {
	if len(files) == 0 {
		return files
	}
	out := make([]*provider.ChangedFile, 0, len(files))
	byPath := make(map[string]*provider.ChangedFile, len(files))
	for _, f := range files {
		cp := *f
		out = append(out, &cp)
		if cp.NewPath != "" {
			byPath[cp.NewPath] = &cp
		}
		if cp.OldPath != "" {
			byPath[cp.OldPath] = &cp
		}
	}
	for _, chunk := range diffx.SplitChunks(raw) {
		f, ok := byPath[diffx.DiffPath(chunk)]
		if !ok {
			continue
		}
		f.Diff = strings.TrimPrefix(chunk, "diff --git ")
	}
	return out
}
