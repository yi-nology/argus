// config_validate.go —— 启动期校验面：Validate 规整 + WarnOnly 告警 + embedding 缺配探测。
package config

import (
	"fmt"
	"strings"
)

// Validate 启动期校验：无 LLM 配置时 builtin 插件不可用，仍允许启动（cli 插件可独立工作）。
func (c *Config) Validate() error {
	if c.PluginMaxConcurrent <= 0 {
		c.PluginMaxConcurrent = 1
	}
	if c.MaxConcurrentTasks <= 0 {
		c.MaxConcurrentTasks = 1
	}
	if c.LLMRateBurst <= 0 {
		c.LLMRateBurst = c.MaxConcurrentTasks
	}
	if c.WebhookMaxBody <= 0 {
		c.WebhookMaxBody = 1 << 20 // 1MB
	}
	// TaskTokenBudget=0 合法：按 EffectiveTaskTokenBudget 从模型窗口派生，不强制回填。
	enabled := 0
	for _, p := range c.Platforms {
		if p.Token != "" || p.GitHubAppEnabled() {
			enabled++
		}
	}
	if enabled == 0 {
		tokenVars := make([]string, 0, len(SupportedPlatforms))
		for _, p := range SupportedPlatforms {
			tokenVars = append(tokenVars, "ARGUS_"+strings.ToUpper(p)+"_TOKEN")
		}
		return fmt.Errorf("未配置任何 git 平台凭证（%s 至少一项，或配置 GitHub App）", strings.Join(tokenVars, " / "))
	}
	return nil
}

// WarnOnly 深挖修复：交叉校验的「部分配置」告警——静默降级会让配置漂移无从
// 排查（与 warnBadEnv 同哲学）。不阻断启动：降级路径本身合法。
func (c *Config) WarnOnly(log WarnLogger) {
	// Milvus 半配：配了地址但 embedding 三件套不全 → 知识飞轮静默关闭
	if c.MilvusAddr != "" {
		if missing := c.embeddingMissing(); len(missing) > 0 {
			log.Warn("argus.config.milvus_partial",
				"missing", strings.Join(missing, ","),
				"effect", "Milvus 知识库不启用，回退本地词元检索（知识飞轮关闭）")
		}
	}
	// fallback 三缺一：只配 FallbackModel 时降级链静默不存在
	if c.LLM.FallbackModel != "" &&
		(c.LLM.FallbackBaseURL == "" || c.LLM.FallbackAPIKey == "") {
		log.Warn("argus.config.fallback_partial",
			"effect", "LLM 降级链未启用（需 FALLBACK_BASE_URL/API_KEY/MODEL 三项全配）",
			"has_base_url", c.LLM.FallbackBaseURL != "", "has_api_key", c.LLM.FallbackAPIKey != "")
	}
}

// embeddingMissing 返回缺失的 embedding 配置项名。
func (c *Config) embeddingMissing() []string {
	var missing []string
	if c.EmbeddingBaseURL == "" {
		missing = append(missing, "ARGUS_EMBEDDING_BASE_URL")
	}
	if c.EmbeddingAPIKey == "" {
		missing = append(missing, "ARGUS_EMBEDDING_API_KEY")
	}
	if c.EmbeddingModel == "" {
		missing = append(missing, "ARGUS_EMBEDDING_MODEL")
	}
	return missing
}
