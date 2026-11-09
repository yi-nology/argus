// service_impact.go —— 影响面视图数据面（v3.183.0）：MCP get_impact 与
// GET /tasks/:id/impact（交互 HTML）共用。快照由 runner 影响面管线即时落
// artifacts_json（任务 running 期即可消费，评审失败也不丢）。
package service

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"argus/internal/impactview"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// prevImpactCompare 同 MR 上一轮 done 任务的影响面快照集合差。
// 无上一轮/无快照/解码失败一律 nil（无对比面,不阻断主图）。
func (s *Service) prevImpactCompare(ctx context.Context, cur *storage.Task) *impactview.ImpactCompare {
	tasks, err := s.Store.ListTasksByPR(ctx, cur.Platform, cur.Owner, cur.Repo, cur.Number, 6)
	if err != nil {
		return nil
	}
	for i := range tasks {
		pt := &tasks[i]
		if pt.ID == cur.ID || pt.Status != storage.StatusDone {
			continue
		}
		raw := storage.DecodeJSONMap(pt.ArtifactsJSON)[plugin.ArtifactImpactView]
		if raw == "" {
			continue
		}
		var prev impactview.Snapshot
		if json.Unmarshal([]byte(raw), &prev) != nil {
			continue
		}
		var curSnap impactview.Snapshot
		if raw2 := storage.DecodeJSONMap(cur.ArtifactsJSON)[plugin.ArtifactImpactView]; raw2 != "" {
			if json.Unmarshal([]byte(raw2), &curSnap) != nil {
				return nil
			}
		}
		return impactview.CompareSnapshots(&prev, &curSnap)
	}
	return nil
}

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
	// v5.8.1 演化对比数据面（MCP get_impact 消费）
	if cmp := s.prevImpactCompare(ctx, task); cmp != nil {
		m["compare"] = cmp
	}
	return m, true
}

// ImpactViewHTML 交互视图：自包含单文件 HTML（零外链，内网可用；可离线存档）。
func (s *Service) ImpactViewHTML(ctx context.Context, taskID string) (string, bool) {
	snap, task, ok := s.impactSnapshot(ctx, taskID)
	if !ok {
		return "", false
	}
	// v5.8.0 审查意见挂图：findings 从任务 ResultJSON 解码（两态单源同 GetTask）
	fs, _ := taskFindings(task.ResultJSON)
	// v5.8.1 演化对比：同 MR 上一轮快照集合差进页头摘要
	cmp := s.prevImpactCompare(ctx, task)
	// v5.8.2 惯犯文件热力：prints 跨 MR 意见史（改动文件集合聚合）
	hist, _ := s.Store.FindingHistoryByFiles(ctx, task.Platform, task.Owner, task.Repo, snap.Changed)
	page := impactview.RenderHTMLFull(snap, task.ID, impactview.ViewOpts{
		Findings: fs, Compare: cmp, History: hist,
	})
	// v5.6.4 图谱互跳：影响图页头注入整仓地图链接（同源相对路径；带 repo-map
	// share 令牌——share 访客（免登看任务图谱）点过去不落 401；secret 空=
	// 鉴权关闭时裸链即可）。
	if page != "" {
		link := repoMapLink(s.Cfg.APIToken, task.Platform, task.Owner, task.Repo)
		page = strings.Replace(page, "<header>",
			`<header><a class="meta" href="`+html.EscapeString(link)+`" style="margin-left:auto">🗺 整仓地图</a>`, 1)
	}
	return page, true
}

// repoMapLink 整仓地图互跳链接（share 令牌可选注入）。
func repoMapLink(secret, platform, owner, repo string) string {
	link := "/repos/map?platform=" + url.QueryEscape(platform) +
		"&owner=" + url.QueryEscape(owner) + "&repo=" + url.QueryEscape(repo)
	if tok := impactview.ImpactShareToken(secret, repoMapShareID(platform, owner, repo)); tok != "" {
		link += "&share=" + tok
	}
	return link
}
