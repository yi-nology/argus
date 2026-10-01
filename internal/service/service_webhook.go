package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yi-nology/go-git-platform/provider"

	api "argus/biz/model/api"
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// ---------- webhook（POST /webhook/:platform）----------

// 事件类型白名单（§5.1 规则 1）。SDK 各后端在投递前都会过 provider.NormalizeCRAction
// 归一化（opened/closed/merged/reopened/updated 五词，未识别词原样透传），因此键必须是
// 归一化词表而非平台原生词：v3.7.x 及此前维护的是原生词表（cr.synchronize/cr.update 等），
// GitHub 的 synchronize、GitLab 的 update 归一化为 cr.updated 后不在白名单——"推新提交
// 触发重审"整条静默断流，Forgejo 的 cr.synchronized 命中纯靠透传拼写巧合（v3.8.0 修复）。
// cr.merged / cr.closed 移出白名单：对已合并/已关闭的 MR 发起全量审查只烧 token。
var crEventWhitelist = map[string]string{
	"cr.opened":       "opened",
	"cr.reopened":     "reopened",
	"cr.updated":      "updated",      // 所有平台"推新提交/编辑"的归一化形态
	"cr.synchronized": "synchronized", // 历史兼容：未过归一化的直连投递仍可触发
	"cr.approved":     "approved",     // 透传词：评审动作触发复审的平台接入点
	"cr.unapproved":   "unapproved",
}

// Webhook 平台 webhook 入口：返回 (IDL 响应体, HTTP 状态码)。
// Webhook webhook 入口。v3.58.0 拆分为准入防线（webhookAdmission）→ 事件分流
// （installation/comment/CR）→ CR 过滤链（webhookCRFilter）→ 建任务去重
// （webhookCreateTask）。§5.1 过滤规则顺序即安全语义，不得重排。
func (s *Service) Webhook(ctx context.Context, c *app.RequestContext, platform string) (*api.WebhookResponse, int) {
	ev, delivery, resp, status, ok := s.webhookAdmission(ctx, c, platform)
	if !ok {
		return resp, status
	}

	// GitHub App 安装事件：记录安装信息，不建审查任务
	if ev.Type == "installation" || ev.Type == "installation_repositories" {
		return s.handleInstallationEvent(ev, delivery), http.StatusOK
	}

	// Q&A 评论路由：issue_comment 事件中含 @argus ask → 直接 LLM 回答（不建 task）。
	// 异步执行（深挖 P0）：ReAct 可达数分钟，同步跑会吃满平台 webhook 超时——
	// 断连取消 ctx 杀掉 ReAct，平台重试又撞 30s 节流，用户永无回复。经
	// SubmitComment（v3.8.0）：WithoutCancel 保 trace + 超时上界 + 有界并发 +
	// 停机排水；delivery 幂等已下沉 DB（MarkCommentDelivery，多副本一致）。
	if isCommentEvent(ev) {
		if !s.SubmitComment(ctx, ev, delivery) {
			s.log().Warn("argus.comment.busy", "platform", platform, "event_id", delivery)
			return &api.WebhookResponse{Status: "busy", Reason: strPtr("评论处理并发已满，请稍后重试"), EventId: strPtr(delivery)}, http.StatusOK
		}
		return &api.WebhookResponse{Status: "accepted", EventId: strPtr(delivery), Reason: strPtr("qa comment")}, http.StatusOK
	}

	// CI 失败事件（v3.71.0，SDK Pipeline Hook 解析）：建 ci_analysis 任务走
	// 写作面归因（失败 job 日志 → LLM → PR 评论）。pipeline 事件没有完整 CR
	// 语义（可能无 MR），不过 CR 过滤链，走独立的轻量建任务分支。
	if strings.HasPrefix(ev.Type, "pipeline.") {
		if ev.CommitSHA == "" || ev.Repo == nil {
			s.log().Info("argus.webhook.skipped", "platform", platform, "event", ev.Type, "reason", "pipeline 事件缺 commit/repo 信息")
			obs.IncrWebhookSkipped("pipeline_incomplete")
			return &api.WebhookResponse{Status: "skipped", Reason: strPtr("pipeline 事件缺 commit/repo 信息"), EventId: strPtr(delivery)}, http.StatusOK
		}
		return s.pipelineCreateTask(ctx, ev, delivery, platform)
	}

	// §5.1 规则 1-3：事件白名单 / bot 自审 / draft 与异常事件防御
	skipped := func(reason, why string) (*api.WebhookResponse, int) {
		// 深挖修复（v3.8.0）：此前 skipped 零日志零指标，整类事件被丢在生产不可观测。
		s.log().Info("argus.webhook.skipped", "platform", platform, "event", ev.Type, "reason", why)
		obs.IncrWebhookSkipped(reason)
		return &api.WebhookResponse{Status: "skipped", Reason: strPtr(why), EventId: strPtr(delivery)}, http.StatusOK
	}
	if resp, status, ok := s.webhookCRFilter(ev, skipped); !ok {
		return resp, status
	}

	return s.webhookCreateTask(ctx, ev, delivery, platform)
}

// webhookAdmission 准入防线（顺序即安全语义，不得重排）：平台接入校验 →
// secret fail-closed → Content-Length 预检 → 签名校验+事件解析 → 幂等预查。
// ok=false 时 resp/status 即最终响应。
func (s *Service) webhookAdmission(ctx context.Context, c *app.RequestContext, platform string) (
	ev *provider.NormalizedEvent, delivery string, resp *api.WebhookResponse, status int, ok bool) {
	platform, pcfg, ok := s.requirePlatform(platform)
	if !ok {
		return nil, "", &api.WebhookResponse{Status: "error", Reason: strPtr("平台未接入: " + platform)}, http.StatusNotFound, false
	}
	// fail-closed（与 🔒 路由同一纪律，v3.0.2 起）：空 secret 时 SDK 跳过签名校验，
	// 该平台 webhook 等于无鉴权——任何能访问本端口的设备都能伪造事件烧 LLM 预算/
	// 发评论。此前只做启动期告警，请求期照常放行；现直接拒绝直到补配 ARGUS_<P>_SECRET。
	if pcfg.Secret == "" {
		obs.IncrWebhookRejected("no_secret")
		s.log().Warn("argus.webhook.rejected_no_secret", "platform", platform,
			"hint", "ARGUS_"+strings.ToUpper(platform)+"_SECRET 未配置，webhook 拒绝服务（fail-closed）")
		return nil, "", &api.WebhookResponse{
			Status: "error",
			Reason: strPtr("平台 webhook secret 未配置，拒绝服务（配置 " + "ARGUS_" + strings.ToUpper(platform) + "_SECRET 后恢复）"),
		}, http.StatusServiceUnavailable, false
	}

	// Content-Length 预检：body 读取前拒绝超限请求，节省内存分配。
	// Hertz WithMaxRequestBodySize 作为第二道防线（框架层拦截）。
	if s.Cfg.WebhookMaxBody > 0 {
		if cl := string(c.GetHeader("Content-Length")); cl != "" {
			if n, err := strconv.Atoi(cl); err == nil && n > s.Cfg.WebhookMaxBody {
				obs.IncrWebhookRejected("body_too_large")
				return nil, "", &api.WebhookResponse{
					Status: "error",
					Reason: strPtr(fmt.Sprintf("请求体过大: %d > %d bytes", n, s.Cfg.WebhookMaxBody)),
				}, http.StatusRequestEntityTooLarge, false
			}
		}
	}

	// 幂等键：在 ParseWebhookEvent 之前从原始请求头取 delivery（§5.2 四轮评审修正）
	delivery = s.SDK.ExtractDeliveryID(func(key string) string {
		return string(c.GetHeader(key))
	})

	// 重建 *http.Request（SDK ParseWebhookEvent 需要 net/http 语义做签名校验+解析）
	rawBody := c.Request.Body()
	if len(rawBody) == 0 {
		return nil, delivery, &api.WebhookResponse{Status: "error", Reason: strPtr("读取 body 失败")}, http.StatusBadRequest, false
	}
	httpReq, err := http.NewRequest(string(c.Method()), string(c.URI().Path()), bytes.NewReader(rawBody))
	if err != nil {
		return nil, delivery, &api.WebhookResponse{Status: "error", Reason: strPtr("内部请求构造失败")}, http.StatusInternalServerError, false
	}
	c.Request.Header.VisitAll(func(k, v []byte) {
		httpReq.Header.Add(string(k), string(v))
	})

	ev, err = s.SDK.ParseWebhook(platform, httpReq, pcfg.Secret)
	if err != nil {
		if errors.Is(err, provider.ErrWebhookValidation) {
			// §10.2：签名校验失败 → 401 拒绝（不产生 task）
			obs.IncrWebhookRejected("signature")
			s.log().Warn("argus.webhook.signature_rejected", "platform", platform)
			return nil, delivery, &api.WebhookResponse{Status: "error", Reason: strPtr("签名校验失败")}, http.StatusUnauthorized, false
		}
		obs.IncrWebhookRejected("parse")
		return nil, delivery, &api.WebhookResponse{Status: "error", Reason: strPtr("事件解析失败: " + err.Error())}, http.StatusBadRequest, false
	}

	// 幂等命中：平台重发 → 返回既有任务（不重复烧 LLM，§5.2）
	if delivery != "" {
		if exist, err := s.Store.GetTaskByEventID(ctx, delivery); err == nil && exist != nil {
			s.log().Info("argus.webhook.duplicate_delivery", "event_id", delivery, "task", exist.ID)
			return nil, delivery, &api.WebhookResponse{
				Status:  "duplicate",
				Reason:  strPtr("重复投递，命中既有任务"),
				EventId: strPtr(delivery),
				TaskId:  strPtr(exist.ID),
			}, http.StatusOK, false
		}
	}
	return ev, delivery, nil, 0, true
}

// webhookCRFilter §5.1 CR 事件过滤链：规则 1 事件类型白名单（防评论风暴、防
// 自我触发循环）→ 规则 2 bot 自审保护 → 规则 3 draft 默认不审 + 缺 CR/Repo
// 信息的异常事件防御性跳过（长跑服务不 panic）。ok=false 时 resp/status 即
// skipped 响应。
func (s *Service) webhookCRFilter(ev *provider.NormalizedEvent,
	skipped func(reason, why string) (*api.WebhookResponse, int)) (*api.WebhookResponse, int, bool) {
	if _, ok := crEventWhitelist[ev.Type]; !ok {
		r, st := skipped("event_type", "事件类型不在白名单: "+ev.Type)
		return r, st, false
	}
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.Cfg.BotUsername {
		r, st := skipped("bot_self", "bot 自身事件，跳过")
		return r, st, false
	}
	if ev.CR == nil || ev.Repo == nil {
		r, st := skipped("missing_cr", "事件缺少 CR/Repo 信息")
		return r, st, false
	}
	if ev.CR.Draft && s.Cfg.SkipDraft {
		r, st := skipped("draft", "draft PR 默认不审（ARGUS_SKIP_DRAFT=true）")
		return r, st, false
	}
	return nil, 0, true
}

// webhookCreateTask 组装任务记录（Kind 固定走需求复核 workflow；PR Bot 形态）
// 并落库：规则 4 同 head SHA 去重（查询与落库同一事务，防 in-flight 竞态/
// TOCTOU）→ 去重分支透出 → 审计 + SSE 发布。
func (s *Service) webhookCreateTask(ctx context.Context, ev *provider.NormalizedEvent,
	delivery, platform string) (*api.WebhookResponse, int) {
	if delivery == "" {
		s.log().Warn("argus.webhook.delivery_header_missing",
			"platform", platform, "event", "幂等键缺失，本次投递不做幂等（event_id=NULL）")
	}
	task := &storage.Task{
		ID:        storage.NewTaskID(),
		Kind:      plugin.TaskKindRequirementCompliance,
		Platform:  platform,
		Owner:     ev.Repo.Owner,
		Repo:      ev.Repo.Name,
		Number:    ev.CR.Number,
		HeadSHA:   ev.CR.HeadSHA,
		EventType: ev.Type,
		Status:    storage.StatusPending,
		Priority:  5, // webhook push：作者刚推代码在等，先于 poller 批量（v3.40.0）
		CreatedAt: time.Now(),
	}
	if delivery != "" {
		task.EventID = &delivery
	}

	dupReason, existingID, err := s.Store.CreateTaskForWebhook(ctx, task)
	if err != nil {
		s.log().Error("argus.webhook.task_create_failed", "error", err.Error())
		return &api.WebhookResponse{Status: "error", Reason: strPtr("任务落库失败")}, http.StatusInternalServerError
	}
	if dupReason != "" {
		plugin.ReportTaskDeduped(dupReason) // 去重分布观测（深挖修复：此前只有日志）
	}
	if dupReason == "last_head_sha" {
		return &api.WebhookResponse{Status: "duplicate", Reason: strPtr("同 head SHA 已审查过"), EventId: strPtr(delivery)}, http.StatusOK
	}
	// event_id 唯一冲突（幂等预查与落库之间的并发窗口，store 回查既有任务）：
	// 此前漏判该分支——照常返回新 task.ID（未入库的幻影 ID）+ 发幽灵 SSE 事件。
	if dupReason == "event_id" {
		resp := &api.WebhookResponse{Status: "duplicate", Reason: strPtr("重复投递，命中既有任务"), EventId: strPtr(delivery)}
		if existingID != "" {
			resp.TaskId = strPtr(existingID)
		}
		return resp, http.StatusOK
	}
	if dupReason == "in_flight" {
		resp := &api.WebhookResponse{Status: "duplicate", Reason: strPtr("同 head SHA 任务执行中"), EventId: strPtr(delivery)}
		if existingID != "" {
			resp.TaskId = strPtr(existingID)
		}
		return resp, http.StatusOK
	}

	s.log().Info("argus.webhook.accepted", "task", task.ID, "platform", platform,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number, "head", ev.CR.HeadSHA, "event_id", delivery)
	if s.Audit != nil {
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "platform", platform,
			"owner", task.Owner, "repo", task.Repo, "number", task.Number, "source", "webhook")
	}
	s.publishCreated(task)
	return &api.WebhookResponse{Status: "accepted", EventId: strPtr(delivery), TaskId: strPtr(task.ID)}, http.StatusOK
}

// pipelineCreateTask CI 失败事件 → ci_analysis 任务（写作面归因）。
// 去重键=同 commit 失败只归因一次（last_head_sha 语义复用：pipeline 对同
// SHA 只有一个终态，重复投递靠 event_id 幂等兜底）。Number 可空——无 MR 的
// push pipeline 归因产物只落库不评论。
func (s *Service) pipelineCreateTask(ctx context.Context, ev *provider.NormalizedEvent,
	delivery, platform string) (*api.WebhookResponse, int) {
	number := ""
	if ev.CR != nil {
		number = ev.CR.Number
	}
	task := &storage.Task{
		ID:        storage.NewTaskID(),
		Kind:      plugin.TaskKindCIAnalysis,
		Platform:  platform,
		Owner:     ev.Repo.Owner,
		Repo:      ev.Repo.Name,
		Number:    number,
		HeadSHA:   ev.CommitSHA,
		EventType: ev.Type,
		Status:    storage.StatusPending,
		Priority:  4, // CI 红了作者正盯着，略高于 poller 批量
		CreatedAt: time.Now(),
	}
	if delivery != "" {
		task.EventID = &delivery
	}
	dupReason, existingID, err := s.Store.CreateTaskForWebhook(ctx, task)
	if err != nil {
		s.log().Error("argus.webhook.pipeline_task_create_failed", "error", err.Error())
		return &api.WebhookResponse{Status: "error", Reason: strPtr("任务落库失败")}, http.StatusInternalServerError
	}
	if dupReason == "event_id" || dupReason == "in_flight" {
		// in_flight 必须处理（v3.75.3 修复）：同 commit 的 retry pipeline 是新
		// delivery，event_id 拦不住第二次；漏判会以未落库的 task.ID 回 accepted
		// 并向 SSE 广播幽灵任务（对照 webhookCreateTask 的同款处理）。
		reason := "重复投递，命中既有任务"
		if dupReason == "in_flight" {
			reason = "同 commit 同类任务审查中，幂等跳过"
		}
		resp := &api.WebhookResponse{Status: "duplicate", Reason: strPtr(reason), EventId: strPtr(delivery)}
		if existingID != "" {
			resp.TaskId = strPtr(existingID)
		}
		return resp, http.StatusOK
	}
	s.log().Info("argus.webhook.accepted", "task", task.ID, "platform", platform,
		"kind", task.Kind, "head", task.HeadSHA, "event_id", delivery)
	if s.Audit != nil {
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "platform", platform,
			"owner", task.Owner, "repo", task.Repo, "number", task.Number, "source", "webhook")
	}
	s.publishCreated(task)
	return &api.WebhookResponse{Status: "accepted", EventId: strPtr(delivery), TaskId: strPtr(task.ID)}, http.StatusOK
}
