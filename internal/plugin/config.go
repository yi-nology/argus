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

	// kind=cli
	Cmd  []string `yaml:"cmd"`
	Mode string   `yaml:"mode"` // full | delegate

	// kind=remote
	Endpoint string `yaml:"endpoint"`
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
