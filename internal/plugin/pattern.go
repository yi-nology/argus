// pattern.go —— 模式级后悔学习环的确定性 pattern_key 派生。
//
// 设计：docs/review-closed-loop-design.md §3.2。键不含 file/line——同一类问题在
// 不同文件聚成同一模式；含 plugin——不同审查来源的信任历史不混用。
// 禁止 LLM 摘要：键必须可离线复算、可单测、可对账。
package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// PatternTextMax 存入 pattern_signals.pattern_text 的人读样本上限（rune）。
const PatternTextMax = 160

// normalizePatternComment 规范化评论文本用于模式键：
// NFKC → 剥路径/十六进制/反引号标识符/纯数字 → 折叠空白 → 小写 → 截断。
func normalizePatternComment(comment string) string {
	s := strings.ToLower(comment)

	// 反引号/路径/十六进制/数字先剥，避免 "错误处理不佳 in `handler.go:12`"
	// 与 "错误处理不佳 in `service.go:99`" 落成不同模式。
	s = backtickRe.ReplaceAllString(s, " ")
	s = pathRe.ReplaceAllString(s, " ")
	s = hexRe.ReplaceAllString(s, " ")
	s = digitsRe.ReplaceAllString(s, " ")

	// 折叠空白；去掉标点中对模式无信息量的重复符号
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	// 再压一遍：仅保留字母数字与 CJK，其余标点变空白后再折叠
	s = punctRe.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	return truncateRunes(s, PatternTextMax)
}

var (
	backtickRe = regexp.MustCompile("`[^`]*`")
	pathRe     = regexp.MustCompile(`(?:[\w.-]+/)+[\w.-]+|\b\w+\.(?:go|ts|js|py|java|rs|c|cpp|h|hpp|md|yaml|yml|json|sql|sh|proto)\b`)
	hexRe      = regexp.MustCompile(`\b[0-9a-f]{6,}\b`)
	digitsRe   = regexp.MustCompile(`\b\d+\b`)
	spaceRe    = regexp.MustCompile(`\s+`)
	punctRe    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// PatternKey 派生仓库级模式键：sha256(plugin \x00 normalize(comment)) 前 16 hex。
// plugin 为空时用 "unknown"——缺插件归属的键仍可计算，但不应静默混入其他插件历史。
func PatternKey(pluginName, comment string) string {
	if pluginName == "" {
		pluginName = "unknown"
	}
	norm := normalizePatternComment(comment)
	sum := sha256.Sum256([]byte(pluginName + "\x00" + norm))
	return hex.EncodeToString(sum[:])[:16]
}

// PatternSample 返回入库的人读样本（规范化后截断），便于审计与提示词注入展示。
func PatternSample(comment string) string {
	return truncateRunes(normalizePatternComment(comment), PatternTextMax)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Confidence 由 posted/regret/suppress/restore 计数推 Beta 后验置信度。
// 先验 (α=2, β=2) → 0.5；Posted α+1；Regret β+2；Suppress β+3；
// Restore α+2 且 β 回抬 max(β-2,1)。samples=Posted+Regret+Suppress，读时计算不落列。
func Confidence(posted, regret, suppress, restore int) float64 {
	if posted < 0 {
		posted = 0
	}
	if regret < 0 {
		regret = 0
	}
	if suppress < 0 {
		suppress = 0
	}
	if restore < 0 {
		restore = 0
	}
	alpha := 2.0 + float64(posted) + 2*float64(restore)
	beta := 2.0 + 2*float64(regret) + 3*float64(suppress)
	if restore > 0 {
		// 撤销误杀：负证据回抬一档（不为负）
		if b := beta - 2*float64(restore); b < 1 {
			beta = 1
		} else {
			beta = b
		}
	}
	return alpha / (alpha + beta)
}

// PatternSamples 参与消费判定的样本量（小样本不消费，防抖动）。
func PatternSamples(posted, regret, suppress int) int {
	if posted < 0 {
		posted = 0
	}
	if regret < 0 {
		regret = 0
	}
	if suppress < 0 {
		suppress = 0
	}
	return posted + regret + suppress
}
