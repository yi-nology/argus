// Package config 承载 Argus 全部运行配置（design §6/§12）。
// 环境变量是唯一配置来源；plugins.yaml 只声明插件清单，不承载服务配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// PlatformConfig 单个 git 平台的接入凭证。
type PlatformConfig struct {
	BaseURL string
	Token   string
	Secret  string // webhook 签名密钥（GitLab 是静态 token 语义，SDK 已屏蔽差异）
	// SkipTLS 跳过 TLS 证书校验（内网自建实例自签/过期证书场景，如 git.example.com）。
	// 仅影响平台 API 与工作副本克隆；开启时日志告警提示风险。
	SkipTLS bool
}

// LLMConfig OpenAI 兼容端点（复用 lacus/.env）。
type LLMConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	MaxTokens    int // 单次生成上限；推理模型（如 mimo）需 >= 4096
	ContextToken int // 模型上下文窗口（token 数），用于缺省 diff 截断公式
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
	TaskTokenBudget    int
	ReportMeta         bool
	PluginsFile        string
	SkillsDir          string
	WorkRoot           string
	Platforms          map[string]PlatformConfig
	LLM                LLMConfig
	Version            string

	// 轮询兜底任务源（v3.0.1 改造）：平台入站 webhook 不可达时的替代来源。
	PollInterval time.Duration // 0 = 关闭
	PollRepos    string        // "platform:owner/repo" 逗号分隔

	// 任务保留天数（运维加固）：超期已终结任务（done/failed/stale）启动+每日清理。
	// 0 = 永久保留（默认，行为与 v3.0 一致）。
	TaskRetentionDays int

	// Debug 留存阶段产物（R1 spec/R2 摘要/R3 原始输出）到任务记录，GET /tasks/:id 可查。
	Debug bool
}

// MaxDiffCharsResolved 返回生效的 diff 总量上限（design §6：显式设置则覆盖公式；
// 缺省按模型上下文窗口动态算：窗口 35%，4 chars/token，min 10k / max 500k）。
func (c *Config) MaxDiffCharsResolved() int {
	if c.MaxDiffChars > 0 {
		return c.MaxDiffChars
	}
	ctx := c.LLM.ContextToken
	if ctx <= 0 {
		ctx = 128000
	}
	n := ctx * 35 / 100 * 4
	if n < 10_000 {
		n = 10_000
	}
	if n > 500_000 {
		n = 500_000
	}
	return n
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
		Addr:               envStr("ARGUS_ADDR", ":8899"),
		DBDSN:              envStr("ARGUS_DB_DSN", "argus.db"),
		APIToken:           os.Getenv("ARGUS_APITOKEN"),
		BotUsername:        envStr("ARGUS_BOT_USERNAME", "argus-bot"),
		SkipDraft:          envBool("ARGUS_SKIP_DRAFT", true),
		MaxConcurrentTasks: envInt("ARGUS_MAX_CONCURRENT_TASKS", 2),
		MaxDiffChars:       envInt("ARGUS_MAX_DIFF_CHARS", 0),
		TaskTokenBudget:    envInt("ARGUS_TASK_TOKEN_BUDGET", 200_000),
		ReportMeta:         envBool("ARGUS_REPORT_META", true),
		PluginsFile:        envStr("ARGUS_PLUGINS", "plugins.yaml"),
		SkillsDir:          envStr("ARGUS_SKILLS_DIR", "skills"),
		WorkRoot:           envStr("ARGUS_WORK_ROOT", "/tmp/argus-work"),
		Platforms:          map[string]PlatformConfig{},
		PollInterval:       envDuration("ARGUS_POLL_INTERVAL", 0),
		PollRepos:          os.Getenv("ARGUS_POLL_REPOS"),
		TaskRetentionDays:  envInt("ARGUS_TASK_RETENTION_DAYS", 0),
		Debug:              envBool("ARGUS_DEBUG", false),
		LLM: LLMConfig{
			BaseURL:      os.Getenv("OPENAI_BASE_URL"),
			APIKey:       os.Getenv("OPENAI_API_KEY"),
			Model:        os.Getenv("OPENAI_MODEL"),
			Timeout:      envDuration("OPENAI_TIMEOUT", 120*time.Second),
			MaxTokens:    envInt("OPENAI_MAX_TOKENS", 8192),
			ContextToken: envInt("ARGUS_MODEL_CONTEXT_TOKENS", 128_000),
		},
	}
	for _, p := range []string{"gitea", "github", "gitlab"} {
		prefix := "ARGUS_" + strings.ToUpper(p) + "_"
		cfg.Platforms[p] = PlatformConfig{
			BaseURL: os.Getenv(prefix + "URL"),
			Token:   os.Getenv(prefix + "TOKEN"),
			Secret:  os.Getenv(prefix + "SECRET"),
			SkipTLS: envBool(prefix+"SKIP_TLS", false),
		}
	}
	return cfg
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
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
		return def
	}
	return d
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
	if c.TaskTokenBudget <= 0 {
		c.TaskTokenBudget = 200_000
	}
	enabled := 0
	for _, p := range c.Platforms {
		if p.Token != "" {
			enabled++
		}
	}
	if enabled == 0 {
		return fmt.Errorf("未配置任何 git 平台凭证（ARGUS_GITEA_TOKEN / ARGUS_GITHUB_TOKEN / ARGUS_GITLAB_TOKEN 至少一项）")
	}
	return nil
}
