package context

import (
	"fmt"
	"path/filepath"
	"strings"

	"argus/internal/plugin"
	"github.com/yi-nology/git-platform-sdk/provider"
)

// 噪声路径规则（复用 v1 原型实现，design §4 术语：diffprep）。
// 命中者不进 diff 文本，但保留在 Files 元信息里（模型仍知道该文件被动过）。
var noisePatterns = []string{
	"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock",
	"poetry.lock", "Gemfile.lock", "composer.lock", "Podfile.lock",
	"vendor/", "third_party/", "node_modules/", "dist/", "build/",
	".pb.go", "_pb2.py", ".min.js", ".min.css", ".map", ".snap", ".svg", ".gif", ".png", ".jpg", ".jpeg", ".webp",
}

// SingleFileMaxChars 单文件 diff 字符截断（防单文件刷爆预算）。
const SingleFileMaxChars = 8000

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
func Prepare(md *provider.MergeDiff, totalLimit int) *plugin.DiffContext {
	dc := &plugin.DiffContext{Files: make([]plugin.DiffFile, 0, len(md.Files))}
	var b strings.Builder
	remaining := totalLimit
	if totalLimit > 0 {
		b.Grow(totalLimit / 2)
	}

	for _, f := range md.Files {
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
		if len(text) > SingleFileMaxChars {
			text = text[:SingleFileMaxChars] + fmt.Sprintf("\n…（%s 截断，原 %d 字符）", entry.Path, len(f.Diff))
		}
		if totalLimit > 0 && len(text) > remaining {
			text = text[:remaining] + "\n…（总量截断）"
			dc.Truncated = true
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
		remaining -= len(text)
		entry.Included = true
		dc.Files = append(dc.Files, entry)
	}
	dc.Text = b.String()
	return dc
}
