package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// severity 词表钉死为 {high, medium, low}（design §3.3）：
// Merger 入口统一映射，越界值一律归 low 并由调用方告警（R5.2 真值表依赖此归一化）。
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// NormalizeSeverity 把任意写法归一到词表。
// 第二个返回值=false 表示原值越界（已归为 low，调用方应记录告警）。
func NormalizeSeverity(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high", "critical", "blocker", "error", "严重", "高危":
		return SeverityHigh, true
	case "medium", "moderate", "warning", "warn", "中", "中等":
		return SeverityMedium, true
	case "low", "info", "minor", "hint", "低", "提示":
		return SeverityLow, true
	default:
		return SeverityLow, false
	}
}

// SeverityRank 越大越严重。
func SeverityRank(s string) int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	default:
		return 1
	}
}

// HigherSeverity 取更严重的一方（归一化冲突取高）。
func HigherSeverity(a, b string) string {
	if SeverityRank(a) >= SeverityRank(b) {
		return a
	}
	return b
}

// ValidSeverity 是否属于钉死词表。
func ValidSeverity(s string) bool {
	return s == SeverityHigh || s == SeverityMedium || s == SeverityLow
}

// Fingerprint 精确指纹：规范化评论文本哈希 + file（design §3.3 v3.0 修订）。
// 行号不入指纹（±2 容差仅作匹配辅助）；同一指纹跨轮次即"同一问题"。
func Fingerprint(file, comment string) string {
	sum := sha256.Sum256([]byte(file + "\x1f" + NormalizeCommentText(comment)))
	return hex.EncodeToString(sum[:16])
}

// NormalizeCommentText 规范化：全小写、去所有空白与标点/符号差异。
// 语义取向：指纹追求"同一问题"的鲁棒匹配（空白/标点抖动不影响判定），
// 代价是极端措辞巧合会合并——由人工抑制通道兜底。
func NormalizeCommentText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// GlobMatch 极简 glob：支持 `**`（跨目录）与 `*`/`?`（单段），用于 Triggers.Paths 与 routing。
// 语义对齐 .gitignore 常见用法：`web/**` 匹配 web/ 下一切；`*.vue` 匹配任意目录下的 .vue。
func GlobMatch(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if pattern == "**" {
		return true
	}
	// 无 "/" 的模式：对任意目录深度匹配 basename（.gitignore 习惯）
	if !strings.Contains(pattern, "/") {
		return segmentMatch(pattern, path[strings.LastIndexByte(path, '/')+1:])
	}
	return globMatch(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func globMatch(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// ** 吃掉剩余所有段：对每个可能的后缀继续匹配
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(seg); i++ {
				if globMatch(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		if !segmentMatch(pat[0], seg[0]) {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

func segmentMatch(pattern, s string) bool {
	// 经典单段通配（* 不跨段）
	var (
		px, sx int
		starPx = -1
		starSx int
	)
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
			continue
		}
		if px < len(pattern) && pattern[px] == '*' {
			starPx = px
			starSx = sx
			px++
			continue
		}
		if starPx >= 0 {
			px = starPx + 1
			starSx++
			sx = starSx
			continue
		}
		return false
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}
