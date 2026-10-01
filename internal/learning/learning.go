// Package learning 模式级后悔学习环的确定性数学、状态投影与消费阈值（v3.47.0 自
// plugin 归位——本域消费方是 storage/dream/health/exportdata/config，与插件语义无关）。
// 本包是纯叶子：零内部 argus 依赖（文本原语走 agentkit/textutil，与 domain 委托
// reportutil 同纪律）；config 可安全引用（config 保持叶子位的约定是"不 import
// plugin 等重组件"，纯数学叶子是许可例外）。
//
// 设计：docs/review-closed-loop-design.md §3.2/§3.7。键不含 file/line——同一类问题在
// 不同文件聚成同一模式；含 plugin 分量——不同审查来源的信任历史不混用。
// 禁止 LLM 摘要：键必须可离线复算、可单测、可对账。
package learning

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"git.example.com/enjoydream/agentkit/textutil"
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

	out, _ := textutil.TruncRunes(s, PatternTextMax)
	return out
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
	out, _ := textutil.TruncRunes(normalizePatternComment(comment), PatternTextMax)
	return out
}

// FirstBy 返回 By 串的首段：Merge 阶段多插件命中同题会把 By 拼成 "a,b"，
// 首见者=评语作者。学习环全部记账（PatternPosted/PatternRegret/matchLowPattern）
// 的 plugin 分量必须统一走这里——漏截会让同一意见的 posted 与 regret 落进
// 不同 plugin 键（错账）。
func FirstBy(by string) string {
	if i := strings.IndexByte(by, ','); i > 0 {
		return by[:i]
	}
	return by
}

// Confidence 由 posted/regret/suppress/restore 计数推 Beta 后验置信度。
// 先验 (α=2, β=2) → 0.5；Posted α+1；Regret β+2；Suppress β+3；
// Restore 只回抬 β（max(β-2,1)），不加 α——此前 Restore α+2 会让
// suppress+restore 成对循环净抬置信度（α+2, β+1），API 持有者可循环洗白；
// 现在 restore 仅撤销部分负证据，成对循环净 β+1（置信度微降），无法洗白（v3.8.0）。
// samples=Posted+Regret+Suppress，读时计算不落列。
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
	alpha := 2.0 + float64(posted)
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

// PatternState 模式信任历史读侧投影（storage.PatternSignal 的读侧投影；
// 消费方 Merger/Runner/dream/health 直接吃本类型，避免双结构转换）。
type PatternState struct {
	Plugin      string
	Pattern     string
	PatternText string
	Posted      int
	Regret      int
	Suppress    int
	Restore     int
	Confidence  float64
	Samples     int
}

// SampleText 人读样本：PatternText 缺省时回退 Pattern 键（dream 知识导出/health
// 报告三处共用单源，v3.64.0）。
func (p PatternState) SampleText() string {
	if p.PatternText != "" {
		return p.PatternText
	}
	return p.Pattern
}

// Config 后悔学习环消费阈值（零值 = 全关）。唯一事实来源是
// config.Config.LearningConfig() 派生——禁止在消费方手写字面量映射。
type Config struct {
	Enabled     bool
	MinSamples  int
	SoftFloor   float64 // 低于此打「置信度低」标注
	HardFloor   float64 // 低于此 + low severity → 丢弃
	InjectFloor float64
	InjectTopN  int
}

// DefaultConfig 缺省阈值（与 design §3.7 一致）。
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		MinSamples:  3,
		SoftFloor:   0.35,
		HardFloor:   0.20,
		InjectFloor: 0.35,
		InjectTopN:  10,
	}
}

// RepoRef 仓库三元组（学习环域的仓库标识：pattern_signals 按 repo 聚合）。
// storage.RepoRef 是本类型的别名——域类型放本包（纯叶子），存储层跟随。
type RepoRef struct {
	Platform, Owner, Repo string
}
