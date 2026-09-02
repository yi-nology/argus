package context

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yi-nology/git-platform-sdk/provider"

	"git.example.com/enjoydream/agentkit/textutil"

	"argus/internal/plugin"
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

// noise 判定路径是否为噪声。
func noise(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(p)
	for _, pat := range noisePatterns {
		switch {
		case strings.HasSuffix(pat, "/"):
			if strings.Contains(p, pat) {
				return true
			}
		case strings.HasPrefix(pat, ".") || strings.HasSuffix(pat, ".go") || strings.HasSuffix(pat, ".py"):
			if strings.HasSuffix(base, pat) {
				return true
			}
		default:
			if base == pat {
				return true
			}
		}
	}
	return false
}

// Prepare 从 SDK 的 MergeDiff 构建 plugin.DiffContext：
// 去 lockfile/vendor/二进制噪声 + 单文件与总量截断 + Truncated 标记。
// totalLimit <=0 表示不截断总量（单测用）。
// 兼容性兜底（v3.0.1 真实试点发现）：gitea 后端的 ChangedFile.Diff 为空、内容在 RawDiff
// （BuildRawDiff 拼接）——此时按 "diff --git" 切块回填到各文件，否则下游拿到空 diff。
func Prepare(md *provider.MergeDiff, totalLimit int) *plugin.DiffContext {
	files := md.Files
	if len(files) > 0 && files[0].Diff == "" && strings.TrimSpace(md.RawDiff) != "" {
		files = fillFromRawDiff(files, md.RawDiff)
	}
	dc := &plugin.DiffContext{Files: make([]plugin.DiffFile, 0, len(files))}
	var b strings.Builder
	remaining := totalLimit
	if totalLimit > 0 {
		b.Grow(totalLimit / 2)
	}
	singleMax := singleFileLimit(totalLimit)

	for _, f := range files {
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
		// 噪声（lockfile/vendor/生成物/二进制）：不进文本，元信息保留
		if f.IsBinary || noise(entry.Path) {
			dc.Files = append(dc.Files, entry)
			continue
		}
		if totalLimit > 0 && remaining <= 0 {
			entry.Included = false
			dc.Files = append(dc.Files, entry)
			dc.Truncated = true
			continue
		}
		text := f.Diff
		if rc := len([]rune(text)); rc > singleMax {
			text, _ = textutil.TruncRunes(text, singleMax)
			text += fmt.Sprintf("\n…（%s 截断，原 %d 字符）", entry.Path, len(f.Diff))
		}
		if totalLimit > 0 {
			if rc := len([]rune(text)); rc > remaining {
				text, _ = textutil.TruncRunes(text, remaining)
				text += "\n…（总量截断）"
				dc.Truncated = true
			}
			remaining -= len([]rune(text))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
		entry.Included = true
		dc.Files = append(dc.Files, entry)
	}
	dc.Text = b.String()
	return dc
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
func PrepareIncremental(md *provider.MergeDiff, lastDiffText string, totalLimit int) *plugin.DiffContext {
	// 无上次审查记录 → 全量审查
	if lastDiffText == "" {
		return Prepare(md, totalLimit)
	}

	files := md.Files
	if len(files) > 0 && files[0].Diff == "" && strings.TrimSpace(md.RawDiff) != "" {
		files = fillFromRawDiff(files, md.RawDiff)
	}

	// 解析上次 diff 为 per-file map
	lastByPath := parseDiffByFile(lastDiffText)

	dc := &plugin.DiffContext{Files: make([]plugin.DiffFile, 0, len(files))}
	var b strings.Builder
	remaining := totalLimit
	if totalLimit > 0 {
		b.Grow(totalLimit / 2)
	}
	singleMax := singleFileLimit(totalLimit)

	hasChanges := false
	for _, f := range files {
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

		// 噪声/二进制：不进文本，元信息保留（与 Prepare 一致）
		if f.IsBinary || noise(entry.Path) {
			dc.Files = append(dc.Files, entry)
			continue
		}

		// 比对：当前文件 diff 与上次是否一致
		lastDiff, existed := lastByPath[entry.Path]
		if existed && f.Diff == lastDiff {
			// 完全一致 → 标记已审查，不进文本
			entry.AlreadyReviewed = true
			dc.Files = append(dc.Files, entry)
			continue
		}

		// 文件有变化（新增/修改/删除），或上次不存在该文件
		hasChanges = true

		if totalLimit > 0 && remaining <= 0 {
			entry.Included = false
			dc.Files = append(dc.Files, entry)
			dc.Truncated = true
			continue
		}
		text := f.Diff
		if rc := len([]rune(text)); rc > singleMax {
			text, _ = textutil.TruncRunes(text, singleMax)
			text += fmt.Sprintf("\n…（%s 截断，原 %d 字符）", entry.Path, len(f.Diff))
		}
		if totalLimit > 0 {
			if rc := len([]rune(text)); rc > remaining {
				text, _ = textutil.TruncRunes(text, remaining)
				text += "\n…（总量截断）"
				dc.Truncated = true
			}
			remaining -= len([]rune(text))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
		entry.Included = true
		dc.Files = append(dc.Files, entry)
	}

	dc.Text = b.String()

	// 增量 diff 太短 → 回退全量审查（避免 LLM 拿到无上下文的碎片）
	if hasChanges && len([]rune(dc.Text)) < minIncrementalChars {
		return Prepare(md, totalLimit)
	}

	if !hasChanges {
		dc.NoNewChanges = true
	}
	return dc
}

// parseDiffByFile 将拼接的 unified diff 按文件路径切分为 map[path]diffText。
// 与 fillFromRawDiff 不同，这里保留 "diff --git " 前缀，使存储值与 ChangedFile.Diff 一致。
func parseDiffByFile(raw string) map[string]string {
	result := make(map[string]string)
	if raw == "" {
		return result
	}
	const sep = "diff --git "
	chunks := strings.Split(raw, "\n"+sep)
	for i, chunk := range chunks {
		if i == 0 {
			if strings.HasPrefix(chunk, sep) {
				// 已有前缀，保留原样
			} else if strings.TrimSpace(chunk) == "" {
				continue
			} else {
				// 无前缀（罕见），补回
				chunk = sep + chunk
			}
		} else {
			chunk = sep + chunk
		}
		path := rawDiffPath(chunk)
		if path != "" {
			result[path] = chunk
		}
	}
	return result
}

// fillFromRawDiff 把拼接的 unified diff 按 "diff --git" 切块回填到各 ChangedFile.Diff。
// 路径对齐优先 NewPath（含 +++ b/<path> 行），删除文件回退 OldPath；未匹配的块忽略。
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
	const sep = "diff --git "
	chunks := strings.Split(raw, "\n"+sep)
	for i, chunk := range chunks {
		if i == 0 {
			if strings.HasPrefix(chunk, sep) {
				chunk = strings.TrimPrefix(chunk, sep)
			} else if strings.TrimSpace(chunk) == "" {
				continue
			}
		} else {
			chunk = sep + chunk
		}
		path := rawDiffPath(chunk)
		if path == "" {
			continue
		}
		if f, ok := byPath[path]; ok {
			f.Diff = chunk
		}
	}
	return out
}

// rawDiffPath 从单文件 diff 块提取目标路径（+++ b/<path>，删除文件回退 --- a/<path>）。
func rawDiffPath(chunk string) string {
	var minus, plus string
	for _, line := range strings.Split(chunk, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			plus = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/"):
			minus = strings.TrimPrefix(line, "--- a/")
		}
		if plus != "" {
			break
		}
	}
	// 去掉行尾制表符附加信息（如 "+++ b/x.go\t2026-01-02"）
	if i := strings.IndexByte(plus, '\t'); i >= 0 {
		plus = plus[:i]
	}
	if plus != "" && plus != "/dev/null" {
		return plus
	}
	if i := strings.IndexByte(minus, '\t'); i >= 0 {
		minus = minus[:i]
	}
	return minus
}
