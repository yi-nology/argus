// config_accessors.go —— 派生量访问器面：预算/截断上限/成本/阶段覆盖等只读推导。
package config

import (
	"github.com/oagentkit/llm/llm"

	"argus/internal/learning"
)

// contextTokens 返回生效的模型窗口（缺省 128k，防未配置时按小窗口裸奔）。
func (c *Config) contextTokens() int {
	if c.LLM.ContextToken > 0 {
		return c.LLM.ContextToken
	}
	return 128_000
}

// budgetBase 窗口派生基准（agentkit/llm.BudgetConfig）——比例公式单一来源，
// 防止 Argus 与 agentkit 两侧公式漂移。Argus 专属的 env 覆盖与下限仍留在本包。
func (c *Config) budgetBase() llm.BudgetConfig {
	return llm.BudgetConfig{ContextTokens: c.contextTokens()}
}

// MaxDiffCharsResolved 返回生效的 diff 总量上限：显式 env 优先；缺省 = 窗口 35%
// 折算字符，min 10k（llm.BudgetConfig 派生）。无硬顶——窗口变大 diff 上限跟着变大
// （500k 硬顶会让 1M 窗口空转）。
func (c *Config) MaxDiffCharsResolved() int {
	if c.MaxDiffChars > 0 {
		return c.MaxDiffChars
	}
	return c.budgetBase().MaxDiffChars()
}

// EffectiveTaskTokenBudget 任务 token 预算：显式 env 优先，缺省 = 窗口 60%
// （R1+R2+R3+R4 累计；R4 fan-out 是大头，比例给足防大任务误裁插件）。
func (c *Config) EffectiveTaskTokenBudget() int {
	if c.TaskTokenBudget > 0 {
		return c.TaskTokenBudget
	}
	return c.budgetBase().TaskTokenBudget()
}

// MaxTokensResolved 生效的单次生成输出上限（恒正，供窗口运算）：
// 显式正值优先，缺省 = max(4096, 窗口 5%)（llm.BudgetConfig 派生）。
// （R1 面对超长需求可能改写出上百条 AC，8k 固定值会截断 JSON 导致降级 COMMENT。）
func (c *Config) MaxTokensResolved() int {
	if c.LLM.MaxTokens > 0 {
		return c.LLM.MaxTokens
	}
	return c.budgetBase().MaxOutputTokens()
}

// LearningConfig 后悔学习环消费阈值（learning.Config 的唯一派生出口，v3.47.0）：
// 此前 bootstrap/main/service 三处手写字面量映射同一组 Regret* 字段，阈值字段
// 变更需要同步多处。learning 是纯叶子包（仅 stdlib），config 引用不破坏
// 「config 不 import 重组件」的叶子位约定（对 plugin 的禁令不变）。
func (c *Config) LearningConfig() learning.Config {
	return learning.Config{
		Enabled:     c.RegretLearning,
		MinSamples:  c.RegretMinSamples,
		SoftFloor:   c.RegretSoftFloor,
		HardFloor:   c.RegretHardFloor,
		InjectFloor: c.RegretInjectFloor,
		InjectTopN:  c.RegretInjectTopN,
	}
}

// StageOverride 单阶段模型覆盖。
type StageOverride struct {
	Model         string // 模型名（继承主 BaseURL/APIKey）
	ContextTokens int    // 模型窗口（0 = 继承主窗口）
}

// stageOverride 解析 <ENV_BASE> + <ENV_BASE>_CONTEXT_TOKENS
// （envBase 形如 "ARGUS_MODEL_R1" / "ARGUS_QA_MODEL" / "ARGUS_REVIEWER_MODEL"）。
func stageOverride(envBase string) *StageOverride {
	m := envStr(envBase, "")
	if m == "" {
		return nil
	}
	return &StageOverride{Model: m, ContextTokens: envInt(envBase+"_CONTEXT_TOKENS", 0)}
}

// CostPer1K 每 1000 token 单价（prompt, completion），报告/记账换算用。
func (c *Config) CostPer1K() [2]float64 {
	return [2]float64{c.LLMCostPromptPer1K, c.LLMCostCompletionPer1K}
}

// MaxTokensParam LLM 请求参数用：显式负值原样透传（agentkit v0.7.0 语义 = 不下发
// max_tokens 参数，兼容要求 max_completion_tokens 的推理模型）；未显式配置时走
// MaxTokensResolved 派生正值。
func (c *Config) MaxTokensParam() int {
	if c.LLM.MaxTokens != 0 {
		return c.LLM.MaxTokens
	}
	return c.MaxTokensResolved()
}

// ReqBodyMaxChars R1 的 PR 描述单条上限（字符）：窗口 6% 折算。
// 上限定位是防恶意刷文本，不是省窗口——正常需求永远碰不到。
func (c *Config) ReqBodyMaxChars() int {
	n := c.contextTokens() * 6 / 100 * 2
	if n < 24_000 {
		n = 24_000
	}
	return n
}

// ReqIssueMaxChars R1 的单条关联 issue 正文上限（字符）：窗口 1.5% 折算。
func (c *Config) ReqIssueMaxChars() int {
	n := c.contextTokens() * 15 / 1000 * 2
	if n < 8_000 {
		n = 8_000
	}
	return n
}

// SASTEnabled SAST 前置是否启用（任一工具 bin 已配置）。
func (c *Config) SASTEnabled() bool {
	return c.SASTGitleaksBin != "" || c.SASTSemgrepBin != ""
}

// GetFileMaxRunes R3 get_file 工具单文件内容上限（rune）：窗口 6% 折算，min 20k。
func (c *Config) GetFileMaxRunes() int {
	n := c.contextTokens() * 6 / 100 * 2
	if n < 20_000 {
		n = 20_000
	}
	return n
}

// GetIssueMaxRunes R3 get_issue 工具正文上限（rune）：窗口 1.5% 折算，min 8k。
func (c *Config) GetIssueMaxRunes() int {
	n := c.contextTokens() * 15 / 1000 * 2
	if n < 8_000 {
		n = 8_000
	}
	return n
}

// GetDiffMaxRunes R3 get_diff 工具重取 diff 上限（rune）：与 diffprep 总量一致。
func (c *Config) GetDiffMaxRunes() int {
	return c.MaxDiffCharsResolved()
}

// GitHubAppEnabled 三者全配时返回 true（AppID + PrivateKey + InstallationID）。
func (pc *PlatformConfig) GitHubAppEnabled() bool {
	return pc.GitHubAppID > 0 && pc.GitHubAppPrivateKey != "" && pc.GitHubAppInstallationID > 0
}
