package bootstrap

import (
	"context"
	"strings"
	"time"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/config"
	"argus/internal/obs"
	"argus/internal/poller"
)

// webhook_ensure.go —— 启动期 webhook 幂等确保（v5.4.1，tech-debt WebhookConfigManager
// 功能机会落地）：ARGUS_ENSURE_WEBHOOK=1 + ARGUS_PUBLIC_URL 时，对 PollRepos 逐仓
// 确保 {PublicURL}/webhook/{platform} 回调存在（不存在建、同 URL 事件集漂移则重建）。
//
// secret 自愈（v5.5.1 收编 gp#34 → EnsureSecretAlwaysUpdate）：平台侧 secret
// write-only 不可比对，EnsureSecretMode=always-update 使「URL+事件集匹配即重写
// secret」，轮换收敛收进单次 ensure 往返（此前的后置 UpdateWebhook 补写
// workaround 已随上游收编删除）。
//
// 失败仅告警不阻塞启动：webhook 是主任务源，poller 兜底仍在。整体异步（GoSafe +
// 60s 上界），不延迟就绪。

// ensureOverallTimeout 确保通道整体上界（逐仓串行：List+Ensure+Update 至多三次
// API 往返；仓库多时整体封顶防拖慢启动外围）。
const ensureOverallTimeout = 60 * time.Second

// ensurePerRepoTimeout 单仓上界。
const ensurePerRepoTimeout = 20 * time.Second

// ensureLeaseTTL 多副本 ensure 单飞租约的 ttl（v5.4.8）：与整体上界同量级，
// 跑完即 Release；实例崩溃时租约自然过期，不阻塞他副本补位。
const ensureLeaseTTL = 2 * time.Minute

// webhookEvents 平台原生默认事件词表。SDK 后端对 Events 的映射各不相同：
// gitea/forgejo 原样透传（试点脚本 pull_request_only+issue_comment 实弹验证）；
// github 原生两词；gitee 经 giteeHookEventFlags 映射（pull_request→merge_requests
// 族、comment→note 族）；gitlab 三族映射（merge_request + note——v0.101.1 起
// SDK 支持 note/comment 词，#32 缺口已修，GitLab 评论命令链路 SDK 面闭环）。
// 平台不在表内且未配覆盖时跳过（错词表整条 webhook 被平台拒收，宁可不建）。
var webhookEvents = map[string][]string{
	"gitea":   {"pull_request_only", "issue_comment"},
	"forgejo": {"pull_request_only", "issue_comment"},
	"github":  {"pull_request", "issue_comment"},
	"gitee":   {"pull_request", "comment"},
	"gitlab":  {"merge_request", "note"},
}

// eventsFor 平台事件集：显式覆盖（ARGUS_<P>_WEBHOOK_EVENTS 逗号分隔）优先，
// 否则内置默认；两者皆无返回 nil（调用方跳过并告警）。
func eventsFor(pc config.PlatformConfig, platform string) []string {
	if raw := strings.TrimSpace(pc.WebhookEvents); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return webhookEvents[platform]
}

// ensureWebhooks 逐仓幂等确保。targets 复用 poller 的仓库清单（既有任务源事实
// 标准）；无 token/secret/事件词表的组合跳过并告警。
func ensureWebhooks(ctx context.Context, sdk webhookSDK, cfg *config.Config, log logx.Logger, targets []poller.Target) {
	ctx, cancel := context.WithTimeout(ctx, ensureOverallTimeout)
	defer cancel()
	created, updated, unchanged := 0, 0, 0
	for _, t := range targets {
		if ctx.Err() != nil {
			log.Warn("argus.webhook_ensure.budget_exhausted", "hint", "整体上界耗尽，剩余仓库跳过；poller 兜底不受影响")
			break
		}
		p, err := sdk.Provider(t.Platform)
		if err != nil {
			log.Warn("argus.webhook_ensure.platform_unavailable", "platform", t.Platform, "error", err.Error())
			obs.IncrWebhookEnsure("failed")
			continue
		}
		pc := cfg.Platforms[t.Platform]
		if pc.Token == "" {
			log.Warn("argus.webhook_ensure.skipped", "platform", t.Platform,
				"repo", t.Owner+"/"+t.Repo, "reason", "平台未配 token")
			obs.IncrWebhookEnsure("skipped")
			continue
		}
		if pc.Secret == "" {
			log.Warn("argus.webhook_ensure.skipped", "platform", t.Platform,
				"repo", t.Owner+"/"+t.Repo,
				"reason", "平台未配 SECRET：入站签名校验 fail-closed，建了也会被本服务全拒")
			obs.IncrWebhookEnsure("skipped")
			continue
		}
		events := eventsFor(pc, t.Platform)
		if len(events) == 0 {
			log.Warn("argus.webhook_ensure.skipped", "platform", t.Platform,
				"repo", t.Owner+"/"+t.Repo,
				"reason", "无平台默认事件词表且未配 ARGUS_"+strings.ToUpper(t.Platform)+"_WEBHOOK_EVENTS")
			obs.IncrWebhookEnsure("skipped")
			continue
		}
		repoCtx, repoCancel := context.WithTimeout(ctx, ensurePerRepoTimeout)
		action, _, err := provider.EnsureWebhook(repoCtx, p, provider.CreateWebhookOptions{
			Owner: t.Owner, Repo: t.Repo,
			URL:    cfg.PublicURL + "/webhook/" + t.Platform,
			Secret: pc.Secret,
			Events: events,
			// secret 轮换收敛（gp#34 → v0.114.0 EnsureSecretAlwaysUpdate）：平台侧
			// secret write-only 不可比对，unchanged 时可能携旧 secret——匹配即重写，
			// 一次 ensure 往返内完成轮换收敛（v5.4.1 时代的手工 UpdateWebhook
			// workaround 已收编进上游，此处删除后置补写段）。
			EnsureSecretMode: provider.EnsureSecretAlwaysUpdate,
		})
		if err != nil {
			log.Warn("argus.webhook_ensure.failed", "platform", t.Platform,
				"repo", t.Owner+"/"+t.Repo, "error", err.Error(), "hint", "poller 兜底不受影响")
			obs.IncrWebhookEnsure("failed")
			repoCancel()
			continue
		}
		switch action {
		case provider.EnsureCreated:
			created++
			obs.IncrWebhookEnsure("created")
		case provider.EnsureUpdated:
			updated++
			obs.IncrWebhookEnsure("updated")
		default:
			unchanged++
			obs.IncrWebhookEnsure("unchanged")
		}
		repoCancel()
	}
	log.Info("argus.webhook_ensure.done", "created", created, "updated", updated, "unchanged", unchanged)
}

// webhookSDK ensure 通道消费的 SDK 面（*argusctx.SDKCtx 满足；收窄接口便于测试打桩）。
type webhookSDK interface {
	Provider(platform string) (provider.Provider, error)
}
