package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	akseverity "git.example.com/enjoydream/agentkit/severity"
)

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

// Fingerprint 意见指纹 v2（file + line + 规范化 comment 的 SHA256 前 16 位）。
// v3.7.0 起行号入指纹：同文件两处不同位置的同模板意见不再共享跨轮统计
// （轮内误并已由 merger 行号相容闸门拦截）。措辞漂移的跨轮匹配由 storage 的
// 近重复匹配层 + 别名表负责（fingerprint v2 消费方必须容忍"精确未命中→近重
// 复并入"语义，勿自行绕过 UpsertPrint）。
// 注意：与 agentkit severity.Fingerprint（v1，无行号）不同格式——存量行由
// storage 启动迁移重算（见 storage.migratePrintFingerprintsV2），勿混用两版。
func Fingerprint(file, comment string, line int) string {
	if line < 0 {
		line = 0
	}
	sum := sha256.Sum256([]byte("v2\x1f" + file + "\x1f" + strconv.Itoa(line) + "\x1f" + akseverity.NormalizeComment(comment)))
	return hex.EncodeToString(sum[:16])
}

// GlobMatch 极简 glob（.gitignore 语义：`web/**` 只匹配目录内部；`?` 按 rune）。
func GlobMatch(pattern, path string) bool { return akseverity.GlobMatch(pattern, path) }
