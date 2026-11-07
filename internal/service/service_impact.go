// service_impact.go —— 影响面视图数据面（v3.183.0）：MCP get_impact 与
// GET /tasks/:id/impact（交互 HTML）共用。快照由 runner 影响面管线即时落
// artifacts_json（任务 running 期即可消费，评审失败也不丢）。
package service

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/hertz/pkg/app"

	"argus/internal/impactview"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// ImpactShareVerifier 影响面 share 链接校验器（v5.6.1，main 装配进
// routerapi.SetImpactShare）：?share= 命中任务绑定 HMAC 令牌即放行该任务的
// /impact 页，否则回落 Bearer（middleware 层组合）。secret 取 API master
// token，为空（鉴权关闭）恒 false——此场景下 Bearer 本就不拦。
func (s *Service) ImpactShareVerifier() func(*app.RequestContext) bool {
	secret := s.Cfg.APIToken
	return func(c *app.RequestContext) bool {
		return impactview.ImpactShareValid(secret, c.Param("taskId"), c.Query("share"))
	}
}

// impactSnapshot 任务影响面快照。无任务/域外任务/无快照/坏 JSON 一律 false
// （域外按不存在处理，不泄露存在性——TaskStages 同纪律）。
func (s *Service) impactSnapshot(ctx context.Context, taskID string) (*impactview.Snapshot, *storage.Task, bool) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, nil, false
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, nil, false
	}
	raw := storage.DecodeJSONMap(task.ArtifactsJSON)[plugin.ArtifactImpactView]
	if raw == "" {
		return nil, task, false
	}
	var snap impactview.Snapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil, task, false
	}
	return &snap, task, true
}

// ImpactView MCP get_impact 数据面：快照 map 形态（附任务元信息）。
func (s *Service) ImpactView(ctx context.Context, taskID string) (map[string]any, bool) {
	snap, task, ok := s.impactSnapshot(ctx, taskID)
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	m["task_id"] = task.ID
	m["task_status"] = task.Status
	return m, true
}

// ImpactViewHTML 交互视图：自包含单文件 HTML（零外链，内网可用；可离线存档）。
func (s *Service) ImpactViewHTML(ctx context.Context, taskID string) (string, bool) {
	snap, task, ok := s.impactSnapshot(ctx, taskID)
	if !ok {
		return "", false
	}
	return impactview.RenderHTML(snap, task.ID), true
}
