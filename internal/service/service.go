// Package service handler 的薄实现层：绑定请求 → 调编排 → 统一响应壳 {code,data,message,trace_id}。
// webhook 同步路径实现 §5.1 全部过滤规则 + §5.2 幂等；异步执行交给 worker pool（§10.4 DB 即队列）。
package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yi-nology/git-platform-sdk/provider"
	"gorm.io/gorm"

	"git.example.com/enjoydream/ekit/concurrency/async"
	mw "git.example.com/enjoydream/ekit/middleware"

	api "argus/biz/model/api"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/qa"
	"argus/internal/storage"
	"git.example.com/enjoydream/agentkit/audit"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/safejson"
)

// Argus-specific audit actions (agentkit/audit provides the Logger, actions are domain-specific).
const (
	auditTaskCreate      audit.Action = "task.create"
	auditFindingSuppress audit.Action = "finding.suppress"
)

// Service 门面：biz handler 只跟它说话。
type Service struct {
	Cfg       *config.Config
	Audit     *audit.Logger // 审计日志（nil 安全）
	Store     *storage.Store
	SDK       *argusctx.SDKCtx
	Registry  *plugin.Registry
	Log       Logger
	Version   string
	Bus       *progress.Bus        // 任务进度事件（v3.1 SSE 数据源；nil 安全）
	Knowledge rag.KnowledgeService // 知识库（误报反馈回流；nil/Noop = 只抑制不回流）
	QAModel   model.BaseChatModel  // Q&A 专用模型（P6 覆盖；nil = 用 Model）
	Model     model.BaseChatModel  // LLM 模型（Q&A 直接调用；nil = Q&A 禁用）
	// RouterModel 评论意图路由分类模型（v3.4 ARGUS_ROUTER_MODEL 覆盖；nil = QAModel → Model）。
	// 仅 ARGUS_COMMENT_ROUTER=1 时消费。
	RouterModel model.BaseChatModel

	// Q&A 节流：per PR 30 秒冷却，防评论风暴。
	qaThrottleMu     sync.Mutex
	qaThrottles      map[string]time.Time // PR 级 30s 节流
	qaSeenDeliveries map[string]time.Time // delivery ID 幂等（防平台重试）
}

// Logger service 层的最小日志接口（logx.Logger 满足）。
type Logger interface {
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// noopLogger 未配置日志时的安全兜底（避免 nil 接口方法调用 panic）。
type noopLogger struct{}

func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Error(string, ...any) {}

// log 返回安全 Logger（nil → noopLogger）。
func (s *Service) log() Logger {
	if s.Log != nil {
		return s.Log
	}
	return noopLogger{}
}

// ---------- 统一响应壳（ekit mw：{code,data,message,trace_id}）----------
// trace_id 由 ekit traceid.Middleware 注入（main 注册），mw 响应函数自动携带。

// strPtr 可选字段指针化（hz 生成 DTO 的 optional 字段均为指针）。
func strPtr(s string) *string { return &s }

// BearerMiddleware 🔒 路由的 Bearer 静态 token 校验（§5；webhook 不挂，靠平台签名）。
// fail-closed（v3.0.1 修订——原实现在 APIToken 未配置时直接放行，等于鉴权裸奔）：
// ARGUS_APITOKEN 未配置时 🔒 路由一律拒绝服务（webhook/健康检查不受影响），
// 配置 token 后立即恢复，无需重启之外的任何操作。
func (s *Service) BearerMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if s.Cfg.APIToken == "" {
			s.log().Warn("argus.auth.rejected_no_token",
				"path", string(c.URI().Path()), "hint", "ARGUS_APITOKEN 未配置，🔒 路由拒绝服务")
			mw.ErrorResp(c, http.StatusServiceUnavailable,
				"鉴权未配置：服务端未设置 ARGUS_APITOKEN，🔒 路由拒绝服务（webhook 与健康检查不受影响）")
			return
		}
		auth := string(c.GetHeader("Authorization"))
		// 常数时间比较：与 SDK webhook 签名校验的纪律对齐（内部静态 token，纵深防御）
		if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+s.Cfg.APIToken)) == 1 {
			c.Next(ctx)
			return
		}
		mw.ErrorResp(c, http.StatusUnauthorized, "缺少或错误的 Authorization: Bearer token")
	}
}

// ---------- webhook（POST /webhook/:platform）----------

// 事件类型白名单（§5.1 规则 1）。注意 Type = "cr." + 平台原生 action 透传，按平台词表维护；
// GitLab 的 MR action 词表不同（cr.open/cr.update 等），接入 GitLab 前必须先核词表（design §5.1）。
var crEventWhitelist = map[string]string{
	// Gitea / GitHub
	"cr.opened":       "opened",
	"cr.synchronize":  "synchronize",
	"cr.synchronized": "synchronized",
	"cr.reopened":     "reopened",
	// GitLab MR actions ("cr." + action, SDK 透传)
	"cr.open":       "open",
	"cr.close":      "close",
	"cr.reopen":     "reopen",
	"cr.update":     "update",
	"cr.merged":     "merged",
	"cr.approved":   "approved",
	"cr.unapproved": "unapproved",
}

// Webhook 平台 webhook 入口：返回 (IDL 响应体, HTTP 状态码)。
func (s *Service) Webhook(ctx context.Context, c *app.RequestContext, platform string) (*api.WebhookResponse, int) {
	platform, pcfg, ok := s.requirePlatform(platform)
	if !ok {
		return &api.WebhookResponse{Status: "error", Reason: strPtr("平台未接入: " + platform)}, http.StatusNotFound
	}
	// fail-closed（与 🔒 路由同一纪律，v3.0.2 起）：空 secret 时 SDK 跳过签名校验，
	// 该平台 webhook 等于无鉴权——任何能访问本端口的设备都能伪造事件烧 LLM 预算/
	// 发评论。此前只做启动期告警，请求期照常放行；现直接拒绝直到补配 ARGUS_<P>_SECRET。
	if pcfg.Secret == "" {
		s.log().Warn("argus.webhook.rejected_no_secret", "platform", platform,
			"hint", "ARGUS_"+strings.ToUpper(platform)+"_SECRET 未配置，webhook 拒绝服务（fail-closed）")
		return &api.WebhookResponse{
			Status: "error",
			Reason: strPtr("平台 webhook secret 未配置，拒绝服务（配置 " + "ARGUS_" + strings.ToUpper(platform) + "_SECRET 后恢复）"),
		}, http.StatusServiceUnavailable
	}

	// Content-Length 预检：body 读取前拒绝超限请求，节省内存分配。
	// Hertz WithMaxRequestBodySize 作为第二道防线（框架层拦截）。
	if s.Cfg.WebhookMaxBody > 0 {
		if cl := string(c.GetHeader("Content-Length")); cl != "" {
			if n, err := strconv.Atoi(cl); err == nil && n > s.Cfg.WebhookMaxBody {
				return &api.WebhookResponse{
					Status: "error",
					Reason: strPtr(fmt.Sprintf("请求体过大: %d > %d bytes", n, s.Cfg.WebhookMaxBody)),
				}, http.StatusRequestEntityTooLarge
			}
		}
	}

	// 幂等键：在 ParseWebhookEvent 之前从原始请求头取 delivery（§5.2 四轮评审修正）
	delivery := s.SDK.ExtractDeliveryID(func(key string) string {
		return string(c.GetHeader(key))
	})

	// 重建 *http.Request（SDK ParseWebhookEvent 需要 net/http 语义做签名校验+解析）
	rawBody := c.Request.Body()
	if len(rawBody) == 0 {
		return &api.WebhookResponse{Status: "error", Reason: strPtr("读取 body 失败")}, http.StatusBadRequest
	}
	httpReq, err := http.NewRequest(string(c.Method()), string(c.URI().Path()), bytes.NewReader(rawBody))
	if err != nil {
		return &api.WebhookResponse{Status: "error", Reason: strPtr("内部请求构造失败")}, http.StatusInternalServerError
	}
	c.Request.Header.VisitAll(func(k, v []byte) {
		httpReq.Header.Add(string(k), string(v))
	})

	ev, err := s.SDK.ParseWebhook(platform, httpReq, pcfg.Secret)
	if err != nil {
		if errors.Is(err, provider.ErrWebhookValidation) {
			// §10.2：签名校验失败 → 401 拒绝（不产生 task）
			s.log().Warn("argus.webhook.signature_rejected", "platform", platform)
			return &api.WebhookResponse{Status: "error", Reason: strPtr("签名校验失败")}, http.StatusUnauthorized
		}
		return &api.WebhookResponse{Status: "error", Reason: strPtr("事件解析失败: " + err.Error())}, http.StatusBadRequest
	}

	// 幂等命中：平台重发 → 返回既有任务（不重复烧 LLM，§5.2）
	if delivery != "" {
		if exist, err := s.Store.GetTaskByEventID(ctx, delivery); err == nil && exist != nil {
			s.log().Info("argus.webhook.duplicate_delivery", "event_id", delivery, "task", exist.ID)
			return &api.WebhookResponse{
				Status:  "duplicate",
				Reason:  strPtr("重复投递，命中既有任务"),
				EventId: strPtr(delivery),
				TaskId:  strPtr(exist.ID),
			}, http.StatusOK
		}
	}

	skipped := func(reason string) (*api.WebhookResponse, int) {
		return &api.WebhookResponse{Status: "skipped", Reason: strPtr(reason), EventId: strPtr(delivery)}, http.StatusOK
	}

	// GitHub App 安装事件：记录安装信息，不建审查任务
	if ev.Type == "installation" || ev.Type == "installation_repositories" {
		return s.handleInstallationEvent(ev, delivery), http.StatusOK
	}

	// Q&A 评论路由：issue_comment 事件中含 @argus ask → 直接 LLM 回答（不建 task）。
	// 异步执行（深挖 P0）：ReAct 可达数分钟，同步跑会吃满平台 webhook 超时——
	// 断连取消 ctx 杀掉 ReAct，平台重试又撞 30s 节流，用户永无回复。挂到
	// Background ctx（与请求生命周期解耦）+ GoSafe 防 panic；delivery 幂等/
	// per-PR 节流都在 HandleComment 内部，重复投递安全。
	if isCommentEvent(ev) {
		async.GoSafe(func() {
			s.HandleComment(context.Background(), ev, delivery)
		})
		return &api.WebhookResponse{Status: "accepted", EventId: strPtr(delivery), Reason: strPtr("qa comment")}, http.StatusOK
	}

	// §5.1 规则 1：事件类型白名单（防评论风暴、防自我触发循环）
	if _, ok := crEventWhitelist[ev.Type]; !ok {
		return skipped("事件类型不在白名单: " + ev.Type)
	}
	// 规则 2：bot 自审保护
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.Cfg.BotUsername {
		return skipped("bot 自身事件，跳过")
	}
	// 规则 3：draft 默认不审；缺 CR/Repo 信息的异常事件防御性跳过（长跑服务不 panic）
	if ev.CR == nil || ev.Repo == nil {
		return skipped("事件缺少 CR/Repo 信息")
	}
	if ev.CR.Draft && s.Cfg.SkipDraft {
		return skipped("draft PR 默认不审（ARGUS_SKIP_DRAFT=true）")
	}

	// 组装任务记录（Kind 固定走需求复核 workflow；PR Bot 形态）
	headSHA := ev.CR.HeadSHA
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
		HeadSHA:   headSHA,
		EventType: ev.Type,
		Status:    storage.StatusPending,
		CreatedAt: time.Now(),
	}
	if delivery != "" {
		task.EventID = &delivery
	}

	// 规则 4：同 head SHA 去重（查询与落库同一事务，防 in-flight 竞态/TOCTOU）
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
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number, "head", headSHA, "event_id", delivery)
	if s.Audit != nil {
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "platform", platform,
			"owner", task.Owner, "repo", task.Repo, "number", task.Number, "source", "webhook")
	}
	s.publishCreated(task)
	return &api.WebhookResponse{Status: "accepted", EventId: strPtr(delivery), TaskId: strPtr(task.ID)}, http.StatusOK
}

// ---------- TaskAPI ----------

// CreateTask CLI/其他系统手动提交审查任务（🔒）。与 webhook 任务同语义（§5.1 规则 5）。
func (s *Service) CreateTask(ctx context.Context, req *api.CreateTaskRequest) (*api.CreateTaskResponse, int) {
	kind, err := kindFromIDL(req.Kind)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	return s.CreateTaskDirect(ctx, kind, req.Platform, req.Owner, req.Repo, req.Number, map[string]string(req.Artifacts))
}

// CreateTaskDirect 核心建任务（kind 为字符串词表），供 handler 与 MCP server 共用。
func (s *Service) CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
	artifacts map[string]string) (*api.CreateTaskResponse, int) {
	switch kind {
	case plugin.TaskKindRequirementCompliance, plugin.TaskKindCodeReview, plugin.TaskKindVisualCheck:
	default:
		return nil, http.StatusBadRequest
	}
	if _, _, ok := s.requirePlatform(platform); !ok {
		return nil, http.StatusBadRequest
	}
	if owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	// number 必须是纯十进制数字（平台 PR 序号）；strconv.Atoi 会放过 "+123" 等带符号写法，
	// 入库后运行期平台查询才失败。
	if !isDigits(number) {
		return nil, http.StatusBadRequest
	}
	artifactsJSON, _ := json.Marshal(artifacts)
	task := &storage.Task{
		ID:            storage.NewTaskID(),
		Kind:          kind,
		Platform:      platform,
		Owner:         owner,
		Repo:          repo,
		Number:        number,
		Status:        storage.StatusPending,
		ArtifactsJSON: string(artifactsJSON),
		CreatedAt:     time.Now(),
	}
	existing, created, err := s.Store.CreateTaskDirectIfIdle(ctx, task)
	if err != nil {
		s.log().Error("argus.api.task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	if !created {
		// 同 PR 已有在飞任务：幂等返回既有任务，不再叠加一份重复审查（token 白烧）。
		s.log().Info("argus.api.task_create_deduped", "existing", existing.ID,
			"status", existing.Status, "pr", owner+"/"+repo+"#"+number)
		return &api.CreateTaskResponse{TaskId: existing.ID, Status: existing.Status}, http.StatusOK
	}
	s.publishCreated(task)

	s.log().Info("argus.api.task_created", "task", task.ID, "kind", kind,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number)
	return &api.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// ListTasks 任务列表（🔒）：status 过滤 + limit 收口，created_at 倒序。
func (s *Service) ListTasks(ctx context.Context, status string, limit int) (*api.ListTasksResponse, int) {
	switch status {
	case "", storage.StatusPending, storage.StatusRunning, storage.StatusDone,
		storage.StatusFailed, storage.StatusStale:
	default:
		return nil, http.StatusBadRequest
	}
	tasks, err := s.Store.ListTasks(ctx, status, limit)
	if err != nil {
		s.log().Error("argus.api.list_tasks_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	// 预分配空切片：nil 会被序列化成 "tasks":null，与 IDL required list 语义不符
	resp := &api.ListTasksResponse{Tasks: []*api.TaskSummary{}}
	for i := range tasks {
		t := &tasks[i]
		item := &api.TaskSummary{
			TaskId:    t.ID,
			Status:    t.Status,
			Kind:      t.Kind,
			Platform:  t.Platform,
			Owner:     t.Owner,
			Repo:      t.Repo,
			Number:    t.Number,
			CreatedAt: t.CreatedAt.Unix(),
		}
		if t.HeadSHA != "" {
			item.HeadSha = strPtr(t.HeadSHA)
		}
		if t.Verdict != "" {
			item.Verdict = strPtr(t.Verdict)
		}
		resp.Tasks = append(resp.Tasks, item)
	}
	return resp, http.StatusOK
}

// SuppressFinding 误报反馈（🔒，设计 §3.3 反馈通道最小入口）：
// 服务端按 Merger 同一指纹函数计算（file + 规范化评论文本哈希，行号不入指纹），
// 置 suppressed 后每轮报告自动剔除该意见。下一轮审查起生效（总结评论不即时重渲）。
func (s *Service) SuppressFinding(ctx context.Context, req *api.SuppressFindingRequest) (*api.SuppressFindingResponse, int) {
	platform, _, ok := s.requirePlatform(req.Platform)
	if !ok {
		return nil, http.StatusBadRequest
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	if req.Number == "" {
		return nil, http.StatusBadRequest
	}

	var (
		fp           string
		file         = str(req.File)
		comment      = str(req.Comment)
		pluginName   string
		matched      bool
		suppressDone bool
	)
	switch {
	case str(req.Fingerprint) != "":
		// 模式一：报告意见行的 fp 短码直达（前缀解析完整指纹，含别名回映射）
		full, by, found := s.Store.ResolvePrintWithPlugin(ctx, platform, req.Owner, req.Repo, req.Number, str(req.Fingerprint))
		if !found {
			return &api.SuppressFindingResponse{Matched: false, Fingerprint: str(req.Fingerprint)}, http.StatusOK
		}
		fp = full
		pluginName = by
	case file != "" && comment != "":
		// 模式二：file + 意见原文（v3.7.0）——v2 指纹含行号，原文无法直算；
		// 走近重复检索抑制既有条目（措辞/行号漂移容忍），未命中预插 suppressed 行
		var err2 error
		if fp, matched, err2 = s.Store.SuppressPrintNear(ctx, platform, req.Owner, req.Repo, req.Number,
			file, comment, time.Now()); err2 != nil {
			s.log().Error("argus.api.suppress_near_failed", "error", err2.Error())
			return nil, http.StatusInternalServerError
		}
		suppressDone = true
	default:
		return nil, http.StatusBadRequest // fingerprint 或 (file+comment) 二选一
	}

	if !suppressDone {
		var err error
		matched, err = s.Store.SuppressPrint(ctx, platform, req.Owner, req.Repo, req.Number,
			fp, plugin.Finding{File: file, Comment: comment, By: pluginName}, time.Now())
		if err != nil {
			s.log().Error("argus.api.suppress_failed", "error", err.Error())
			return nil, http.StatusInternalServerError
		}
	}
	if matched {
		// 知识飞轮（v3.3）：误报结论回流知识库——R3 search_knowledge 命中
		// "本仓库历史误报"即带抑制上下文，同型误报率随使用下降
		s.recordFalsePositiveKnowledge(ctx, platform, req.Owner, req.Repo, req.Number, fp, file, comment)
	}
	// 学习环：人工判误报 → PatternSuppress（失败只告警）
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if err := s.Store.PatternSuppress(ctx, platform, req.Owner, req.Repo, pluginName,
			plugin.Finding{File: file, Comment: comment, By: pluginName}, time.Now()); err != nil {
			s.log().Warn("argus.regret.pattern_suppress_failed", "error", err.Error())
		}
	}
	s.log().Info("argus.api.suppressed", "pr", req.Owner+"/"+req.Repo+"#"+req.Number,
		"file", file, "matched", matched, "fp", fp[:8])
	if s.Audit != nil {
		s.Audit.Log(auditFindingSuppress,
			"platform", platform, "owner", req.Owner, "repo", req.Repo, "number", req.Number,
			"file", file, "fingerprint", fp[:8], "matched", matched)
	}
	return &api.SuppressFindingResponse{Matched: matched, Fingerprint: fp}, http.StatusOK
}

// publishCreated 广播 task.created（nil Bus 安全）。
func (s *Service) publishCreated(t *storage.Task) {
	if s.Bus == nil || t == nil {
		return
	}
	s.Bus.Publish(progress.Event{
		Type: "task.created", TaskID: t.ID, Platform: t.Platform,
		Owner: t.Owner, Repo: t.Repo, Number: t.Number,
	})
}

// UsageMonthly 本月各仓库 token 用量（GET /usage/monthly；🔒，v3.1）。
func (s *Service) UsageMonthly(ctx context.Context) (map[string]any, int) {
	since := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	usage, err := s.Store.MonthlyTokensByRepo(ctx, since)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	items := make([]storage.RepoTokenUsage, 0, len(usage))
	var total int64
	for _, u := range usage {
		items = append(items, u)
		total += u.Tokens
	}
	return map[string]any{
		"month":  since.Format("2006-01"),
		"total":  total,
		"budget": s.Cfg.RepoMonthlyTokenBudget,
		"repos":  items,
	}, http.StatusOK
}

// GetReportByRef 按 head SHA 查最近一次成功报告（GET /reports/:ref；🔒，v3.1）。
func (s *Service) GetReportByRef(ctx context.Context, ref string) (*api.GetTaskResponse, int) {
	if ref == "" {
		return nil, http.StatusBadRequest
	}
	task, err := s.Store.FindReportByHead(ctx, ref)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	return &api.GetTaskResponse{
		TaskId:  task.ID,
		Status:  task.Status,
		Verdict: strPtr(task.Verdict),
		Report:  strPtr(task.Report),
	}, http.StatusOK
}

// GetTask 查询任务状态与结果（报告 markdown 内联返回，§5；🔒）。
func (s *Service) GetTask(ctx context.Context, taskID string) (*api.GetTaskResponse, int) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	resp := &api.GetTaskResponse{
		TaskId: task.ID,
		Status: task.Status,
	}
	if task.Verdict != "" {
		resp.Verdict = strPtr(task.Verdict)
	}
	if task.FailReason != "" {
		resp.FailReason = strPtr(task.FailReason)
	}
	if task.Report != "" {
		resp.Report = strPtr(task.Report)
	}
	if task.ResultJSON != "" {
		var payload struct {
			Findings []plugin.Finding `json:"findings"`
		}
		if err := json.Unmarshal([]byte(task.ResultJSON), &payload); err == nil {
			for _, f := range payload.Findings {
				by := f.By
				resp.Findings = append(resp.Findings, &api.Finding{
					File:     f.File,
					Line:     int32(f.Line),
					Severity: f.Severity,
					Comment:  f.Comment,
					By:       strPtr(by),
				})
			}
		}
	}
	if task.DebugJSON != "" {
		resp.Debug = strPtr(task.DebugJSON)
	}
	return resp, http.StatusOK
}

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
	for _, w := range warnings {
		view.Warnings = append(view.Warnings, w)
	}
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

func strPtrOrEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------- helpers ----------

// requirePlatform 平台准入检查：规范化 platform 名并返回 (pcfg, true) 表示已接入且有凭证（PAT 或 GitHub App）。
func (s *Service) requirePlatform(platform string) (string, *config.PlatformConfig, bool) {
	platform = strings.ToLower(platform)
	pc, ok := s.Cfg.Platforms[platform]
	if !ok {
		return platform, nil, false
	}
	// GitHub App installation token 也算已配置
	if pc.Token == "" && !pc.GitHubAppEnabled() {
		return platform, nil, false
	}
	return platform, &pc, true
}

func kindFromIDL(k api.TaskKind) (string, error) {
	switch k {
	case api.TaskKind_REQUIREMENT_COMPLIANCE:
		return plugin.TaskKindRequirementCompliance, nil
	case api.TaskKind_CODE_REVIEW:
		return plugin.TaskKindCodeReview, nil
	case api.TaskKind_VISUAL_CHECK:
		return plugin.TaskKindVisualCheck, nil
	default:
		return "", fmt.Errorf("未知任务类型 %d", int(k))
	}
}

func kindToIDL(k plugin.Kind) api.AgentKind {
	switch k {
	case plugin.KindCLI:
		return api.AgentKind_CLI
	case plugin.KindRemote:
		return api.AgentKind_REMOTE
	default:
		return api.AgentKind_BUILTIN
	}
}

func protocolToIDL(p plugin.Protocol) api.AgentProtocol {
	switch p {
	case plugin.ProtocolA2A:
		return api.AgentProtocol_A2A
	case plugin.ProtocolMCP:
		return api.AgentProtocol_MCP
	default:
		return api.AgentProtocol_NONE
	}
}

// handleInstallationEvent 处理 GitHub App installation / installation_repositories 事件。
// 记录安装信息到日志（安装 ID、账号、仓库列表），不建审查任务。
// SDK 的 ParseWebhookEvent 对 installation 事件只填充 Type + RawPayload，
// 需要从原始 payload 提取结构化信息。
func (s *Service) handleInstallationEvent(ev *provider.NormalizedEvent, delivery string) *api.WebhookResponse {
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
			RepositorySelection string `json:"repository_selection"`
		} `json:"installation"`
		Repositories []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(ev.RawPayload, &payload); err != nil {
		s.log().Warn("argus.installation.parse_failed", "error", err.Error())
		return &api.WebhookResponse{Status: "skipped", Reason: strPtr("installation 事件解析失败"), EventId: strPtr(delivery)}
	}

	repoNames := make([]string, 0, len(payload.Repositories))
	for _, r := range payload.Repositories {
		repoNames = append(repoNames, r.FullName)
	}

	s.log().Info("argus.installation.event",
		"action", payload.Action,
		"installation_id", payload.Installation.ID,
		"account", payload.Installation.Account.Login,
		"account_type", payload.Installation.Account.Type,
		"repository_selection", payload.Installation.RepositorySelection,
		"repos", repoNames,
		"event_id", delivery,
	)
	if s.Audit != nil {
		s.Audit.Log("github_app_installation",
			"action", payload.Action,
			"installation_id", payload.Installation.ID,
			"account", payload.Installation.Account.Login,
			"repos", repoNames,
		)
	}
	return &api.WebhookResponse{
		Status:  "accepted",
		Reason:  strPtr("installation event logged"),
		EventId: strPtr(delivery),
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------- Q&A 评论交互（@argus ask）----------

// recordFalsePositiveKnowledge 误报结论回流知识库（Milvus 模式；Local 模式跳过——
// configmap 只读目录不可写，且跨仓库泛化本就需要向量语义）。内容经 safejson
// 转义（半可信：包含开发者手输的反馈语境）。幂等：同 fp 重复反馈重复入库无碍
// （检索打分摊薄），主键确定性 file#chunk_N 覆盖旧行。
func (s *Service) recordFalsePositiveKnowledge(ctx context.Context, platform, owner, repo, number, fp, file, comment string) {
	ms, ok := s.Knowledge.(*rag.MilvusStore)
	if !ok {
		return
	}
	var b strings.Builder
	b.WriteString("## 历史误报记录（开发者反馈）\n\n")
	b.WriteString(fmt.Sprintf("- 仓库：%s/%s#%s\n- 文件：%s\n- 指纹：%s\n\n", owner, repo, number, file, fp))
	b.WriteString("原审查意见：\n\n> " + safejson.EscapeUntrusted(comment) + "\n\n")
	b.WriteString("开发者判定：以上意见为**误报**，同型问题在本仓库语境下不成立；检索到本条时请下调该类问题的置信度并核对历史语境。")
	if err := ms.Index(ctx, fmt.Sprintf("feedback/%s/%s#%s/%s", owner, repo, number, fp[:8]), b.String()); err != nil {
		s.log().Warn("argus.feedback.knowledge_failed", "fp", fp[:8], "error", err.Error())
		return
	}
	s.log().Info("argus.feedback.knowledge_recorded", "fp", fp[:8], "repo", owner+"/"+repo)
	s.Audit.Log("feedback.suppressed", "repo", owner+"/"+repo, "fp", fp[:8])
}

// qa 组装问答引擎（v3.5.6 Q&A 逻辑拆至 internal/qa）。Engine 无自有状态，
// 字段全为共享引用；会话历史在 Store，节流与 delivery 幂等留在本层 HandleComment。
func (s *Service) qa() *qa.Engine {
	return &qa.Engine{
		SDK:       s.SDK,
		Store:     s.Store,
		Knowledge: s.Knowledge,
		Model:     s.Model,
		QAModel:   s.QAModel,
		Log:       s.log(),
	}
}

// LatestReview 某 PR 最近一次审查结论（mcpserver ask_argus 数据源）。
// 返回 {task_id, finished_at, verdict, findings, report}；无记录返回 (nil, false)。
func (s *Service) LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	return s.qa().LatestReview(ctx, platform, owner, repo, number)
}

// AnswerAboutReview 基于最近一次报告回答追问（ask_argus 带 question 时走这里；
// 单轮直调——报告已在手，无需工具链）。模型未配置或无报告返回 ("" , false)。
func (s *Service) AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	return s.qa().AnswerAboutReview(ctx, platform, owner, repo, number, question)
}

// qaCommentPayload 解析 issue_comment webhook 原始负载中我们关心的字段。
// 仅用于从 RawPayload 提取评论正文与 PR 信息，不做全量反序列化。
type qaCommentPayload struct {
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	Issue struct {
		Number int `json:"number"`
		// GitHub 与 Gitea/Forgejo 的 pull_request 元数据字段集不完全一致，
		// 只取原始 JSON 判存在性（非空 = 这是 PR，不是普通 issue）。
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
}

// gitlabNotePayload GitLab note webhook 负载（object_kind=note）中我们关心的字段。
// MR 评论带 merge_request 对象（iid 即 MR 序号）；system=true 是平台自动生成的
// 系统注释（改指派/标签/状态等），没有用户意图，必须跳过。
type gitlabNotePayload struct {
	ObjectAttributes struct {
		Note   string `json:"note"`
		System bool   `json:"system"`
	} `json:"object_attributes"`
	MergeRequest struct {
		IID int `json:"iid"`
	} `json:"merge_request"`
}

// commentMeta 评论负载的归一化视图：两种平台形状折叠后的评论要素。
type commentMeta struct {
	Body     string
	PRNumber int
}

// parseCommentMeta 按事件类型解析评论负载。GitLab note → cr.note 形状；
// GitHub issue_comment 与 Gitea/Forgejo（Type 空 + comment 字段兜底）→ issue 形状。
// ok=false 表示解析失败或非 PR 评论（commit/snippet/普通 issue），调用方静默忽略。
func parseCommentMeta(ev *provider.NormalizedEvent) (commentMeta, bool) {
	if ev.Type == "cr.note" {
		var gl gitlabNotePayload
		if err := json.Unmarshal(ev.RawPayload, &gl); err != nil {
			return commentMeta{}, false
		}
		if gl.ObjectAttributes.System || gl.MergeRequest.IID == 0 {
			return commentMeta{}, false
		}
		return commentMeta{Body: gl.ObjectAttributes.Note, PRNumber: gl.MergeRequest.IID}, true
	}
	var pl qaCommentPayload
	if err := json.Unmarshal(ev.RawPayload, &pl); err != nil {
		return commentMeta{}, false
	}
	// pull_request 元数据字段非空 = 这是 PR（不是普通 issue）；Gitea 的该对象形状
	// 与 GitHub 不完全一致，因此只判存在性，不依赖具体字段。
	if len(pl.Issue.PullRequest) == 0 || string(pl.Issue.PullRequest) == "null" {
		return commentMeta{}, false
	}
	return commentMeta{Body: pl.Comment.Body, PRNumber: pl.Issue.Number}, true
}

// isCommentEvent 判定 NormalizedEvent 是否为可处理的评论事件。
// GitHub issue_comment → ev.Type == "issue_comment"；
// GitLab note → ev.Type == "cr.note"（SDK 归一化，Actor/Repo/CR.Number 已填充）；
// Gitea/Forgejo 未识别事件 → ev.Type 为空但 payload 含 comment 字段。
func isCommentEvent(ev *provider.NormalizedEvent) bool {
	if ev.Type == "issue_comment" || ev.Type == "cr.note" {
		return true
	}
	if ev.Type != "" {
		return false
	}
	// Gitea 后端不识别的事件类型 → Type 为空；用 payload 结构兜底判定。
	var probe struct {
		Comment *json.RawMessage `json:"comment"`
	}
	return len(ev.RawPayload) > 0 &&
		json.Unmarshal(ev.RawPayload, &probe) == nil &&
		probe.Comment != nil
}

// normalizeCommentEvent 从原始负载补齐评论事件的 Actor/Repo。
// 背景：git-platform-sdk 的 GitHub 后端对 issue_comment 走 default 分支只置 Type，
// 不填 Actor/Repo（v3.5.5 真机实测发现，评论链路因此静默早退）；Gitea/GitLab 后端
// 则在 switch 前统一填充。此处以负载为准兜底，使评论处理与 SDK 归一化程度解耦。
func normalizeCommentEvent(ev *provider.NormalizedEvent) {
	if len(ev.RawPayload) == 0 {
		return
	}
	if ev.Repo == nil {
		var probe struct {
			Repository struct {
				FullName string `json:"full_name"`
				Name     string `json:"name"`
				Owner    struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			owner, name := probe.Repository.Owner.Login, probe.Repository.Name
			if owner == "" || name == "" {
				// full_name = "owner/name" 兜底
				if i := strings.IndexByte(probe.Repository.FullName, '/'); i > 0 {
					if owner == "" {
						owner = probe.Repository.FullName[:i]
					}
					if name == "" {
						name = probe.Repository.FullName[i+1:]
					}
				}
			}
			if owner != "" && name != "" {
				ev.Repo = &provider.EventRepo{Owner: owner, Name: name}
			}
		}
	}
	if ev.Actor == nil {
		var probe struct {
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
			User struct {
				Username string `json:"username"`
				Login    string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			login := probe.Sender.Login
			if login == "" {
				login = probe.User.Login
			}
			if login == "" {
				login = probe.User.Username
			}
			if login != "" {
				ev.Actor = &provider.CRUser{Username: login}
			}
		}
	}
}

// HandleComment 处理 @argus ask 评论：提取问题 → 加载 PR 上下文 → LLM 回答 → 回帖。
// 轻量路径：不建 task、不入 DB、不走 worker pool。
func (s *Service) HandleComment(ctx context.Context, ev *provider.NormalizedEvent, delivery string) {
	if s.Model == nil {
		s.log().Warn("argus.qa.no_model", "hint", "LLM 未配置，Q&A 评论忽略")
		return
	}
	normalizeCommentEvent(ev)

	meta, ok := parseCommentMeta(ev)
	if !ok {
		return // 解析失败或非 PR 评论（系统注释/commit/普通 issue），静默忽略
	}

	// bot 自身评论防循环
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.Cfg.BotUsername {
		return
	}
	// delivery 幂等：防平台重试导致重复 LLM 调用 + 重复回复
	if delivery != "" {
		s.qaThrottleMu.Lock()
		if s.qaSeenDeliveries == nil {
			s.qaSeenDeliveries = make(map[string]time.Time)
		}
		if _, seen := s.qaSeenDeliveries[delivery]; seen {
			s.qaThrottleMu.Unlock()
			return
		}
		s.qaSeenDeliveries[delivery] = time.Now()
		// 清理旧条目
		if len(s.qaSeenDeliveries) > 200 {
			for k, t := range s.qaSeenDeliveries {
				if time.Since(t) > time.Hour {
					delete(s.qaSeenDeliveries, k)
				}
			}
		}
		s.qaThrottleMu.Unlock()
	}
	if ev.Repo == nil {
		return
	}

	question := extractQuestion(meta.Body, s.Cfg.BotUsername)
	if question == "" {
		return // 不含 @argus 指令，忽略
	}

	// /argus restore fp:xxxxxxxx —— 误抑制恢复（suppress 反向，误杀可撤销）：
	// 将 suppressed 指纹置回 open，后续轮次重新可见。评论轻量路径，不建任务。
	if fp, ok := extractRestoreFP(question); ok {
		s.handleRestoreComment(ctx, ev, meta.PRNumber, fp)
		return
	}

	// /argus suppress fp:xxxxxxxx —— 误报反馈（v3.3 知识飞轮入口）：
	// 抑制该指纹后续轮次 + 结论回流知识库。评论轻量路径，不建任务。
	if fp, ok := extractSuppressFP(question); ok {
		s.handleSuppressComment(ctx, ev, meta.PRNumber, fp, meta.Body)
		return
	}

	prNum := fmt.Sprintf("%d", meta.PRNumber)
	prRef := ev.Repo.Owner + "/" + ev.Repo.Name + "#" + prNum

	// per-PR 节流（30 秒冷却）
	throttleKey := ev.Repo.Owner + "/" + ev.Repo.Name + "/" + prNum
	s.qaThrottleMu.Lock()
	if s.qaThrottles == nil {
		s.qaThrottles = make(map[string]time.Time)
	}
	if last, ok := s.qaThrottles[throttleKey]; ok && time.Since(last) < 30*time.Second {
		s.qaThrottleMu.Unlock()
		s.log().Warn("argus.qa.throttled", "pr", prRef, "question", question)
		return
	}
	s.qaThrottles[throttleKey] = time.Now()
	// 防内存泄漏：map 超100 条时清理 1 小时前的旧条目
	if len(s.qaThrottles) > 100 {
		for k, t := range s.qaThrottles {
			if time.Since(t) > time.Hour {
				delete(s.qaThrottles, k)
			}
		}
	}
	s.qaThrottleMu.Unlock()

	s.log().Info("argus.qa.received", "pr", prRef, "question", question,
		"actor", ev.Actor.Username, "event_id", delivery)

	platform := string(ev.Source)

	// 意图路由（v3.4）：ARGUS_COMMENT_ROUTER=1 时先分类再分派——review 建全量
	// 复审任务，其余一律 Q&A（Fallback 也是 Q&A）。关闭时行为与 v3.3 完全一致。
	if !s.Cfg.CommentRouter {
		answer, err := s.qa().Answer(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, question)
		if err != nil {
			s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
			s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, "LLM 调用失败: "+err.Error())
			return
		}
		s.postCommentReply(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef,
			"> "+question+"\n\n"+answer+"\n\n<!-- argus:qa delivery="+delivery+" -->",
			question, len(answer))
		return
	}

	reply, err := s.routeAndReply(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, question, delivery)
	if err != nil {
		s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, "处理失败: "+err.Error())
		return
	}
	s.postCommentReply(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, reply, question, len(reply))
}

// postCommentReply 回帖 + 留痕（Q&A 与意图路由分派的共用出口）。
func (s *Service) postCommentReply(ctx context.Context, platform, owner, repo, prNum, prRef,
	body, question string, answerLen int) {
	if _, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, prNum, body); err != nil {
		s.log().Error("argus.qa.reply_failed", "pr", prRef, "error", err.Error())
		return
	}
	s.log().Info("argus.qa.replied", "pr", prRef, "answer_len", answerLen)
	if s.Audit != nil {
		s.Audit.Log("qa_reply", "pr", prRef, "question", question, "answer_len", answerLen)
	}
}

// extractQuestion 从评论正文提取 @argus ask 后面的问题文本。
// 支持 "@argus ask ..." 和 "@argus ..." 两种格式；返回空串表示不匹配。
// extractSuppressFP 解析 "suppress fp:xxxxxxxx" 指令（fp 短码 ≥6 位十六进制）。
func extractSuppressFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "suppress"))
	if !strings.HasPrefix(strings.ToLower(rest), "fp:") {
		return "", false
	}
	fp := strings.TrimSpace(rest[len("fp:"):])
	if i := strings.IndexAny(fp, " \t\n"); i >= 0 {
		fp = fp[:i]
	}
	if len(fp) < 6 || len(fp) > 64 {
		return "", false
	}
	for _, r := range fp {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return "", false
		}
	}
	return strings.ToLower(fp), true
}

// handleSuppressComment 处理 MR 评论里的误报反馈：短码解析完整指纹 → 抑制入库 →
// 知识回流 → 回帖确认。复用 SuppressFinding 服务路径（同一套幂等与回流逻辑）。
func (s *Service) handleSuppressComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix, commentBody string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	req := &api.SuppressFindingRequest{
		Platform: platform, Owner: ev.Repo.Owner, Repo: ev.Repo.Name, Number: num,
		Fingerprint: strPtr(fpPrefix),
		Comment:     strPtr(commentBody),
	}
	resp, status := s.SuppressFinding(ctx, req)
	if resp == nil {
		s.log().Warn("argus.feedback.comment_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "status", status)
		return
	}
	reply := "已收到误报反馈。"
	if !resp.Matched {
		reply = "未找到匹配的意见指纹，请核对报告中 `fp:` 开头的短码后重试。"
	} else {
		reply = "已标记为误报：后续轮次自动抑制" + s.knowledgeSuffix() + "。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}

// knowledgeSuffix 报告尾注（知识回流是否生效取决于后端模式）。
func (s *Service) knowledgeSuffix() string {
	if _, ok := s.Knowledge.(*rag.MilvusStore); ok {
		return "，误报结论已回流知识库（同型问题检索时将带历史语境）"
	}
	return ""
}

func extractQuestion(body, botUsername string) string {
	// 先规整原文再取切片（深挖修复）：此前 lower 基于 TrimSpace 后的串、切片基于
	// 原始 body，前导空白时切点前移——产出乱码前缀甚至切进多字节 rune 中间
	body = strings.TrimSpace(body)
	lower := strings.ToLower(body)
	mention := "@" + strings.ToLower(botUsername)
	if !strings.HasPrefix(lower, mention) {
		return ""
	}
	rest := strings.TrimSpace(body[len(mention):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "ask"))
	return strings.TrimSpace(rest)
}

// postQAError 当 Q&A 流程失败时，向 PR 发一条错误说明（避免用户无响应）。
func (s *Service) postQAError(ctx context.Context, platform, owner, repo, number, msg string) {
	body := ":warning: **Argus Q&A 错误**: " + msg +
		"\n\n<!-- argus:qa error -->"
	if _, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, number, body); err != nil {
		s.log().Error("argus.qa.error_reply_failed", "error", err.Error())
	}
}

// extractRestoreFP 解析 "restore fp:xxxxxxxx" 指令（fp 短码 ≥6 位十六进制）。
// 与 extractSuppressFP 同形但独立实现：restore 指令不得被 suppress 分支截胡。
func extractRestoreFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "restore"))
	if !strings.HasPrefix(strings.ToLower(rest), "fp:") {
		return "", false
	}
	fp := strings.TrimSpace(rest[len("fp:"):])
	if i := strings.IndexAny(fp, " \t\n"); i >= 0 {
		fp = fp[:i]
	}
	if len(fp) < 6 || len(fp) > 64 {
		return "", false
	}
	for _, r := range fp {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return "", false
		}
	}
	return strings.ToLower(fp), true
}

// handleRestoreComment 处理 MR 评论里的恢复指令：短码解析 → 置回 open → 回帖确认。
// 与 handleSuppressComment 互为反向；不走知识回流（恢复不是误报结论）。
func (s *Service) handleRestoreComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	// 学习环：先解析插件归属，restore 后原行仍可查但状态已变
	var pluginName, fullFP, comment string
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if fp, by, ok := s.Store.ResolvePrintWithPlugin(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, fpPrefix); ok {
			fullFP, pluginName = fp, by
			if st, err := s.Store.GetPrintState(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, fp); err == nil && st != nil {
				// PrintState 无 Comment 字段，用 fingerprint 短码作为可追踪样本
				comment = "restore:" + fp[:8]
			}
		}
	}
	matched, err := s.Store.RestorePrint(ctx, platform, ev.Repo.Owner, ev.Repo.Name, number, fpPrefix)
	if err != nil {
		s.log().Warn("argus.feedback.restore_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "err", err)
		return
	}
	if matched && s.Cfg != nil && s.Cfg.RegretLearning && fullFP != "" {
		if err := s.Store.PatternRestore(ctx, platform, ev.Repo.Owner, ev.Repo.Name, pluginName,
			plugin.Finding{Comment: comment}, time.Now()); err != nil {
			s.log().Warn("argus.regret.pattern_restore_failed", "error", err.Error())
		}
	}
	reply := "已恢复：该意见后续轮次重新可见。"
	if !matched {
		reply = "未找到匹配的已抑制指纹（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}
