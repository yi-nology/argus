// Package config 承载 Argus 全部运行配置（design §6/§12）。
// 环境变量是唯一配置来源；plugins.yaml 只声明插件清单，不承载服务配置。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	ekitconfig "git.example.com/enjoydream/ekit/config"
)

// PlatformConfig 单个 git 平台的接入凭证。
type PlatformConfig struct {
	BaseURL string
	Token   string
	Secret  string // webhook 签名密钥（GitLab 是静态 token 语义，SDK 已屏蔽差异）
	// SkipTLS 跳过 TLS 证书校验（内网自建实例自签/过期证书场景，如 git.example.com）。
	// 仅影响平台 API 与工作副本克隆；开启时日志告警提示风险。
	SkipTLS bool
	// TokenStyle 覆盖默认认证头风格: "bearer" (Authorization: Bearer) / "private" (PRIVATE-TOKEN)。
	// 空值使用平台默认值（GitLab=private, GitHub=bearer）。
	TokenStyle string

	// GitHub App 认证（仅 github 平台）：配置后自动用 installation token 替代 PAT。
	// 三者全配才启用；AppID + PrivateKey 用于签发 JWT，InstallationID 用于换取 token。
	GitHubAppID          int64  // ARGUS_GITHUB_APP_ID
	GitHubAppPrivateKey  string // ARGUS_GITHUB_APP_PRIVATE_KEY（PEM 内容或 base64 编码）
	GitHubAppInstallationID int64 // ARGUS_GITHUB_APP_INSTALLATION_ID
}

// LLMConfig OpenAI 兼容端点（复用 lacus/.env）。
type LLMConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	MaxTokens    int // 单次生成上限；0 = 按 MaxTokensResolved 从窗口派生；推理模型（如 mimo）需 >= 4096
	ContextToken int // 模型上下文窗口（token 数，输入+输出合计），其余上限的唯一派生基准
}

// Config 服务全局配置。
type Config struct {
	Addr               string
	DBDSN              string
	APIToken           string // 空 = 关闭鉴权（仅学习期允许，启动时告警）
	BotUsername        string
	SkipDraft          bool
	MaxConcurrentTasks int
	MaxDiffChars       int // <=0 表示按公式动态计算
	TaskTokenBudget    int // <=0 表示按公式动态计算（EffectiveTaskTokenBudget）
	ReportMeta         bool
	PluginsFile        string
	SkillsDir          string
	WorkRoot           string
	KnowledgeDir       string // RAG 知识库目录（markdown）；目录不存在时知识检索禁用
	Platforms          map[string]PlatformConfig
	LLM                LLMConfig
	Version            string

	// 轮询兜底任务源（v3.0.1 改造）：平台入站 webhook 不可达时的替代来源。
	PollInterval time.Duration // 0 = 关闭
	PollRepos    string        // "platform:owner/repo" 逗号分隔

	// octopus IM 推送（v3.1）：终态经 octopus /api/v2/send/quick 发消息。
	// URL 空 = 禁用。
	OctopusURL       string
	OctopusChannelID int64
	OctopusUser      string // 机器账号（X-User-Id，默认 argus-bot）
	OctopusToken     string // 可选 Bearer

	// 仓库级月度 token 预算（v3.1）：单仓库本月 done 任务累计消耗上限，
	// 超限任务直接 failed（不调 LLM）。0 = 关闭（缺省）。
	RepoMonthlyTokenBudget int

	// LLMRateLimit 全局 LLM 请求速率限制（requests/sec）；0 = 不限速。
	// 配合 LLMRateBurst 使用，token bucket 限速器在 LLM 调用前阻塞等待。
	LLMRateLimit float64
	// LLMRateBurst 限速器突发容量；0 = 默认等于 MaxConcurrentTasks。
	LLMRateBurst int

	// WebhookMaxBody Webhook 请求体上限（字节）；默认 1MB。
	// Hertz WithMaxRequestBodySize + Content-Length 双重防护。
	WebhookMaxBody int

	// 任务保留天数（运维加固）：超期已终结任务（done/failed/stale）启动+每日清理。
	// 0 = 永久保留（默认，行为与 v3.0 一致）。
	TaskRetentionDays int

	// Debug 留存阶段产物（R1 spec/R2 摘要/R3 原始输出）到任务记录，GET /tasks/:id 可查。
	Debug bool

	// Milvus 向量检索配置（ARGUS_MILVUS_ADDR 非空时启用）。
	MilvusAddr      string // Milvus 地址，如 "localhost:19530"
	MilvusCollection string // 集合名，默认 "agentkit_knowledge"
	// Embedding 配置（Milvus 向量化）。
	EmbeddingBaseURL string // OpenAI 兼容端点
	EmbeddingAPIKey  string
	EmbeddingModel   string // 如 text-embedding-3-small / text-embedding-v3
	EmbeddingDim     int    // 向量维度
}

// 上限派生基准（design §6 v3.1 修订）：模型窗口是唯一基准，全部上限按比例派生，
// 防止"换了 1M 模型服务仍按 128k 截断"的配置漂移。chars/token 按 2 保守估算
// （中文约 0.6~1 token/char，按 4 估会低估 token 数导致超窗 400）。

// contextTokens 返回生效的模型窗口（缺省 128k，防未配置时按小窗口裸奔）。
func (c *Config) contextTokens() int {
	if c.LLM.ContextToken > 0 {
		return c.LLM.ContextToken
	}
	return 128_000
}

// MaxDiffCharsResolved 返回生效的 diff 总量上限：显式 env 优先；缺省 = 窗口 35%
// 折算字符，min 10k。无硬顶——窗口变大 diff 上限跟着变大（500k 硬顶会让 1M 窗口空转）。
func (c *Config) MaxDiffCharsResolved() int {
	if c.MaxDiffChars > 0 {
		return c.MaxDiffChars
	}
	n := c.contextTokens() * 35 / 100 * 4
	if n < 10_000 {
		n = 10_000
	}
	return n
}

// EffectiveTaskTokenBudget 任务 token 预算：显式 env 优先，缺省 = 窗口 60%
// （R1+R2+R3+R4 累计；R4 fan-out 是大头，比例给足防大任务误裁插件）。
func (c *Config) EffectiveTaskTokenBudget() int {
	if c.TaskTokenBudget > 0 {
		return c.TaskTokenBudget
	}
	return c.contextTokens() * 60 / 100
}

// MaxTokensResolved 单次生成输出上限：显式 env 优先，缺省 = max(4096, 窗口 5%)
// （R1 面对超长需求可能改写出上百条 AC，8k 固定值会截断 JSON 导致降级 COMMENT）。
func (c *Config) MaxTokensResolved() int {
	if c.LLM.MaxTokens > 0 {
		return c.LLM.MaxTokens
	}
	n := c.contextTokens() * 5 / 100
	if n < 4096 {
		n = 4096
	}
	return n
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

// MaxCallInputChars 单次 LLM 调用的输入字符上限（llm.fitInput 兜底）：
// （窗口 - 输出预留）× 2 chars/token，再打 9 折留分词误差余量。
func (c *Config) MaxCallInputChars() int {
	n := (c.contextTokens() - c.MaxTokensResolved()) * 2 * 9 / 10
	if n < 8_000 {
		n = 8_000
	}
	return n
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

// EnabledPlatforms 返回已配置 token 的平台名（小写）。
func (c *Config) EnabledPlatforms() []string {
	names := make([]string, 0, len(c.Platforms))
	for k, v := range c.Platforms {
		if v.Token != "" {
			names = append(names, k)
		}
	}
	return names
}

// Load 从环境变量读取全部配置（已加载 .env 后调用）。
func Load() *Config {
	cfg := &Config{
		Addr:                   envStr("ARGUS_ADDR", ":8899"),
		DBDSN:                  envStr("ARGUS_DB_DSN", "argus.db"),
		APIToken:               os.Getenv("ARGUS_APITOKEN"),
		BotUsername:            envStr("ARGUS_BOT_USERNAME", "argus-bot"),
		SkipDraft:              envBool("ARGUS_SKIP_DRAFT", true),
		MaxConcurrentTasks:     envInt("ARGUS_MAX_CONCURRENT_TASKS", 2),
		MaxDiffChars:           envInt("ARGUS_MAX_DIFF_CHARS", 0),
		TaskTokenBudget:        envInt("ARGUS_TASK_TOKEN_BUDGET", 0),
		ReportMeta:             envBool("ARGUS_REPORT_META", true),
		PluginsFile:            envStr("ARGUS_PLUGINS", "plugins.yaml"),
		SkillsDir:              envStr("ARGUS_SKILLS_DIR", "skills"),
		WorkRoot:               envStr("ARGUS_WORK_ROOT", "/tmp/argus-work"),
		KnowledgeDir:           envStr("ARGUS_KNOWLEDGE_DIR", "knowledge"),
		Platforms:              map[string]PlatformConfig{},
		PollInterval:           envDuration("ARGUS_POLL_INTERVAL", 0),
		PollRepos:              os.Getenv("ARGUS_POLL_REPOS"),
		TaskRetentionDays:      envInt("ARGUS_TASK_RETENTION_DAYS", 0),
		RepoMonthlyTokenBudget: envInt("ARGUS_REPO_MONTHLY_TOKEN_BUDGET", 0),
		LLMRateLimit:           envFloat("ARGUS_LLM_RATE_LIMIT", 0),
		LLMRateBurst:           envInt("ARGUS_LLM_RATE_BURST", 0),
		WebhookMaxBody:         envInt("ARGUS_WEBHOOK_MAX_BODY", 1<<20),
		OctopusURL:             os.Getenv("ARGUS_OCTOPUS_URL"),
		OctopusChannelID:       int64(envInt("ARGUS_OCTOPUS_CHANNEL_ID", 0)),
		OctopusUser:            envStr("ARGUS_OCTOPUS_USER", "argus-bot"),
		OctopusToken:           os.Getenv("ARGUS_OCTOPUS_TOKEN"),
		Debug:                  envBool("ARGUS_DEBUG", false),
		// Milvus 向量检索
		MilvusAddr:       os.Getenv("ARGUS_MILVUS_ADDR"),
		MilvusCollection: envStr("ARGUS_MILVUS_COLLECTION", "agentkit_knowledge"),
		// Embedding
		EmbeddingBaseURL: os.Getenv("ARGUS_EMBEDDING_BASE_URL"),
		EmbeddingAPIKey:  os.Getenv("ARGUS_EMBEDDING_API_KEY"),
		EmbeddingModel:   envStr("ARGUS_EMBEDDING_MODEL", "text-embedding-v3"),
		EmbeddingDim:     envInt("ARGUS_EMBEDDING_DIM", 1024),
		LLM: LLMConfig{
			BaseURL:      os.Getenv("OPENAI_BASE_URL"),
			APIKey:       os.Getenv("OPENAI_API_KEY"),
			Model:        os.Getenv("OPENAI_MODEL"),
			Timeout:      envDuration("OPENAI_TIMEOUT", 300*time.Second),
			MaxTokens:    envInt("OPENAI_MAX_TOKENS", 0),
			ContextToken: envInt("ARGUS_MODEL_CONTEXT_TOKENS", 128_000),
		},
	}
	for _, p := range []string{"gitea", "github", "gitlab"} {
		prefix := "ARGUS_" + strings.ToUpper(p) + "_"
		cfg.Platforms[p] = PlatformConfig{
			BaseURL:    os.Getenv(prefix + "URL"),
			Token:      os.Getenv(prefix + "TOKEN"),
			Secret:     os.Getenv(prefix + "SECRET"),
			SkipTLS:    envBool(prefix+"SKIP_TLS", false),
			TokenStyle: os.Getenv(prefix + "TOKEN_STYLE"),
		}
	}
	// GitHub App 认证：三者全配时启用（覆盖 Token）
	if gh, ok := cfg.Platforms["github"]; ok {
		gh.GitHubAppID = int64(envInt("ARGUS_GITHUB_APP_ID", 0))
		gh.GitHubAppPrivateKey = os.Getenv("ARGUS_GITHUB_APP_PRIVATE_KEY")
		gh.GitHubAppInstallationID = int64(envInt("ARGUS_GITHUB_APP_INSTALLATION_ID", 0))
		cfg.Platforms["github"] = gh
	}
	return cfg
}

// env 读取助手：类型解析复用 ekit config（strconv 同源），非法值回退默认时
// 保留 warnBadEnv 留痕——ekit 的 *WithDefault 系列是静默回退，不能直接换。

func envStr(key, def string) string {
	return ekitconfig.GetStringWithDefault(key, def)
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, ok := ekitconfig.GetBool(key)
	if !ok {
		warnBadEnv(key, v, def)
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, ok := ekitconfig.GetInt(key)
	if !ok {
		warnBadEnv(key, v, def)
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		warnBadEnv(key, v, def)
		return def
	}
	return d
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		warnBadEnv(key, v, def)
		return def
	}
	return f
}

// warnBadEnv 非法环境变量值回退默认时必须留痕——静默回退会让
// "以为开了 SKIP_TLS/DEBUG/MAX_CONCURRENT 实际没开"的配置漂移无从排查
// （config 包此时还没有 logger，直接 stderr）。
func warnBadEnv(key, val string, def any) {
	fmt.Fprintf(os.Stderr, "argus.config: 环境变量 %s=%q 非法，回退默认值 %v\n", key, val, def)
}

// LoadDotEnv 依次尝试常见位置的 .env（服务根目录、lacus/ 根目录；test 子目录多一级）。
// 仅当环境变量尚不存在时写入，已 export 的优先。
func LoadDotEnv() {
	for _, path := range []string{"./.env", "../.env", "../../.env", "../../../.env"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
		return
	}
}

// Validate 启动期校验：无 LLM 配置时 builtin 插件不可用，仍允许启动（cli 插件可独立工作）。
func (c *Config) Validate() error {
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
		return fmt.Errorf("未配置任何 git 平台凭证（ARGUS_GITEA_TOKEN / ARGUS_GITHUB_TOKEN / ARGUS_GITLAB_TOKEN 至少一项，或配置 GitHub App）")
	}
	return nil
}
