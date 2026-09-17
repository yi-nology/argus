package plugin

import (
	"fmt"
	"os"
	"time"

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
	// SquadModeConsensus 黑板共识模式：成员按轮互看共享黑板（agentkit/blackboard），
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
	Name    string            `yaml:"name"`
	Command []string          `yaml:"command"` // stdio 传输
	Env     []string          `yaml:"env"`     // stdio 子进程环境白名单
	URL     string            `yaml:"url"`     // streamable http 传输
	Headers map[string]string `yaml:"headers"` // 请求头（值支持 ${VAR} 环境引用，如 Bearer token）
	Timeout string            `yaml:"timeout"` // "30s" 写法
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

	// kind=acpx：CLI 编码 agent 名（agentkit/acpx 注册表词表：
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
func LoadPluginsFile(path string) (*PluginsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pf PluginsFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
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
	Use     []string      `yaml:"use"`
	Disable []string      `yaml:"disable"`
	Routing []RoutingRule `yaml:"routing"`
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
	return &RepoConfig{Use: f.Use, Disable: f.Disable, Routing: f.Routing, Loaded: true}, nil
}

// RepoConfig 解析后供 Dispatcher 使用的仓库配置（RepoConfigFile 的运行时形态）。
type RepoConfig struct {
	Use     []string
	Disable []string
	Routing []RoutingRule
	Loaded  bool // 是否成功读到文件（读不到 = 全部默认启用）
}

// EmptyRepoConfig 未加载到仓库配置时的默认值。
func EmptyRepoConfig() *RepoConfig { return &RepoConfig{Loaded: false} }
