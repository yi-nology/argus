package plugin

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"
	oekitconfig "github.com/oekit/core/config"
	"gopkg.in/yaml.v3"
)

// ---------- plugins.yaml（Hub 全局配置，唯一能声明 cli 二进制/remote endpoint 的地方）----------

// PluginsFile 文件根结构（design §12）。
type PluginsFile struct {
	Agents []AgentDecl `yaml:"agents"`
	// MCPServers MCP server 连接声明（design §9.2 方向 A：外部工具 → agent 的"手"）。
	MCPServers []MCPServerDecl `yaml:"mcpServers"`
	// Tools 工具白名单：哪些 server 的哪些工具暴露给 agent 工具表。
	Tools []ToolSpecDecl `yaml:"tools"`
	// Squads 交叉审查编队：触发条件命中时把 members 追加进本轮分派
	//（高危变更双专家交叉，Merger 按"仅一方报告"标注分歧）。
	// mode=consensus（v3.4）时成员不进 fan-out，改走黑板共识（互看轮转、收敛即止）。
	Squads []SquadDecl `yaml:"squads"`
}

// 编队执行模式（SquadDecl.Mode）。
const (
	// SquadModeIndependent 默认模式：成员互盲独立审查，分歧由 Merger 事后标注。
	SquadModeIndependent = ""
	// SquadModeConsensus 黑板共识模式：成员按轮互看共享黑板（oagentkit/agent/blackboard），
	// 一整轮无人补充 = 共识达成；上限 consensusMaxRounds 轮。
	SquadModeConsensus = "consensus"
)

// SquadDecl 编队声明：paths/labels 任一命中即触发（都空 = 永不触发，配置告警）。
type SquadDecl struct {
	Name    string   `yaml:"name"`
	Mode    string   `yaml:"mode"`   // ""（默认，互盲独立）| "consensus"（黑板共识）
	Paths   []string `yaml:"paths"`  // glob（.gitignore 语义）
	Labels  []string `yaml:"labels"` // PR 标签任一命中
	Members []string // 插件名（须已注册；启用中才会追加）
}

// MCPServerDecl 单个 MCP server 连接声明。
type MCPServerDecl struct {
	Name         string            `yaml:"name"`
	Command      []string          `yaml:"command"`      // stdio 传输
	Env          []string          `yaml:"env"`          // stdio 子进程环境白名单
	URL          string            `yaml:"url"`          // streamable http 传输
	Headers      map[string]string `yaml:"headers"`      // 请求头（值支持 ${VAR} 环境引用，如 Bearer token）
	Timeout      string            `yaml:"timeout"`      // "30s" 写法
	ProbeTimeout string            `yaml:"probeTimeout"` // 探活独立预算（"5s" 写法；缺省 5s，不误吃连接级 timeout）
}

// ToolSpecDecl 工具白名单条目：tools: [{server: docs, allow: [search_docs]}]。
type ToolSpecDecl struct {
	Server string   `yaml:"server"`
	Allow  []string `yaml:"allow"`
}

// AgentDecl 单插件声明。timeout 支持 "120s" 等写法。
type AgentDecl struct {
	Name     string      `yaml:"name"`
	Kind     string      `yaml:"kind"`
	Protocol string      `yaml:"protocol"`
	Version  string      `yaml:"version"`
	Skills   []string    `yaml:"skills"`
	Needs    []string    `yaml:"needs"`
	Triggers TriggerDecl `yaml:"triggers"`
	Cost     string      `yaml:"cost"`
	Timeout  string      `yaml:"timeout"`

	// 信任边界（§3.4 红线 3）：lockRepoConfig=true 的插件仓库配置不可关闭/排除。
	LockRepoConfig bool     `yaml:"lockRepoConfig"`
	SkillRefs      []string `yaml:"skillRefs"`
	Enabled        *bool    `yaml:"enabled"`

	// ReviewN best-of-N 同质采样次数（v3.10.0；0/1 = 单次，钳制 ≤ MaxReviewN=5）。
	// 高危路径 opt-in：同一插件并发跑 N 次、复现 ≥2 的发现升严重度并标注 k/n。
	// 成本 ×N（token 记账逐采样累计，/usage 可见）。
	ReviewN int `yaml:"reviewN"`

	// kind=cli
	Cmd  []string `yaml:"cmd"`
	Mode string   `yaml:"mode"` // full | delegate
	// Env cli 子进程的额外环境变量白名单（按名从 Argus 进程环境透传）。
	// 缺省只给最小环境（PATH/HOME/TMPDIR/代理/CA），绝不继承 ARGUS_* / OPENAI_* 等密钥——
	// cli 在不可信工作树里执行，全量继承环境等于把凭证交给待审代码。
	Env []string `yaml:"env"`

	// kind=remote
	Endpoint string `yaml:"endpoint"`
	// Tool mcp 协议要调用的工具名（缺省 review；review-service 用 create_review）。
	Tool string `yaml:"tool"`
	// AuthTokenEnv Bearer 共享密钥的环境变量名（如 OCRSHIM_TOKEN；值从 argus 进程
	// 环境按名解析，声明进 yaml、凭证留在 env——与 cli 的 Env 同范式）。设置后
	// 请求带 Authorization: Bearer <值>；声明了但环境变量未设置 → 启动期停用该插件。
	// 与 ocrshim 的 -auth-token/OCRSHIM_TOKEN 配对（非 loopback sidecar 建议开启）。
	AuthTokenEnv string `yaml:"auth_token_env"`

	// kind=acpx：CLI 编码 agent 名（oagentkit/tools/acpx 注册表词表：
	// claude/zcode/codex/opencode/minimax/kimi/gemini/qwen/mimo）。
	Agent string `yaml:"agent"`
}

// TriggerDecl yaml 侧触发声明。
type TriggerDecl struct {
	Paths  []string `yaml:"paths"`
	Labels []string `yaml:"labels"`
	Events []string `yaml:"events"`
}

// LoadPluginsFile 读取并解析 plugins.yaml。
// v3.159.0：严格解码——自有配置的未知键拒载而非静默忽略（插件作者把 skills
// 拼成 skils 时，旧语义=该插件悄悄不生效且无任何线索；严格语义=启动期响亮报错）。
// v3.160.0：oekit core v0.11.0 落地字节级/任意路径入口（#25），从 yaml.v3
// KnownFields 切 config.LoadFile(path, WithStrict())——错误面升级为嵌套路径 +
// 该层合法键清单 + 拼写建议（「是否想写 skills?」），三入口同一条解码线。
// 敌对面 .reviewagents.yaml 保持宽容解码不变（攻击者不能靠配置错误面搅黄审查）。
func LoadPluginsFile(path string) (*PluginsFile, error) {
	var pf PluginsFile
	if err := oekitconfig.LoadFile(path, &pf, oekitconfig.WithStrict()); err != nil {
		return nil, fmt.Errorf("解析 %s（严格模式：未知键拒载，检查拼写）: %w", path, err)
	}
	return &pf, nil
}

// ParseTimeout 解析声明里的 timeout；空串返回 0（由适配器给默认值）。
func (d *AgentDecl) ParseTimeout() (time.Duration, error) {
	if d.Timeout == "" {
		return 0, nil
	}
	return time.ParseDuration(d.Timeout)
}

// ---------- .reviewagents.yaml（业务仓库配置：只能"选"不能"增"）----------

// RepoConfigFile 仓库侧文件（design §3.4：按敌对输入对待）。
type RepoConfigFile struct {
	Use      []string          `yaml:"use"`
	Disable  []string          `yaml:"disable"`
	Routing  []RoutingRule     `yaml:"routing"`
	Review   ReviewSettings    `yaml:"review"`   // v3.15.0 评审行为开关（路径过滤/严重度阈值/评论上限/附加指令）
	Describe DescribeSettings  `yaml:"describe"` // v3.15.0 @argus describe 的标签词表
	Related  []RelatedContract `yaml:"related"`  // v3.90.0 跨仓契约快照声明
}

// ReviewSettings 仓库级评审行为配置（v3.15.0 主流差距收口：对标 .coderabbit.yaml
// 的 path filters / review profiles）。全部字段缺省 = 维持服务级默认行为。
type ReviewSettings struct {
	// ExcludePaths 额外噪声路径（glob，.gitignore 语义）：命中文件不进 diff 文本，
	// 元信息保留。与服务级硬编码噪声表叠加（用户配置只能再排除，不能放行内置噪声）。
	ExcludePaths []string `yaml:"exclude_paths"`
	// IncludePaths 审查白名单（glob）：非空时未命中的文件一律排除（Exclude 之上再收紧）。
	IncludePaths []string `yaml:"include_paths"`
	// MinSeverity 报告与裁决的最低严重度（low|medium|high）：低于阈值的意见不进
	// 报告、不参与 R5 裁决。空 = 不过滤（缺省）。high 意见任何阈值下都保留。
	MinSeverity string `yaml:"min_severity"`
	// MaxComments 单轮行内评论上限（0 = 不限，缺省）。超出部分按严重度保留 top N，
	// 其余仅在总结评论列出（降级注明）。钳制 ≤500。
	MaxComments int `yaml:"max_comments"`
	// ExtraInstructions 仓库附加审查指令（对标 Copilot custom instructions）：
	// 与 .argus/review-guidelines.md 合并注入 R4 提示词。≤4000 runes（截断）。
	ExtraInstructions string `yaml:"extra_instructions"`
}

// DescribeSettings @argus describe 命令的仓库级配置（v3.15.0）。
type DescribeSettings struct {
	// Labels 建议标签词表：非空时 describe 只从词表选；空 = 模型自拟 type/area 建议。
	Labels []LabelDecl `yaml:"labels"`
}

// LabelDecl 建议标签声明。
type LabelDecl struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// RoutingRule 条件启用：paths/labels 任一命中即把 use 里的插件加入本 PR。
type RoutingRule struct {
	Paths  []string `yaml:"paths"`
	Labels []string `yaml:"labels"`
	Use    []string `yaml:"use"`
}

// ParseRepoConfig 解析仓库配置并转为运行时形态（输入视为不可信数据：仅取词表引用，长度收紧）。
func ParseRepoConfig(data []byte) (*RepoConfig, error) {
	var f RepoConfigFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析 .reviewagents.yaml: %w", err)
	}
	const maxList = 64
	if len(f.Use) > maxList || len(f.Disable) > maxList || len(f.Routing) > maxList {
		return nil, fmt.Errorf(".reviewagents.yaml 列表超长（>%d），拒绝加载", maxList)
	}
	for i := range f.Routing {
		if len(f.Routing[i].Use) > maxList {
			return nil, fmt.Errorf(".reviewagents.yaml routing[%d].use 超长", i)
		}
	}
	review, err := sanitizeReviewSettings(f.Review, maxList)
	if err != nil {
		return nil, err
	}
	if len(f.Describe.Labels) > maxList {
		return nil, fmt.Errorf(".reviewagents.yaml describe.labels 超长（>%d），拒绝加载", maxList)
	}
	related, err := sanitizeRelatedContracts(f.Related, maxList)
	if err != nil {
		return nil, err
	}
	return &RepoConfig{
		Use: f.Use, Disable: f.Disable, Routing: f.Routing,
		Review: review, Describe: f.Describe, Related: related, Loaded: true,
	}, nil
}

// related 段硬上限（解析期与运行期共用；调整须过评测）。
const (
	maxRelatedContracts = 3
	maxRelatedPathsEach = 8
)

// sanitizeRelatedContracts related 段敌对输入钳制（v3.90.0）：仓数/路径数/长度
// 硬上限，repo 形状必须 owner/name，路径拒绝绝对形态与目录穿越。非法整份拒绝
// ——LoadRepoConfig 侧降级为无配置+告警，不会炸任务。
func sanitizeRelatedContracts(cs []RelatedContract, maxList int) ([]RelatedContract, error) {
	const maxContractLen = 256
	if len(cs) > maxRelatedContracts {
		return nil, fmt.Errorf(".reviewagents.yaml related 仓数超上限（>%d），拒绝加载", maxRelatedContracts)
	}
	for i, c := range cs {
		parts := strings.Split(strings.TrimSpace(c.Repo), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf(".reviewagents.yaml related[%d].repo 形状须为 owner/name: %q", i, c.Repo)
		}
		if len(c.Repo) > maxContractLen || len(c.Ref) > maxContractLen {
			return nil, fmt.Errorf(".reviewagents.yaml related[%d] repo/ref 超长", i)
		}
		if len(c.Paths) == 0 {
			return nil, fmt.Errorf(".reviewagents.yaml related[%d].paths 为空（声明了关联仓却不给契约文件）", i)
		}
		if len(c.Paths) > maxRelatedPathsEach {
			return nil, fmt.Errorf(".reviewagents.yaml related[%d].paths 超上限（>%d）", i, maxRelatedPathsEach)
		}
		for _, p := range c.Paths {
			if p == "" || len(p) > maxContractLen {
				return nil, fmt.Errorf(".reviewagents.yaml related[%d].paths 含空/超长路径", i)
			}
			clean := path.Clean(p)
			if path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") {
				return nil, fmt.Errorf(".reviewagents.yaml related[%d].paths 路径越界: %q", i, p)
			}
		}
	}
	return cs, nil
}

// sanitizeReviewSettings review 段敌对输入钳制：glob 数量/长度上限、severity 词表
// 校验（非法整份拒绝——LoadRepoConfig 侧会降级为无配置+告警，不会炸任务）、
// max_comments 封顶、extra_instructions 截断。
func sanitizeReviewSettings(r ReviewSettings, maxList int) (ReviewSettings, error) {
	const maxGlobLen = 256
	for _, fld := range [][]string{r.ExcludePaths, r.IncludePaths} {
		if len(fld) > maxList {
			return r, fmt.Errorf(".reviewagents.yaml review 路径列表超长（>%d），拒绝加载", maxList)
		}
		for _, p := range fld {
			if len(p) > maxGlobLen {
				return r, fmt.Errorf(".reviewagents.yaml review 路径超长（>%d 字节）", maxGlobLen)
			}
		}
	}
	if r.MinSeverity != "" {
		// 只收规范词表（比 reportutil.Normalize 严格）：别名宽容会让 "critcal" 类
		// 笔误静默落到意外档位，配置卫生优先
		switch strings.ToLower(strings.TrimSpace(r.MinSeverity)) {
		case reportutil.Low, reportutil.Medium, reportutil.High:
			r.MinSeverity = strings.ToLower(strings.TrimSpace(r.MinSeverity))
		default:
			return r, fmt.Errorf(".reviewagents.yaml review.min_severity=%q 非法（合法词表 low|medium|high）", r.MinSeverity)
		}
	}
	const maxCommentsCap = 500
	if r.MaxComments < 0 {
		r.MaxComments = 0
	} else if r.MaxComments > maxCommentsCap {
		r.MaxComments = maxCommentsCap
	}
	const maxExtraRunes = 4000
	if t, truncated := textutil.TruncRunes(strings.TrimSpace(r.ExtraInstructions), maxExtraRunes); truncated {
		r.ExtraInstructions = t + "\n…（截断）"
	} else {
		r.ExtraInstructions = strings.TrimSpace(r.ExtraInstructions)
	}
	return r, nil
}

// RelatedContract 跨仓契约快照声明（v3.90.0）：审查时拉取关联仓的契约面
// 文件（openapi/proto 等显式相对路径，无 glob——防路径枚举）注入审查上下文。
// 信任边界：服务级 ARGUS_RELATED_REPOS 开启才生效；解析期敌对输入整份拒绝
// （与 review 段同纪律），运行期单文件失败降级注明。
type RelatedContract struct {
	Repo  string   `yaml:"repo"`  // 同平台 owner/name
	Ref   string   `yaml:"ref"`   // 空 = 该仓默认分支
	Paths []string `yaml:"paths"` // 显式契约文件相对路径
}

// RepoConfig 解析后供 Dispatcher 使用的仓库配置（RepoConfigFile 的运行时形态）。
type RepoConfig struct {
	Use      []string
	Disable  []string
	Routing  []RoutingRule
	Review   ReviewSettings    // v3.15.0 评审行为开关
	Describe DescribeSettings  // v3.15.0 describe 标签词表
	Related  []RelatedContract // v3.90.0 跨仓契约快照（服务级开关门控）
	Loaded   bool              // 是否成功读到文件（读不到 = 全部默认启用）
}

// EmptyRepoConfig 未加载到仓库配置时的默认值。
func EmptyRepoConfig() *RepoConfig { return &RepoConfig{Loaded: false} }
