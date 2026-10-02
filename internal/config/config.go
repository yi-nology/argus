// Package config 承载 Argus 全部运行配置（design §6/§12）。
// 环境变量是唯一配置来源；plugins.yaml 只声明插件清单，不承载服务配置。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/llm"

	"argus/internal/learning"
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
	GitHubAppID             int64  // ARGUS_GITHUB_APP_ID
	GitHubAppPrivateKey     string // ARGUS_GITHUB_APP_PRIVATE_KEY（PEM 内容或 base64 编码）
	GitHubAppInstallationID int64  // ARGUS_GITHUB_APP_INSTALLATION_ID
}

// LLMConfig OpenAI 兼容端点（复用 lacus/.env）。
type LLMConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	MaxTokens    int // 单次生成上限；0 = 按 MaxTokensResolved 从窗口派生；推理模型（如 mimo）需 >= 4096
	ContextToken int // 模型上下文窗口（token 数，输入+输出合计），其余上限的唯一派生基准

	// 降级备选模型（可选）：四项全配时启用 Resilient 多模型降级链，
	// 主模型失败（429/超时/超窗等）自动切换到备选。
	FallbackBaseURL      string
	FallbackAPIKey       string
	FallbackModel        string
	FallbackContextToken int

	// 降级链调优（可选）：
	LLMResilienceRetries   int           // 每模型重试次数（默认 2）
	LLMResilienceBaseDelay time.Duration // 重试退避基准（默认 2s）
}

// Config 服务全局配置。
type Config struct {
	Addr               string
	DBDSN              string
	APIToken           string // 空 = 关闭鉴权（仅学习期允许，启动时告警）
	BotUsername        string
	SkipDraft          bool
	MaxConcurrentTasks int
	// PluginMaxConcurrent 单任务内插件 fan-out 的最大并发数（ARGUS_PLUGIN_MAX_CONCURRENT，
	// 深挖修复：原硬编码 2 不可调）。防百炼等 API burst 429。
	PluginMaxConcurrent int
	MaxDiffChars        int // <=0 表示按公式动态计算
	TaskTokenBudget     int // <=0 表示按公式动态计算（EffectiveTaskTokenBudget）
	ReportMeta          bool
	PluginsFile         string
	SkillsDir           string
	WorkRoot            string
	KnowledgeDir        string // RAG 知识库目录（markdown）；目录不存在时知识检索禁用
	Platforms           map[string]PlatformConfig
	LLM                 LLMConfig
	Version             string

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

	// LLMCostPromptPer1K / LLMCostCompletionPer1K 每 1000 token 单价（美元），
	// 用于报告成本摘要与成本记账；0 = 不计价（报告不出现成本行）。
	LLMCostPromptPer1K     float64
	LLMCostCompletionPer1K float64

	// 阶段模型覆盖（P6 分阶段路由）：ARGUS_MODEL_<STAGE> 配模型名，
	// ARGUS_MODEL_<STAGE>_CONTEXT_TOKENS 配该阶段窗口。
	// v3.33.0 起窗口对全部阶段生效：Generator 链（R1/R2/REVIEWER）走 Client
	// fitInput；直调阶段（R3/R35/QA/ROUTER）走 ctxfit 两级拟合（此前直调路径
	// 窗口被忽略并启动期告警——v3.8.0 如实化，现已闭环）。
	// 覆盖阶段继承主 BaseURL/APIKey 与全局降级链（fallback 模型不覆盖）；
	// 未配置的阶段走主模型。STAGE ∈ {R1, R2, R3, R35, REVIEWER, QA, ROUTER}。
	StageModelR1       *StageOverride
	StageModelR2       *StageOverride
	StageModelR3       *StageOverride
	StageModelR35      *StageOverride // R3.5 报告反思（v3.4 reflection）；缺省沿用 R3 模型
	StageModelReviewer *StageOverride
	StageModelQA       *StageOverride
	StageModelRouter   *StageOverride // 评论意图路由分类器（v3.4 router）；快模型即可
	// ReflectRounds R3.5 报告反思的"批判→修订"轮数上限（ARGUS_REFLECTION_ROUNDS）。
	// 0 = 关闭（缺省）；1~3 = 启用（>3 钳制为 3）。开启后每任务多 2~2N 次模型调用。
	ReflectRounds int
	// ReflectEvidence 证据门模式（ARGUS_REFLECTION_EVIDENCE）：
	//   off   = 行为同 v3.4（缺省，不校验 evidence/counterexample）；
	//   soft  = 缺证据强制降为 low + 标注，不剔除、不记 regret；
	//   strict= 缺证据剔除并记 regret（无证据不得发表）。
	// 仅在 ReflectRounds>0 时生效。
	ReflectEvidence string
	// ReactMaxIterations builtin 插件（correctness/security 等）ReAct 循环迭代上限
	// （ARGUS_REACT_MAX_ITERATIONS）。缺省 12（历史行为）；推理型模型在大 MR 上
	// 12 轮可能不够，超限后两次 ReAct 尝试都空转到上限再降级单轮，白白多花数分钟。
	// <2 视为未配置回退默认。仅 toolsHook 非 nil（ReAct 模式）时生效。
	// ⚠️ 不覆盖 requirement R3——其迭代上限走 reactMaxIterations(AC 条数) 派生
	//（base 12 / 封顶 24，无 env），两套上限互不相干（v3.41.0 注释如实化）。
	ReactMaxIterations int
	// CompletenessPolicy 完备性裁判策略（ARGUS_COMPLETENESS_POLICY，v3.11.0 G4）：
	//   annotate = 缺省，失败项只追加报告降级注记，不改 verdict；
	//   demote   = 任一完备性检查不过时 APPROVE→COMMENT（rule=R5.C）。
	CompletenessPolicy string
	// CompletenessRetry 受限补审（ARGUS_COMPLETENESS_RETRY，默认关闭）：
	// 执行失败的插件在剩余预算内重试一次，成功替换原结果（不叠加 findings）。
	CompletenessRetry bool
	// RegretLearning 模式级后悔学习环（ARGUS_REGRET_LEARNING，默认关闭）。
	// 开启后：Merger 按置信度标注/丢弃 low 条目，R4 注入历史误报模式提示词。
	RegretLearning bool
	// 学习环阈值（design §3.7）。
	RegretMinSamples  int
	RegretSoftFloor   float64
	RegretHardFloor   float64
	RegretInjectFloor float64
	RegretInjectTopN  int
	// DreamInterval 长期记忆整合周期（ARGUS_DREAM_INTERVAL，Go duration；
	// 0=关闭；建议 6h/24h）。仅在 RegretLearning 开启时有意义。
	DreamInterval time.Duration
	// HealthReportInterval 审查健康报告周期（ARGUS_HEALTH_REPORT_INTERVAL，
	// v3.11.0 G5，Go duration；0 = 关闭）。周期聚合学习环状态并经 octopus IM
	// 推送（未配 ARGUS_OCTOPUS_URL 时仅写日志）。建议 168h（周报）。
	HealthReportInterval time.Duration
	// CommentRouter 评论意图路由（ARGUS_COMMENT_ROUTER，默认关闭）：
	// @argus 自由文本评论先分类再分派——review（要求重新审查）建新任务，其余走 Q&A。
	// 显式关键词（"重新审查"等短命令）零分类调用直达；歧义文本才走 LLM 分类。
	CommentRouter bool
	// RouterMinConfidence LLM 意图分类的置信度下限（0~1，默认 0.6）；
	// 低于阈值一律回落 ask（Q&A），宁可多答不误触发重审。
	RouterMinConfidence float64
	// LLMRateLimit 全局 LLM 请求速率限制（requests/sec）；0 = 不限速。
	// 配合 LLMRateBurst 使用，token bucket 限速器在 LLM 调用前阻塞等待。
	LLMRateLimit float64
	// LLMRateBurst 限速器突发容量；0 = 默认等于 MaxConcurrentTasks。
	LLMRateBurst int

	// CommentTimeout 单条评论事件（Q&A/重审路由/suppress 指令）的整体超时上界
	//（ARGUS_COMMENT_TIMEOUT，Go duration；默认 10m）。此前 HandleComment 挂
	// 无超时的 Background ctx，单条评论理论可达 30 分钟级且无上界（v3.8.0）。
	CommentTimeout time.Duration
	// CommentConcurrency 评论链路全局并发上限（ARGUS_COMMENT_CONCURRENCY，默认 2）。
	// 此前评论链路无资源闸（对比任务链路有信号量+熔断+预算三重），评论风暴 =
	// 无界 goroutine × 无界 LLM 调用（v3.8.0）。满载时新评论收到"忙"回复。
	CommentConcurrency int
	// TaskMaxRetries failed 任务自动重试上限（ARGUS_TASK_MAX_RETRIES，默认 2；
	// 0 = 禁用）。指数退避 5m×3^attempt（封顶 1h），超限后等待新事件或手工重审。
	TaskMaxRetries int
	// WorkerGrace 停机时 worker 排水宽限（ARGUS_WORKER_GRACE，Go duration；默认 60s）。
	// 与典型任务时长（compliance 600s 级）的匹配由部署方权衡：宽限越长发布打断越少。
	WorkerGrace time.Duration
	// MetricsAuth /metrics 挂 Bearer 鉴权（ARGUS_METRICS_AUTH，默认关闭）。
	// 开启后 prometheus 抓取需带 token（与 APIToken 相同）；内网直抓部署保持关闭。
	MetricsAuth bool

	// EvalResultsPath golden 评测结果文件（ARGUS_EVAL_RESULTS_PATH，默认
	// eval/results/latest.json）：eval-real 离线落盘，服务侧周期加载暴露
	// argus_eval_* gauge；文件不存在属常态（该部署未跑过评测），静默跳过。
	EvalResultsPath string

	// WebhookMaxBody Webhook 请求体上限（字节）；默认 1MB。
	// Hertz WithMaxRequestBodySize + Content-Length 双重防护。
	WebhookMaxBody int

	// SinkURL 审查结果上报端点（ARGUS_SINK_URL；非空即启用，push 模型旁路）。
	// 审查完成后向该 URL POST 结果契约（Bearer ARGUS_SINK_TOKEN），供
	// review-service「Argus 审查」页承接展示。上报失败只告警，不阻断审查主流程。
	SinkURL string
	// SinkToken 上报端点的 Bearer 凭证（ARGUS_SINK_TOKEN；与 review-service
	// /api/v1/* 鉴权同源，配置为独立 ingest token）。空 = 不带 Authorization 头。
	SinkToken string

	// SDKCallTimeout 单次 git-platform-sdk 调用的兜底超时（ARGUS_SDK_TIMEOUT）。
	// 入参 ctx 无 deadline 时启用；0 = 默认 120s；负值 = 关闭兜底（调用随 ctx 语义）。
	// 背景：弱网/VPN 下 TCP 连接可能停滞且不触发传输层错误，无超时的调用会永久挂起
	// （v3.5.5 真机实测：poster 总结评论更新在停滞连接上无限等待）。
	SDKCallTimeout time.Duration

	// 任务保留天数（运维加固）：超期已终结任务（done/failed/stale）启动+每日清理。
	// 0 = 永久保留（默认，行为与 v3.0 一致）。
	TaskRetentionDays int

	// Debug 留存阶段产物（R1 spec/R2 摘要/R3 原始输出）到任务记录，GET /tasks/:id 可查。
	Debug bool

	// SkillMode skill 注入模式（ARGUS_SKILL_MODE）：
	//   inject（默认）= 全文拼进 R3 系统提示词（现状，skill 少/小时最优）；
	//   tool          = 决策使用（渐进披露）——提示词只注入可用清单（名称+描述），
	//                   R3 agent 经 use_skill 工具按需加载全文（skill 多/大时用）。
	SkillMode string

	// MCP 闲置连接回收（v0.10.18 连接租约面；默认关闭）。闲置时钟 = 建连/
	// 取工具/探活成功。MaxIdle 必须 ≫ 最大工具超时，否则在飞调用被误回收。
	MCPIdleReapInterval time.Duration // ARGUS_MCP_IDLE_REAP_INTERVAL，检查周期（启用时缺省 5m；⚠️ 仅 MAX_IDLE>0 时生效，单配本项静默无效）
	MCPIdleReapMaxIdle  time.Duration // ARGUS_MCP_IDLE_REAP_MAX_IDLE，闲置阈值（0 = 关闭）

	// MemoryDir repo 评审记忆目录（ARGUS_MEMORY_DIR，默认 "memory"）：dream
	// 周期把低置信模式导出为事实文件，R3 注入索引（感知面）。dream 未启用时
	// 目录保持为空 = 无注入，零开销。
	MemoryDir string

	// RepoGuideOff 关闭仓库级评审规范加载（ARGUS_REPO_GUIDE_OFF=1；默认读取
	// 被评审仓库的 .argus/REVIEW.md @ head 注入 R3/R4，100KB 截断 + 提醒标签转义）。
	RepoGuideOff bool

	// SkillContentCap use_skill 工具全文单次加载上限（ARGUS_SKILL_CONTENT_CAP，
	// 字节；缺省 100_000 与 agentkit 缺省一致，≤0 = 不限——与 RenderList 的
	// 20K 清单预算构成两级纪律）。
	SkillContentCap int

	// ExternalToolCallLimit 外部 MCP 工具单任务调用上限（ARGUS_EXTERNAL_TOOL_CALL_LIMIT；
	// 缺省 5，v3.41.0 起可配——此前硬编码，破坏性工具可再配低）。≤0 回退 5。
	ExternalToolCallLimit int

	// MemoryIndexBudget 评审记忆索引注入预算（ARGUS_MEMORY_INDEX_BUDGET，rune；
	// 缺省 2000，超限截断注明。≤0 用包内缺省）。
	MemoryIndexBudget int

	// OCR delegate 委托前置（design §2 委托模式；ARGUS_OCR_BIN 非空时启用）：
	// dispatch 前在工作副本执行 `ocr delegate preview/rule`（确定性子命令，OCR 端
	// 零 LLM、零凭证），产物注入 R4 审查提示词。BIN 须为绝对路径（§8 供应链：
	// 运行期只执行配置所指二进制）。任何失败告警降级，不阻断审查。
	OcrBin string
	// OcrTimeout OCR delegate 单条命令硬上限（ARGUS_OCR_TIMEOUT；0 = 默认 90s）。
	OcrTimeout time.Duration
	// OcrHistoryDir OCR delegate 产物留档目录（ARGUS_OCR_HISTORY_DIR；空 = 不留档）。
	// 每次前置把 preview/rule 原始 JSON 与渲染 section 落盘一份，供人工回溯 OCR
	// 原文（argus DB 只存合并后 findings）。deploy 挂到持久卷（/data/ocr-history）。
	OcrHistoryDir string

	// SAST 确定性前置扫描（v3.15.0 P0-3）：gitleaks/semgrep 在工作副本上执行，
	// findings 直接并入 Merger（零 LLM）。全部来自服务级 env，默认全空 = 关闭
	// （零依赖部署形态不变）；仓库配置不可启用/替换（§8 供应链红线）。
	SASTGitleaksBin   string        // ARGUS_SAST_GITLEAKS_BIN
	SASTSemgrepBin    string        // ARGUS_SAST_SEMGREP_BIN
	SASTSemgrepConfig string        // ARGUS_SAST_SEMGREP_CONFIG（--config 值）
	SASTTimeout       time.Duration // ARGUS_SAST_TIMEOUT（0 = 120s）

	// Codegraph Go AST 符号索引（v3.15.0 P0-1）：工作副本上以 go/parser 建
	// get_symbol/get_references 工具与变更触达符号注入（零 LLM）。
	// 变更不含 Go 文件或无工作副本池时自动跳过。ARGUS_CODEGRAPH=0 关闭。
	Codegraph bool

	// CodeSearch 工作副本全文检索（v3.84.0）：为 R3/R4 审查 agent 挂 search_code
	// 受控 grep 工具——跨文件词法线索（字符串/配置/非 Go 代码），零 LLM、
	// 命中数/单文件/输出三重封顶。无纳入文件或无工作副本池时自动跳过。
	// ARGUS_CODESEARCH=0 关闭。
	CodeSearch bool

	// RelatedRepos 跨仓契约快照（v3.90.0）：允许 .reviewagents.yaml related 段
	// 生效——审查时拉取关联仓契约文件注入 R4。服务级总门（仓库声明只是必要
	// 条件）：跨仓读取扩大数据访问面，由部署方显式授权。ARGUS_RELATED_REPOS=1 开。
	RelatedRepos bool

	// TestGen 沙箱验证（v3.22.0）：@argus testgen 产物的工作副本编译验证。
	// 纪律：argus 自身绝不直接执行待审代码——VerifyCmd 是部署方声明的隔离执行
	// 包装命令（模板含 {dir} 占位符，如
	//   "docker run --rm -v {dir}:/w -w /w golang:1.24 go test ./..."
	// ），隔离责任（网络/文件系统/资源限制）在包装命令侧，与 cli 插件 §8 供应链
	// 同构。空 = 关闭（缺省），testgen 回帖保持「未经编译验证」声明。
	TestGenVerifyCmd string
	// FixVerify 沙箱修复验证（v3.74.0）：审查意见 suggested_fix 的工作副本
	// 应用+编译验证，通过者在回帖标「✅ 沙箱验证通过」。空 = 功能停用
	//（findings 保持「未经编译验证」，零行为变更）。
	FixVerifyCmd string
	// FixPush 修复 push 环（v3.74.1，ARGUS_FIX_PUSH=1 显式 opt-in）：验证通过的
	// 修复建议自动 commit 并 push 到 PR 源分支。默认关闭——bot 直接写用户分支
	// 是信任面决策；依赖 FixVerifyCmd（没有验证环就没有 push 环）。
	FixPush bool
	// APITokens 多 token 分级（v3.87.0，ARGUS_API_TOKENS）：逗号分隔的
	// "token:scope" 列表，scope ∈ read|ci|admin（admin ⊇ ci ⊇ read）。旧
	// ARGUS_APITOKEN 等价一条 admin 条目，两者可并存；两者全空 = 🔒 路由
	// 503 fail-closed（语义不变）。
	APITokens []APITokenScope
	// FixAgent @argus fix 修复命令的编码 agent（v3.85.0，ARGUS_FIX_AGENT，空 =
	// 关闭）。值为 acpx agent 名（claude/codex/gemini 族——fixer 需要 workspace-
	// write 沙箱档，无沙箱旗标的 agent 显式拒绝）。修复在隔离沙箱执行，验证走
	// FixVerifyCmd，不自动 push。
	FixAgent string
	// FixTimeout 修复命令 agent 硬上限（ARGUS_FIX_TIMEOUT，0 = 10m）。
	FixTimeout time.Duration
	// FixVerifyTimeout 包装命令硬上限（ARGUS_FIX_VERIFY_TIMEOUT，0 = 3m）。
	FixVerifyTimeout time.Duration
	// TestGenVerifyTimeout 包装命令硬上限（ARGUS_TESTGEN_VERIFY_TIMEOUT，0 = 5m）。
	TestGenVerifyTimeout time.Duration

	// Milvus 向量检索配置（ARGUS_MILVUS_ADDR 非空时启用）。
	MilvusAddr       string // Milvus 地址，如 "localhost:19530"
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

// Load 从环境变量读取全部配置（已加载 .env 后调用）。
func Load() *Config {
	cfg := &Config{
		Addr:                   envStr("ARGUS_ADDR", ":8899"),
		DBDSN:                  envStr("ARGUS_DB_DSN", "argus.db"),
		APIToken:               os.Getenv("ARGUS_APITOKEN"),
		APITokens:              ParseAPITokens(os.Getenv("ARGUS_API_TOKENS")),
		BotUsername:            envStr("ARGUS_BOT_USERNAME", "argus-bot"),
		SkipDraft:              envBool("ARGUS_SKIP_DRAFT", true),
		MaxConcurrentTasks:     envInt("ARGUS_MAX_CONCURRENT_TASKS", 2),
		PluginMaxConcurrent:    envInt("ARGUS_PLUGIN_MAX_CONCURRENT", 2),
		MaxDiffChars:           envInt("ARGUS_MAX_DIFF_CHARS", 0),
		TaskTokenBudget:        envInt("ARGUS_TASK_TOKEN_BUDGET", 0),
		ReportMeta:             envBool("ARGUS_REPORT_META", true),
		PluginsFile:            envStr("ARGUS_PLUGINS", "plugins.yaml"),
		SkillsDir:              envStr("ARGUS_SKILLS_DIR", "skills"),
		WorkRoot:               envStr("ARGUS_WORK_ROOT", "/tmp/argus-work"),
		KnowledgeDir:           envStr("ARGUS_KNOWLEDGE_DIR", "knowledge"),
		OcrBin:                 envStr("ARGUS_OCR_BIN", ""),
		OcrTimeout:             envDuration("ARGUS_OCR_TIMEOUT", 0),
		OcrHistoryDir:          envStr("ARGUS_OCR_HISTORY_DIR", ""),
		SASTGitleaksBin:        os.Getenv("ARGUS_SAST_GITLEAKS_BIN"),
		SASTSemgrepBin:         os.Getenv("ARGUS_SAST_SEMGREP_BIN"),
		SASTSemgrepConfig:      os.Getenv("ARGUS_SAST_SEMGREP_CONFIG"),
		SASTTimeout:            envDuration("ARGUS_SAST_TIMEOUT", 0),
		Codegraph:              envBool("ARGUS_CODEGRAPH", true),
		CodeSearch:             envBool("ARGUS_CODESEARCH", true),
		RelatedRepos:           envBool("ARGUS_RELATED_REPOS", false),
		TestGenVerifyCmd:       os.Getenv("ARGUS_TESTGEN_VERIFY_CMD"),
		FixVerifyCmd:           os.Getenv("ARGUS_FIX_VERIFY_CMD"),
		FixPush:                os.Getenv("ARGUS_FIX_PUSH") == "1",
		FixAgent:               os.Getenv("ARGUS_FIX_AGENT"),
		FixTimeout:             envDuration("ARGUS_FIX_TIMEOUT", 0),
		FixVerifyTimeout:       envDuration("ARGUS_FIX_VERIFY_TIMEOUT", 0),
		TestGenVerifyTimeout:   envDuration("ARGUS_TESTGEN_VERIFY_TIMEOUT", 0),
		Platforms:              map[string]PlatformConfig{},
		PollInterval:           envDuration("ARGUS_POLL_INTERVAL", 0),
		PollRepos:              os.Getenv("ARGUS_POLL_REPOS"),
		SDKCallTimeout:         envDuration("ARGUS_SDK_TIMEOUT", 0),
		TaskRetentionDays:      envInt("ARGUS_TASK_RETENTION_DAYS", 30),
		RepoMonthlyTokenBudget: envInt("ARGUS_REPO_MONTHLY_TOKEN_BUDGET", 0),
		LLMCostPromptPer1K:     envFloat("ARGUS_LLM_COST_PROMPT_PER_1K", 0),
		LLMCostCompletionPer1K: envFloat("ARGUS_LLM_COST_COMPLETION_PER_1K", 0),
		StageModelR1:           stageOverride("ARGUS_MODEL_R1"),
		StageModelR2:           stageOverride("ARGUS_MODEL_R2"),
		StageModelR3:           stageOverride("ARGUS_MODEL_R3"),
		StageModelR35:          stageOverride("ARGUS_MODEL_R35"),
		StageModelReviewer:     stageOverride("ARGUS_REVIEWER_MODEL"),
		StageModelQA:           stageOverride("ARGUS_QA_MODEL"),
		StageModelRouter:       stageOverride("ARGUS_ROUTER_MODEL"),
		ReflectRounds:          envInt("ARGUS_REFLECTION_ROUNDS", 0),
		ReactMaxIterations:     envInt("ARGUS_REACT_MAX_ITERATIONS", 12),
		CompletenessPolicy:     normalizeCompletenessPolicy(envStr("ARGUS_COMPLETENESS_POLICY", "annotate")),
		CompletenessRetry:      envBool("ARGUS_COMPLETENESS_RETRY", false),
		ReflectEvidence:        normalizeEvidenceMode(envStr("ARGUS_REFLECTION_EVIDENCE", "off")),
		RegretLearning:         envBool("ARGUS_REGRET_LEARNING", false),
		RegretMinSamples:       envInt("ARGUS_REGRET_MIN_SAMPLES", 3),
		RegretSoftFloor:        envFloat("ARGUS_REGRET_SOFT_FLOOR", 0.35),
		RegretHardFloor:        envFloat("ARGUS_REGRET_HARD_FLOOR", 0.20),
		RegretInjectFloor:      envFloat("ARGUS_REGRET_INJECT_FLOOR", 0.35),
		RegretInjectTopN:       envInt("ARGUS_REGRET_INJECT_TOP_N", 10),
		DreamInterval:          envDuration("ARGUS_DREAM_INTERVAL", 0),
		HealthReportInterval:   envDuration("ARGUS_HEALTH_REPORT_INTERVAL", 0),
		CommentRouter:          envBool("ARGUS_COMMENT_ROUTER", false),
		CommentTimeout:         envDuration("ARGUS_COMMENT_TIMEOUT", 10*time.Minute),
		CommentConcurrency:     envInt("ARGUS_COMMENT_CONCURRENCY", 2),
		TaskMaxRetries:         envInt("ARGUS_TASK_MAX_RETRIES", 2),
		WorkerGrace:            envDuration("ARGUS_WORKER_GRACE", 60*time.Second),
		MetricsAuth:            envBool("ARGUS_METRICS_AUTH", false),
		EvalResultsPath:        envStr("ARGUS_EVAL_RESULTS_PATH", "eval/results/latest.json"),
		RouterMinConfidence:    envFloat("ARGUS_ROUTER_MIN_CONFIDENCE", 0.6),
		LLMRateLimit:           envFloat("ARGUS_LLM_RATE_LIMIT", 0),
		LLMRateBurst:           envInt("ARGUS_LLM_RATE_BURST", 0),
		WebhookMaxBody:         envInt("ARGUS_WEBHOOK_MAX_BODY", 1<<20),
		SinkURL:                envStr("ARGUS_SINK_URL", ""),
		SinkToken:              envStr("ARGUS_SINK_TOKEN", ""),
		OctopusURL:             os.Getenv("ARGUS_OCTOPUS_URL"),
		OctopusChannelID:       int64(envInt("ARGUS_OCTOPUS_CHANNEL_ID", 0)),
		OctopusUser:            envStr("ARGUS_OCTOPUS_USER", "argus-bot"),
		OctopusToken:           os.Getenv("ARGUS_OCTOPUS_TOKEN"),
		Debug:                  envBool("ARGUS_DEBUG", false),
		SkillMode:              envStr("ARGUS_SKILL_MODE", "inject"),
		MCPIdleReapInterval:    envDuration("ARGUS_MCP_IDLE_REAP_INTERVAL", 5*time.Minute),
		MCPIdleReapMaxIdle:     envDuration("ARGUS_MCP_IDLE_REAP_MAX_IDLE", 0),
		MemoryDir:              envStr("ARGUS_MEMORY_DIR", "memory"),
		RepoGuideOff:           envBool("ARGUS_REPO_GUIDE_OFF", false),
		SkillContentCap:        envInt("ARGUS_SKILL_CONTENT_CAP", 100_000),
		ExternalToolCallLimit:  envInt("ARGUS_EXTERNAL_TOOL_CALL_LIMIT", 5),
		MemoryIndexBudget:      envInt("ARGUS_MEMORY_INDEX_BUDGET", 2000),
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

			FallbackBaseURL:      os.Getenv("OPENAI_FALLBACK_BASE_URL"),
			FallbackAPIKey:       os.Getenv("OPENAI_FALLBACK_API_KEY"),
			FallbackModel:        os.Getenv("OPENAI_FALLBACK_MODEL"),
			FallbackContextToken: envInt("OPENAI_FALLBACK_CONTEXT_TOKENS", 128_000),

			LLMResilienceRetries:   envInt("ARGUS_LLM_RESILIENCE_RETRIES", 2),
			LLMResilienceBaseDelay: envDuration("ARGUS_LLM_RESILIENCE_BASE_DELAY", 2*time.Second),
		},
	}
	for _, p := range []string{"gitea", "github", "gitlab", "gitee"} {
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
		return fmt.Errorf("未配置任何 git 平台凭证（ARGUS_GITEA_TOKEN / ARGUS_GITHUB_TOKEN / ARGUS_GITLAB_TOKEN / ARGUS_GITEE_TOKEN 至少一项，或配置 GitHub App）")
	}
	return nil
}

// WarnLogger WarnOnly 的日志口（与 logx.Logger 解耦，测试可注入）。
type WarnLogger interface{ Warn(msg string, kv ...any) }

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
