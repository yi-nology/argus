package dispatch

import (
	"fmt"
	"slices"
	"sync"
)

// Registration 注册表中一个插件的完整登记。
type Registration struct {
	Manifest AgentManifest
	Plugin   AgentPlugin

	// 配置侧属性（不在 manifest 里，因为是 Hub 管控面而非契约面）
	LockRepoConfig bool     // §3.4 红线 3：仓库配置不可关闭
	SkillRefs      []string // §9.1 方法论注入引用
	Enabled        bool     // plugins.yaml enabled（缺省 true）
}

// Registry 插件注册表。启动期由 plugins.yaml + 适配器构造填充；运行期只读（加锁防误用）。
type Registry struct {
	mu    sync.RWMutex
	items map[string]*Registration
	order []string
}

func NewRegistry() *Registry {
	return &Registry{items: map[string]*Registration{}}
}

// Register 注册插件；重名报错（启动期失败快速暴露配置错误）。
func (r *Registry) Register(reg *Registration) error {
	if reg == nil || reg.Plugin == nil {
		return fmt.Errorf("plugin: 注册 %q 缺少 AgentPlugin 实现", regName(reg))
	}
	if reg.Manifest.Name == "" {
		return fmt.Errorf("plugin: 注册缺少 name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.items[reg.Manifest.Name]; dup {
		return fmt.Errorf("plugin: 插件 %q 重复注册", reg.Manifest.Name)
	}
	r.items[reg.Manifest.Name] = reg
	r.order = append(r.order, reg.Manifest.Name)
	return nil
}

func regName(reg *Registration) string {
	if reg == nil || reg.Manifest.Name == "" {
		return "<nil>"
	}
	return reg.Manifest.Name
}

// Get 按名查找。
func (r *Registry) Get(name string) (*Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.items[name]
	return reg, ok
}

// Snapshot 按名称字母序返回全部登记（GET /agents 可观测；顺序确定性保证）。
func (r *Registry) Snapshot() []*Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := append([]string(nil), r.order...)
	slices.Sort(names)
	out := make([]*Registration, 0, len(names))
	for _, n := range names {
		out = append(out, r.items[n])
	}
	return out
}

// ResolveRepoConfig 把仓库的 .reviewagents.yaml 应用到注册表上，执行信任边界校验：
//   - 未知名：忽略并返回告警（§3.4 红线 2：只能"选"不能"增"）；
//   - disable/排除 lockRepoConfig 插件：无效并告警（红线 3）；
//   - routing 只允许引用已注册插件名（不新增 endpoint —— endpoint 只能来自 Hub 全局配置）。
func ResolveRepoConfig(rc *RepoConfig, reg *Registry) (warnings []string) {
	if rc == nil || !rc.Loaded {
		return nil
	}
	known := func(names []string, ctx string) []string {
		var ok []string
		for _, n := range names {
			if _, exists := reg.Get(n); exists {
				ok = append(ok, n)
			} else {
				warnings = append(warnings, fmt.Sprintf("仓库配置 %s 引用未注册插件 %q，已忽略", ctx, n))
			}
		}
		return ok
	}
	rc.Use = known(rc.Use, "use")
	rc.Disable = known(rc.Disable, "disable")
	for i := range rc.Routing {
		rc.Routing[i].Use = known(rc.Routing[i].Use, fmt.Sprintf("routing[%d].use", i))
	}
	// 锁定插件从 disable 中剔除并告警
	var disable []string
	for _, n := range rc.Disable {
		if regst, ok := reg.Get(n); ok && regst.LockRepoConfig {
			warnings = append(warnings, fmt.Sprintf("插件 %q 受 lockRepoConfig 保护，仓库配置不得禁用，已忽略", n))
			continue
		}
		disable = append(disable, n)
	}
	rc.Disable = disable
	return warnings
}
