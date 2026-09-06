package plugin

import akseverity "git.example.com/enjoydream/agentkit/severity"

// severity 词表钉死为 {high, medium, low}（design §3.3）。
// 实现整体委托 agentkit/severity——Argus 不再自维护克隆（此前双源并存，
// 本地 glob 版缺 .gitignore 尾随 ** 语义与多字节 ? 支持，已随 v0.7.1 收敛）。
// Merger 入口统一映射，越界值一律归 low 并由调用方告警（R5.2 真值表依赖此归一化）。
const (
	SeverityHigh   = akseverity.High
	SeverityMedium = akseverity.Medium
	SeverityLow    = akseverity.Low
)

// NormalizeSeverity 把任意写法归一到词表。
// 第二个返回值=false 表示原值越界（已归为 low，调用方应记录告警）。
func NormalizeSeverity(s string) (string, bool) { return akseverity.Normalize(s) }

// SeverityRank 排序权重（high > medium > low）。
func SeverityRank(s string) int { return akseverity.Rank(s) }

// HigherSeverity 返回两者中更严重的级别。
func HigherSeverity(a, b string) string { return akseverity.Higher(a, b) }

// SeverityValid 是否在词表内。
func SeverityValid(s string) bool { return akseverity.Valid(s) }

// Fingerprint 意见指纹（file + 规范化 comment 的 SHA256 前 16 位，跨轮去重）。
func Fingerprint(file, comment string) string { return akseverity.Fingerprint(file, comment) }

// GlobMatch 极简 glob（.gitignore 语义：`web/**` 只匹配目录内部；`?` 按 rune）。
func GlobMatch(pattern, path string) bool { return akseverity.GlobMatch(pattern, path) }
