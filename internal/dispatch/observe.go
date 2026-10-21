package dispatch

import (
	"slices"
	"sort"
)

// 仓库侧生效状态词表（GET /agents 可观测，v3.0.1 评审项）。
const (
	RepoEffectDefault       = "default"         // 仓库配置不影响（常驻候选）
	RepoEffectSelected      = "selected"        // 被 use 显式选中
	RepoEffectRouted        = "routed"          // 不在 use，仅由 routing 规则条件选中
	RepoEffectExcludedByUse = "excluded_by_use" // use 白名单存在且未含它（也无 routing 引用）
	RepoEffectDisabled      = "disabled"        // 被 disable（非锁定）
	RepoEffectLocked        = "locked"          // lockRepoConfig：仓库配置不可影响
)

// PluginRepoStatus 单插件在仓库配置下的生效状态。
type PluginRepoStatus struct {
	Name     string
	Effect   string
	RoutedBy []int // 被哪些 routing 规则引用
	Note     string
}

// ObserveRepoConfig 计算仓库配置对注册表的可观测视图（幂等，不改变注册表状态）。
// 先跑 ResolveRepoConfig 做信任边界规范化（未知名剔除/lock 保护），再逐一判定生效状态。
// 追加参数：resolveWarns 透出给调用方展示。
func ObserveRepoConfig(reg *Registry, rc *RepoConfig) (statuses []PluginRepoStatus, warnings []string) {
	if rc == nil || !rc.Loaded {
		// 无仓库配置：全部 default
		for _, r := range reg.Snapshot() {
			statuses = append(statuses, PluginRepoStatus{
				Name:   r.Manifest.Name,
				Effect: RepoEffectDefault,
			})
		}
		return statuses, nil
	}
	warnings = ResolveRepoConfig(rc, reg)

	useSet := toSet(rc.Use)
	disableSet := toSet(rc.Disable)
	hasUse := len(rc.Use) > 0

	for _, r := range reg.Snapshot() {
		name := r.Manifest.Name
		st := PluginRepoStatus{Name: name}

		// routing 引用（先记，便于 selected/default 附注）
		for i, rule := range rc.Routing {
			if slices.Contains(rule.Use, name) {
				st.RoutedBy = append(st.RoutedBy, i)
			}
		}

		switch {
		case r.LockRepoConfig:
			st.Effect = RepoEffectLocked
			if disableSet[name] {
				// ResolveRepoConfig 已把锁定项从 disable 剔除并告警；此处标记备注
				st.Note = "仓库配置请求禁用，但受 lockRepoConfig 保护，已忽略"
			}
			if hasUse && !useSet[name] {
				st.Note = joinNote(st.Note, "不在 use 白名单，但 lockRepoConfig 优先，仍参与")
			}
		case disableSet[name]:
			st.Effect = RepoEffectDisabled
		case hasUse && useSet[name]:
			st.Effect = RepoEffectSelected
		case len(st.RoutedBy) > 0:
			st.Effect = RepoEffectRouted // 不在 use：参与路径 = routing 命中（dispatcher 同语义）
		case hasUse:
			st.Effect = RepoEffectExcludedByUse
		default:
			st.Effect = RepoEffectDefault
		}

		if len(st.RoutedBy) > 0 {
			st.Note = joinNote(st.Note, "被 routing 规则引用（命中条件时追加参与）")
		}
		statuses = append(statuses, st)
	}
	return statuses, warnings
}

func toSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, v := range list {
		m[v] = true
	}
	return m
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// SortStatuses 观测输出排序（名字序，稳定）。
func SortStatuses(sts []PluginRepoStatus) {
	sort.SliceStable(sts, func(i, j int) bool { return sts[i].Name < sts[j].Name })
}
