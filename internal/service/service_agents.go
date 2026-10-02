// service_agents.go —— Agent 可观测与 MCP 副驾数据面：agent 清单/仓库配置视图/任务阶段/findings/建议定位。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	api "argus/biz/model/api"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// ListAgents 插拔层可观测（🔒）：Registry 全部插件清单/状态（含 needs/triggers）。
// platform/owner/repo 三参全传时，附带该仓库 .reviewagents.yaml 的可观测视图：
// 配置原文（信任边界规范化后）+ 各插件生效状态 + 告警（§3.4 落到看得见的地方）。
func (s *Service) ListAgents(ctx context.Context, platform, owner, repo string) *api.ListAgentsResponse {
	resp := &api.ListAgentsResponse{}
	for _, reg := range s.Registry.Snapshot() {
		m := reg.Manifest
		dto := &api.AgentManifestDTO{
			Name:          m.Name,
			Kind:          kindToIDL(m.Kind),
			Skills:        m.Skills,
			Enabled:       reg.Enabled,
			Needs:         m.Needs.Strings(),
			TriggerPaths:  m.Triggers.Paths,
			TriggerLabels: m.Triggers.Labels,
		}
		if m.Cost != "" {
			dto.Cost = strPtr(m.Cost)
		}
		if m.Timeout > 0 {
			ms := m.Timeout.Milliseconds()
			dto.TimeoutMs = &ms
		}
		if m.Version != "" {
			dto.Version = strPtr(m.Version)
		}
		if m.Protocol != "" {
			p := protocolToIDL(m.Protocol)
			dto.Protocol = &p
		}
		resp.Agents = append(resp.Agents, dto)
	}

	if platform != "" && owner != "" && repo != "" {
		resp.RepoConfig = s.repoConfigView(ctx, platform, owner, repo)
	}
	return resp
}

// repoConfigView 拉取并解析仓库配置为可观测视图；失败降级为 loaded=false + 告警（不 5xx）。
func (s *Service) repoConfigView(ctx context.Context, platform, owner, repo string) *api.RepoConfigView {
	view := &api.RepoConfigView{Loaded: false}
	addWarn := func(msg string) {
		view.Warnings = append(view.Warnings, msg)
	}
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		addWarn("平台未接入: " + platform)
		return view
	}
	repoInfo, err := s.SDK.GetRepo(ctx, platform, owner, repo)
	if err != nil {
		addWarn("获取仓库失败: " + err.Error())
		return view
	}
	rc, err := s.SDK.LoadRepoConfig(ctx, platform, owner, repo, repoInfo.DefaultBranch)
	if err != nil {
		addWarn("读取 .reviewagents.yaml 失败: " + err.Error())
		return view
	}
	view.Loaded = rc.Loaded
	statuses, warnings := plugin.ObserveRepoConfig(s.Registry, rc)
	plugin.SortStatuses(statuses)
	view.Use = rc.Use
	view.Disable = rc.Disable
	for _, rule := range rc.Routing {
		view.Routing = append(view.Routing, &api.RoutingRuleView{
			Paths: rule.Paths, Labels: rule.Labels, Use: rule.Use,
		})
	}
	view.Warnings = append(view.Warnings, warnings...)
	for _, st := range statuses {
		item := &api.RepoAgentStatus{
			Name:       st.Name,
			RepoEffect: st.Effect,
			Note:       strPtrOrEmpty(st.Note),
		}
		if len(st.RoutedBy) > 0 {
			ids := make([]string, 0, len(st.RoutedBy))
			for _, i := range st.RoutedBy {
				ids = append(ids, fmt.Sprintf("%d", i))
			}
			item.RoutedBy = strPtr(strings.Join(ids, ","))
		}
		view.Plugins = append(view.Plugins, item)
	}
	return view
}

// TaskStages 任务阶段时间线（零 LLM、纯 DB 推导）：
//   - stage_artifacts 有 checkpoint_r1_spec/r2_summary → R1/R2 done（checkpoint 复用同口径）；
//   - debug JSON 有 r3_checks/r3_raw → R3 done；
//   - result_json 非空 → R4/R5 done（合并与裁决已产出）；
//   - 任务终态 done → 全链 done（覆盖 R3.5/无产物场景）。
//
// 无任务返回 false。
func (s *Service) TaskStages(ctx context.Context, taskID string) (map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, false
	}
	stageDone := map[string]bool{}
	arts := storage.DecodeJSONMap(task.StageArtifacts)
	if _, ok := arts[plugin.ArtifactCkptR1Spec]; ok {
		stageDone[plugin.LayerR1] = true
	}
	if _, ok := arts[plugin.ArtifactCkptR2Summary]; ok {
		stageDone[plugin.LayerR2] = true
	}
	debug := storage.DecodeJSONMap(task.DebugJSON)
	if _, ok := debug[plugin.DebugKeyR3Checks]; ok {
		stageDone[plugin.LayerR3] = true
	}
	if debug[plugin.DebugKeyR3Raw] != "" {
		stageDone[plugin.LayerR3] = true
	}
	if task.ResultJSON != "" {
		stageDone[plugin.LayerR4] = true
	}
	if task.ResultJSON != "" && task.Verdict != "" {
		stageDone[plugin.LayerR5] = true
	}
	if task.Status == storage.StatusDone {
		for _, st := range plugin.StageTimeline {
			stageDone[st] = true
		}
	}
	stages := make([]map[string]any, 0, len(plugin.StageTimeline))
	for _, st := range plugin.StageTimeline {
		stages = append(stages, map[string]any{"stage": st, "done": stageDone[st]})
	}
	return map[string]any{
		"task_id": task.ID, "status": task.Status, "verdict": task.Verdict,
		"stages": stages,
	}, true
}

// Findings 结构化意见清单（fp/suggested_fix 直出，副驾免解析 markdown）。
func (s *Service) Findings(ctx context.Context, taskID string) ([]map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil || task.ResultJSON == "" {
		return nil, false
	}
	var payload struct {
		Findings []plugin.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(task.ResultJSON), &payload); err != nil {
		return nil, false
	}
	out := make([]map[string]any, 0, len(payload.Findings))
	for _, f := range payload.Findings {
		out = append(out, map[string]any{
			"fp": f.FP, "file": f.File, "line": f.Line, "severity": f.Severity,
			"comment": f.Comment, "by": f.By, "suggested_fix": f.SuggestedFix,
		})
	}
	return out, true
}

// Suggestion 按指纹短码取回单条建议（一键回写的数据源；应用动作在客户端侧执行）。
func (s *Service) Suggestion(ctx context.Context, taskID, fpPrefix string) (map[string]any, bool) {
	findings, ok := s.Findings(ctx, taskID)
	if !ok {
		return nil, false
	}
	for _, f := range findings {
		fp, _ := f["fp"].(string)
		if fp != "" && strings.HasPrefix(fp, fpPrefix) {
			return f, true
		}
	}
	return nil, false
}
