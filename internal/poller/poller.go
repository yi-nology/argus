// Package poller 轮询兜底任务源（v3.0.1 试点改造）：
// 平台→Argus 入站不可达（NAT/VPN/egress 受限，真实试点环境实测如此）时，
// 以出站轮询发现 PR 的创建与 head 变化，落库为任务——与 webhook 任务同语义。
// 去重复用 CreateTaskForWebhook 的同 SHA 双查（last_head_sha + in-flight），天然幂等。
package poller

import (
	"context"
	"strings"
	"time"

	"github.com/yi-nology/git-platform-sdk/provider"

	"git.example.com/enjoydream/ekit/concurrency/async"
	"git.example.com/enjoydream/ekit/observability/logx"

	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/storage"
)

// Target 一个被 watch 的仓库。
type Target struct {
	Platform string
	Owner    string
	Repo     string
}

// Poller 轮询器。
type Poller struct {
	SDK      *argusctx.SDKCtx
	Store    *storage.Store
	Targets  []Target
	Interval time.Duration
	// SkipDraft 与 webhook 路径的 ARGUS_SKIP_DRAFT 同源（main 装配时传 cfg.SkipDraft），
	// 两条任务源对 draft 的行为保持一致。
	SkipDraft bool
	Log       logx.Logger
	Bus       *progress.Bus // task.created 事件（SSE；nil 安全）
	// IsLeader 选主门控（多副本部署：worker.LeaderElector.IsLeader 注入；
	// nil = 单实例语义恒为 leader）。非 leader 的 tick 直接跳过——轮询任务源
	// 本身幂等（同 SHA 双查），双跑无害只浪费，收敛为单份省 API 配额。
	IsLeader func() bool
}

// ParseTargets 解析 "platform:owner/repo" 列表（逗号分隔）；非法项跳过并记入错误。
func ParseTargets(raw string) ([]Target, []string) {
	var targets []Target
	var errs []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		platform, rest, ok := strings.Cut(item, ":")
		if !ok {
			errs = append(errs, item)
			continue
		}
		owner, repo, ok := strings.Cut(rest, "/")
		if !ok || owner == "" || repo == "" {
			errs = append(errs, item)
			continue
		}
		targets = append(targets, Target{
			Platform: strings.ToLower(strings.TrimSpace(platform)),
			Owner:    strings.TrimSpace(owner),
			Repo:     strings.TrimSpace(repo),
		})
	}
	return targets, errs
}

// Start 启动轮询循环；ctx 取消时退出。
func (p *Poller) Start(ctx context.Context) {
	async.GoSafe(func() {
		ticker := time.NewTicker(p.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if p.IsLeader != nil && !p.IsLeader() {
					continue // 非 leader 实例：跳过本轮（选主器日志已记录角色）
				}
				p.pollOnce(ctx)
			}
		}
	})
	p.Log.Info("argus.poller.started",
		"interval", p.Interval.String(),
		"repos", len(p.Targets),
		"hint", "轮询兜底：平台入站 webhook 不可达时的任务来源")
}

func (p *Poller) pollOnce(ctx context.Context) {
	for _, t := range p.Targets {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var prs []*provider.ChangeRequest
		pagingTruncated := false
		for page := 1; page <= 3; page++ { // 上限 3 页 × 30：超大盘仓库另议
			pagePrs, _, err := p.SDK.ListCRs(ctx, t.Platform, provider.ListCROptions{
				Owner: t.Owner, Repo: t.Repo, State: provider.CRStateOpened,
				Page: page, PerPage: 30,
			})
			if err != nil {
				p.Log.Warn("argus.poller.list_failed",
					"repo", t.Owner+"/"+t.Repo, "error", err.Error())
				prs = nil
				break
			}
			prs = append(prs, pagePrs...)
			if len(pagePrs) < 30 {
				break
			}
			if page == 3 {
				// 分页上限截断静默是盲区：第 91+ 个 open PR 永远不被轮询发现（v3.8.0）
				pagingTruncated = true
			}
		}
		if pagingTruncated {
			p.Log.Warn("argus.poller.paging_truncated", "repo", t.Owner+"/"+t.Repo,
				"limit", 90, "hint", "open PR 超过轮询分页上限，超出部分不被发现；如需覆盖请调大分页上限")
		}
		for _, cr := range prs {
			// 防线：平台 state 过滤失灵时（实测 Forgejo 曾因 state=opened 被忽略而
			// 返回全部 PR）非 open 的 CR 一律不建任务，避免已关闭 PR 被重新审查。
			if cr.State != provider.CRStateOpened {
				continue
			}
			if cr.Draft && p.SkipDraft { // 与 webhook 路径的 §5.1 规则 3 同源配置
				continue
			}
			if cr.HeadSHA == "" {
				continue
			}
			task := &storage.Task{
				ID:        storage.NewTaskID(),
				Kind:      plugin.TaskKindRequirementCompliance,
				Platform:  t.Platform,
				Owner:     t.Owner,
				Repo:      t.Repo,
				Number:    cr.Number,
				HeadSHA:   cr.HeadSHA,
				EventType: "", // 轮询来源无事件类型；触发器 Events 条件视为通过
				Status:    storage.StatusPending,
				CreatedAt: time.Now(),
			}
			dupReason, _, err := p.Store.CreateTaskForWebhook(ctx, task)
			if err != nil {
				p.Log.Warn("argus.poller.task_create_failed", "error", err.Error())
				continue
			}
			if dupReason != "" {
				continue // 同 SHA 已审查或审查中：幂等跳过
			}
			p.Log.Info("argus.poller.task_created",
				"pr", t.Owner+"/"+t.Repo+"#"+cr.Number, "head", cr.HeadSHA, "task", task.ID)
			if p.Bus != nil {
				p.Bus.Publish(progress.Event{
					Type: "task.created", TaskID: task.ID, Platform: task.Platform,
					Owner: task.Owner, Repo: task.Repo, Number: task.Number,
				})
			}
		}
	}
}
