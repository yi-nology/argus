package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

func mkObsRegistry() *Registry {
	reg := NewRegistry()
	mk := func(name string, lock bool) *Registration {
		return &Registration{
			Manifest:       plugin.AgentManifest{Name: name, Kind: plugin.KindBuiltin, Skills: []string{plugin.SkillCodeReview}},
			Plugin:         nopAgentPlugin{},
			LockRepoConfig: lock,
			Enabled:        true,
		}
	}
	_ = reg.Register(mk("security", true))
	_ = reg.Register(mk("correctness", false))
	_ = reg.Register(mk("ocr", false))
	_ = reg.Register(mk("frontend-visual", false))
	return reg
}

type nopAgentPlugin struct{}

func (nopAgentPlugin) Manifest() plugin.AgentManifest { return plugin.AgentManifest{} }

func (nopAgentPlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, nil
}

// 验证各生效状态 + 信任边界告警进入可观测视图（v3.0.1 评审项锚点）。
func TestObserveRepoConfig(t *testing.T) {
	reg := mkObsRegistry()
	rc := &plugin.RepoConfig{
		Loaded:  true,
		Use:     []string{"requirement-compliance", "ocr"}, // 未知名 requirement-compliance → 告警
		Disable: []string{"security"},                      // 锁定 → 拒绝 + 告警
		Routing: []plugin.RoutingRule{{Paths: []string{"web/**"}, Use: []string{"frontend-visual"}}},
	}
	statuses, warnings := ObserveRepoConfig(reg, rc)
	byName := map[string]PluginRepoStatus{}
	for _, st := range statuses {
		byName[st.Name] = st
	}

	if got := byName["security"].Effect; got != RepoEffectLocked {
		t.Fatalf("security 应 locked，got %s", got)
	}
	if byName["security"].Note == "" {
		t.Fatal("security 的禁用被拒绝应有备注")
	}
	if got := byName["ocr"].Effect; got != RepoEffectSelected {
		t.Fatalf("ocr 应 selected，got %s", got)
	}
	if got := byName["correctness"].Effect; got != RepoEffectExcludedByUse {
		t.Fatalf("correctness 应 excluded_by_use，got %s", got)
	}
	if got := byName["frontend-visual"].Effect; got != RepoEffectRouted {
		t.Fatalf("frontend-visual 应 routed（不在 use，仅 routing 条件选中），got %s", got)
	}
	if len(byName["frontend-visual"].RoutedBy) != 1 || byName["frontend-visual"].RoutedBy[0] != 0 {
		t.Fatalf("frontend-visual 应被 routing[0] 引用: %+v", byName["frontend-visual"].RoutedBy)
	}
	// 告警：未知名（use 里的 requirement-compliance 未注册）+ 锁定禁用拒绝
	foundUnknown, foundLock := false, false
	for _, w := range warnings {
		if strings.Contains(w, "requirement-compliance") {
			foundUnknown = true
		}
		if strings.Contains(w, "security") {
			foundLock = true
		}
	}
	if !foundUnknown || !foundLock {
		t.Fatalf("告警缺失（未知名/锁定拒绝）: %v", warnings)
	}
	// 仓库配置在解析后应被规范化：未知名剔除、disable 中的锁定项剔除
	if strings.Contains(strings.Join(rc.Use, ","), "requirement-compliance") || strings.Contains(strings.Join(rc.Disable, ","), "security") {
		t.Fatalf("信任边界规范化未生效: use=%v disable=%v", rc.Use, rc.Disable)
	}
}

func TestObserveRepoConfigNotLoaded(t *testing.T) {
	reg := mkObsRegistry()
	statuses, warnings := ObserveRepoConfig(reg, plugin.EmptyRepoConfig())
	if len(warnings) != 0 || len(statuses) != 4 {
		t.Fatalf("无仓库配置应全 default 且无告警: %d %v", len(statuses), warnings)
	}
	for _, st := range statuses {
		if st.Effect != RepoEffectDefault {
			t.Fatalf("%s 应 default", st.Name)
		}
	}
}
