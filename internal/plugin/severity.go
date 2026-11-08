package plugin

import (
	"strings"

	aktextutil "github.com/oagentkit/textx/textutil"

	"argus/internal/domain"
)

// severity 词表钉死为 {high, medium, low}（design §3.3）。
// 单一来源在 internal/domain（委托 oagentkit/textx/reportutil）；本文件只是既有 API 门面。
const (
	SeverityHigh   = domain.SeverityHigh
	SeverityMedium = domain.SeverityMedium
	SeverityLow    = domain.SeverityLow
)

// NormalizeSeverity 把任意写法归一到词表。
// 第二个返回值=false 表示原值越界（已归为 low，调用方应记录告警）。
func NormalizeSeverity(s string) (string, bool) {
	return domain.NormalizeSeverity(s)
}

// DegradationMarker 插件在 Result.Notes 里自报"审查降级"的行内标记；
// Merger 把含该标记的行透出为报告 Degraded 注记。
const DegradationMarker = "[审查降级]"

// SeverityRank 排序权重（high > medium > low）。
func SeverityRank(s string) int { return domain.SeverityRank(s) }

// HigherSeverity 返回两者中更严重的级别。
func HigherSeverity(a, b string) string { return domain.HigherSeverity(a, b) }

// Fingerprint 意见指纹 v2。实现住在 internal/domain（storage 与 plugin 共用同一份）。
func Fingerprint(file, comment string, line int) string {
	return domain.Fingerprint(file, comment, line)
}

// FingerprintNorm 一次规范化同时给出指纹与规范化评语（避免双重 NormalizeComment）。
func FingerprintNorm(file, comment string, line int) (fp, norm string) {
	return domain.FingerprintNorm(file, comment, line)
}

// GlobMatch 极简 glob（.gitignore 语义：`web/**` 只匹配目录内部；`?` 按 rune）。
func GlobMatch(pattern, path string) bool { return aktextutil.GlobMatch(pattern, path) }

// RuntimeFailMark R3 运行中断降级 Detail 的统一前缀（v5.5.3 引入，v5.7.1
// 上提至契约层）：reportview 报告面据此把「本轮判定被运行中断」收敛成顶部
// banner，避免大清单逐条长文案刷屏；requirement 与 completeness 共用判定。
const RuntimeFailMark = "R3 运行中断"

// IsRuntimeFailDetail 判定 AC Detail 是否运行中断形态。
func IsRuntimeFailDetail(detail string) bool {
	return strings.HasPrefix(detail, RuntimeFailMark)
}
