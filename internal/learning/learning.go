// Package learning 模式级后悔学习环的域模型、状态投影与消费阈值（v3.47.0 自
// plugin 归位——本域消费方是 storage/dream/health/exportdata/config，与插件语义
// 无关）。本包是纯叶子：零内部 argus 依赖；确定性数学与键派生已下沉
// oagentkit/textx/reportutil（v3.77.0，与 WilsonCI/McNemarExact 同族归位 SDK），
// 本包保留 API 门面，39+ 消费方零改动。
//
// 设计：docs/review-closed-loop-design.md §3.2/§3.7。键不含 file/line——同一类问题在
// 不同文件聚成同一模式；含 plugin 分量——不同审查来源的信任历史不混用。
// 禁止 LLM 摘要：键必须可离线复算、可单测、可对账。
package learning

import (
	"strings"

	"github.com/oagentkit/textx/reportutil"
)

// PatternTextMax 存入 pattern_signals.pattern_text 的人读样本上限（rune）。
const PatternTextMax = reportutil.PatternTextMax

// PatternKey 派生仓库级模式键（sha256(plugin\x00normalize(comment)) 前 16 hex）。
func PatternKey(pluginName, comment string) string {
	return reportutil.PatternKey(pluginName, comment)
}

// PatternSample 返回入库的人读样本（规范化后截断）。
func PatternSample(comment string) string {
	return reportutil.PatternSample(comment)
}

// Confidence 由 posted/regret/suppress/restore 计数推 Beta 后验置信度
// （先验 0.5；Posted 抬、Regret/Suppress 压、Restore 只回抬不加正证据——
// suppress+restore 成对循环不可洗白，v3.8.0）。权重语义详见 reportutil.BetaConfidence。
func Confidence(posted, regret, suppress, restore int) float64 {
	return reportutil.BetaConfidence(posted, regret, suppress, restore)
}

// PatternSamples 参与消费判定的样本量（小样本不消费，防抖动）。
func PatternSamples(posted, regret, suppress int) int {
	return reportutil.PatternSamples(posted, regret, suppress)
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
