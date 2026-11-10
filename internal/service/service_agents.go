// service_agents.go —— Agent 可观测与 MCP 副驾数据面：agent 清单/仓库配置视图/任务阶段/findings/建议定位。

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// ListAgents 插拔层可观测（🔒）：Registry 全部插件清单/状态（含 needs/triggers）。
// platform/owner/repo 三参全传时，附带该仓库 .reviewagents.yaml 的可观测视图：
// 配置原文（信任边界规范化后）+ 各插件生效状态 + 告警（§3.4 落到看得见的地方）。
func (s *Service) ListAgents(ctx context.Context, platform, owner, repo string) *argus.ListAgentsResponse {
	resp := &argus.ListAgentsResponse{}
	for _, reg := range s.Registry.Snapshot() {
		m := reg.Manifest
		dto := &argus.AgentManifestDTO{
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
func (s *Service) repoConfigView(ctx context.Context, platform, owner, repo string) *argus.RepoConfigView {
	view := &argus.RepoConfigView{Loaded: false}
	addWarn := func(msg string) {
		view.Warnings = append(view.Warnings, msg)
	}
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		addWarn("平台未接入: " + platform)
		return view
	}
	rc, err := s.SDK.LoadRepoConfigAuto(ctx, platform, owner, repo, "")
	if err != nil {
		addWarn("读取 .reviewagents.yaml 失败: " + err.Error())
		return view
	}
	view.Loaded = rc.Loaded
	statuses, warnings := dispatch.ObserveRepoConfig(s.Registry, rc)
	dispatch.SortStatuses(statuses)
	view.Use = rc.Use
	view.Disable = rc.Disable
	for _, rule := range rc.Routing {
		view.Routing = append(view.Routing, &argus.RoutingRuleView{
			Paths: rule.Paths, Labels: rule.Labels, Use: rule.Use,
		})
	}
	view.Warnings = append(view.Warnings, warnings...)
	for _, st := range statuses {
		item := &argus.RepoAgentStatus{
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

// TaskStages 任务阶段时间线（零 LLM、纯 DB 推导）：形态见 deriveStageDone。
// 无任务返回 false。
func (s *Service) TaskStages(ctx context.Context, taskID string) (map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, false // 域外任务按不存在处理（不泄露存在性）
	}
	stageDone := deriveStageDone(
		storage.DecodeJSONMap(task.StageArtifacts),
		storage.DecodeJSONMap(task.DebugJSON),
		task.ResultJSON, task.Verdict, task.Status)
	stages := make([]map[string]any, 0, len(plugin.StageTimeline))
	for _, st := range plugin.StageTimeline {
		stages = append(stages, map[string]any{"stage": st, "done": stageDone[st]})
	}
	return map[string]any{
		"task_id": task.ID, "status": task.Status, "verdict": task.Verdict,
		"stages": stages,
	}, true
}

// deriveStageDone 阶段完成度推导（v3.132.0 加 stage_progress 标记主判据）：
//   - stage_progress（onStage 每阶段开头落库）：标记之前的阶段全 done——这是
//     主判据，天然涵盖「跳过的阶段」（RunReviewOnly 无 R1/R3/R35，此前被推导
//     成"没做到"，轮询恒显示 R1）；标记本身的阶段=进行中（不算 done）。
//   - 产物规则（老任务无标记时的回退，有标记时亦是佐证）：checkpoint_r1_spec/
//     r2_summary → R1/R2；debug r3_checks/r3_raw → R3；result_json → R4；
//     +verdict → R5。
//   - 任务终态 done → 全链 done（覆盖无产物场景）。
func deriveStageDone(arts, debug map[string]string, resultJSON, verdict, status string) map[string]bool {
	stageDone := map[string]bool{}
	// 主判据：进度标记位置
	if progress := arts[plugin.ArtifactStageProgress]; progress != "" {
		for _, st := range plugin.StageTimeline {
			if st == progress {
				break
			}
			stageDone[st] = true
		}
	}
	// 产物回退/佐证
	if _, ok := arts[plugin.ArtifactCkptR1Spec]; ok {
		stageDone[plugin.LayerR1] = true
	}
	if _, ok := arts[plugin.ArtifactCkptR2Summary]; ok {
		stageDone[plugin.LayerR2] = true
	}
	if _, ok := debug[plugin.DebugKeyR3Checks]; ok {
		stageDone[plugin.LayerR3] = true
	}
	if debug[plugin.DebugKeyR3Raw] != "" {
		stageDone[plugin.LayerR3] = true
	}
	if resultJSON != "" {
		stageDone[plugin.LayerR4] = true
	}
	if resultJSON != "" && verdict != "" {
		stageDone[plugin.LayerR5] = true
	}
	if status == storage.StatusDone {
		for _, st := range plugin.StageTimeline {
			stageDone[st] = true
		}
	}
	return stageDone
}

// Findings 结构化意见清单（fp/suggested_fix 直出，副驾免解析 markdown）。
func (s *Service) Findings(ctx context.Context, taskID string) ([]map[string]any, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, false // 域外任务按不存在处理
	}
	findings, ok := taskFindings(task.ResultJSON)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		out = append(out, map[string]any{
			"fp": f.FP, "file": f.File, "line": f.Line, "severity": f.Severity,
			"comment": f.Comment, "by": f.By, "suggested_fix": f.SuggestedFix,
		})
	}
	return out, true
}

// SearchCode MCP search_code 数据面（v3.121.0）：PR 工作副本受控全文检索。
// 启用门控（ARGUS_CODESEARCH/工作副本池）、目录解析与检索语义都在 qa.Engine，
// 门面纯透传——与 LatestReview/AnswerAboutReview 同型。
func (s *Service) SearchCode(ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SearchCode(ctx, platform, owner, repo, number, pattern, glob, ignoreCase)
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
