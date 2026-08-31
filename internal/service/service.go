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
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
	"github.com/yi-nology/git-platform-sdk/provider"

	api "argus/biz/model/api"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/storage"
	"argus/internal/workflow/requirement"
)

// Service 门面：biz handler 只跟它说话。
type Service struct {
	Cfg      *config.Config
	Store    *storage.Store
	SDK      *argusctx.SDKCtx
	Registry *plugin.Registry
	Log      Logger
	Version  string
	Bus      *progress.Bus // 任务进度事件（v3.1 SSE 数据源；nil 安全）
}

// Logger service 层的最小日志接口（logx.Logger 满足）。
type Logger interface {
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// ---------- 统一响应壳（工作区惯例）----------

type envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

func traceOf(c *app.RequestContext) string {
	if v := string(c.GetHeader("X-Trace-ID")); v != "" {
		return v
	}
	id, _ := uuid.NewRandom()
	return id.String()
}

// strPtr 可选字段指针化（hz 生成 DTO 的 optional 字段均为指针）。
func strPtr(s string) *string { return &s }

func ok(c *app.RequestContext, data any) {
	c.JSON(http.StatusOK, envelope{Code: 0, Data: data, Message: "ok", TraceID: traceOf(c)})
}

func fail(c *app.RequestContext, httpStatus, code int, msg string) {
	c.JSON(httpStatus, envelope{Code: code, Data: nil, Message: msg, TraceID: traceOf(c)})
}

// BearerMiddleware 🔒 路由的 Bearer 静态 token 校验（§5；webhook 不挂，靠平台签名）。
// fail-closed（v3.0.1 修订——原实现在 APIToken 未配置时直接放行，等于鉴权裸奔）：
// ARGUS_APITOKEN 未配置时 🔒 路由一律拒绝服务（webhook/健康检查不受影响），
// 配置 token 后立即恢复，无需重启之外的任何操作。
func (s *Service) BearerMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if s.Cfg.APIToken == "" {
			s.Log.Warn("argus.auth.rejected_no_token",
				"path", string(c.URI().Path()), "hint", "ARGUS_APITOKEN 未配置，🔒 路由拒绝服务")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, envelope{
				Code:    50300,
				Message: "鉴权未配置：服务端未设置 ARGUS_APITOKEN，🔒 路由拒绝服务（webhook 与健康检查不受影响）",
				TraceID: traceOf(c),
			})
			return
		}
		auth := string(c.GetHeader("Authorization"))
		// 常数时间比较：与 SDK webhook 签名校验的纪律对齐（内部静态 token，纵深防御）
		if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+s.Cfg.APIToken)) == 1 {
			c.Next(ctx)
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, envelope{
			Code: 40100, Message: "缺少或错误的 Authorization: Bearer token", TraceID: traceOf(c),
		})
	}
}

// ---------- webhook（POST /webhook/:platform）----------

// 事件类型白名单（§5.1 规则 1）。注意 Type = "cr." + 平台原生 action 透传，按平台词表维护；
// GitLab 的 MR action 词表不同（cr.open/cr.update 等），接入 GitLab 前必须先核词表（design §5.1）。
var crEventWhitelist = map[string]string{
	"cr.opened":       "opened",
	"cr.synchronize":  "synchronize",
	"cr.synchronized": "synchronized",
	"cr.reopened":     "reopened",
}

// Webhook 平台 webhook 入口：返回 (IDL 响应体, HTTP 状态码)。
func (s *Service) Webhook(ctx context.Context, c *app.RequestContext, platform string) (*api.WebhookResponse, int) {
	platform = strings.ToLower(platform)
	pcfg, configured := s.Cfg.Platforms[platform]
	if !configured || pcfg.Token == "" {
		return &api.WebhookResponse{Status: "error", Reason: strPtr("平台未接入: " + platform)}, http.StatusNotFound
	}
	// fail-closed（与 🔒 路由同一纪律，v3.0.2 起）：空 secret 时 SDK 跳过签名校验，
	// 该平台 webhook 等于无鉴权——任何能访问本端口的设备都能伪造事件烧 LLM 预算/
	// 发评论。此前只做启动期告警，请求期照常放行；现直接拒绝直到补配 ARGUS_<P>_SECRET。
	if pcfg.Secret == "" {
		s.Log.Warn("argus.webhook.rejected_no_secret", "platform", platform,
			"hint", "ARGUS_"+strings.ToUpper(platform)+"_SECRET 未配置，webhook 拒绝服务（fail-closed）")
		return &api.WebhookResponse{
			Status: "error",
			Reason: strPtr("平台 webhook secret 未配置，拒绝服务（配置 " + "ARGUS_" + strings.ToUpper(platform) + "_SECRET 后恢复）"),
		}, http.StatusServiceUnavailable
	}

	// 幂等键：在 ParseWebhookEvent 之前从原始请求头取 delivery（§5.2 四轮评审修正）
	delivery := s.SDK.ExtractDeliveryID(func(key string) string {
		return string(c.GetHeader(key))
	})

	// 重建 *http.Request（SDK ParseWebhookEvent 需要 net/http 语义做签名校验+解析）
	rawBody, err := readBody(c)
	if err != nil {
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
			s.Log.Warn("argus.webhook.signature_rejected", "platform", platform)
			return &api.WebhookResponse{Status: "error", Reason: strPtr("签名校验失败")}, http.StatusUnauthorized
		}
		return &api.WebhookResponse{Status: "error", Reason: strPtr("事件解析失败: " + err.Error())}, http.StatusBadRequest
	}

	// 幂等命中：平台重发 → 返回既有任务（不重复烧 LLM，§5.2）
	if delivery != "" {
		if exist, err := s.Store.GetTaskByEventID(ctx, delivery); err == nil && exist != nil {
			s.Log.Info("argus.webhook.duplicate_delivery", "event_id", delivery, "task", exist.ID)
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
		s.Log.Warn("argus.webhook.delivery_header_missing",
			"platform", platform, "event", "幂等键缺失，本次投递不做幂等（event_id=NULL）")
	}
	task := &storage.Task{
		ID:        newTaskID(),
		Kind:      requirement.KindRequirementCompliance,
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
		s.Log.Error("argus.webhook.task_create_failed", "error", err.Error())
		return &api.WebhookResponse{Status: "error", Reason: strPtr("任务落库失败")}, http.StatusInternalServerError
	}
	if dupReason == "last_head_sha" {
		return &api.WebhookResponse{Status: "duplicate", Reason: strPtr("同 head SHA 已审查过"), EventId: strPtr(delivery)}, http.StatusOK
	}
	if dupReason == "in_flight" {
		resp := &api.WebhookResponse{Status: "duplicate", Reason: strPtr("同 head SHA 任务执行中"), EventId: strPtr(delivery)}
		if existingID != "" {
			resp.TaskId = strPtr(existingID)
		}
		return resp, http.StatusOK
	}

	s.Log.Info("argus.webhook.accepted", "task", task.ID, "platform", platform,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number, "head", headSHA, "event_id", delivery)
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
	case requirement.KindRequirementCompliance, requirement.KindCodeReview, requirement.KindVisualCheck:
	default:
		return nil, http.StatusBadRequest
	}
	platform = strings.ToLower(platform)
	if pc, ok := s.Cfg.Platforms[platform]; !ok || pc.Token == "" {
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
		ID:            newTaskID(),
		Kind:          kind,
		Platform:      platform,
		Owner:         owner,
		Repo:          repo,
		Number:        number,
		Status:        storage.StatusPending,
		ArtifactsJSON: string(artifactsJSON),
		CreatedAt:     time.Now(),
	}
	if err := s.Store.CreateTask(ctx, task); err != nil {
		s.Log.Error("argus.api.task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.publishCreated(task)

	s.Log.Info("argus.api.task_created", "task", task.ID, "kind", kind,
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
		s.Log.Error("argus.api.list_tasks_failed", "error", err.Error())
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
	platform := strings.ToLower(req.Platform)
	if pc, ok := s.Cfg.Platforms[platform]; !ok || pc.Token == "" {
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
		fp      string
		file    = str(req.File)
		comment = str(req.Comment)
	)
	switch {
	case str(req.Fingerprint) != "":
		// 模式一：报告意见行的 fp 短码直达（前缀解析完整指纹）
		full, found := s.Store.ResolvePrintFingerprint(ctx, platform, req.Owner, req.Repo, req.Number, str(req.Fingerprint))
		if !found {
			return &api.SuppressFindingResponse{Matched: false, Fingerprint: str(req.Fingerprint)}, http.StatusOK
		}
		fp = full
	case file != "" && comment != "":
		// 模式二：file + 意见原文（与 Merger 同一指纹函数）
		fp = plugin.Fingerprint(file, comment)
	default:
		return nil, http.StatusBadRequest // fingerprint 或 (file+comment) 二选一
	}

	matched, err := s.Store.SuppressPrint(ctx, platform, req.Owner, req.Repo, req.Number,
		fp, plugin.Finding{File: file, Comment: comment}, time.Now())
	if err != nil {
		s.Log.Error("argus.api.suppress_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.Log.Info("argus.api.suppressed", "pr", req.Owner+"/"+req.Repo+"#"+req.Number,
		"file", file, "matched", matched, "fp", fp[:8])
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
		if isNotFound(err) {
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
		if isNotFound(err) {
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
	platform = strings.ToLower(platform)
	if pc, ok := s.Cfg.Platforms[platform]; !ok || pc.Token == "" {
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
	for i, rule := range rc.Routing {
		view.Routing = append(view.Routing, &api.RoutingRuleView{
			Paths: rule.Paths, Labels: rule.Labels, Use: rule.Use,
		})
		_ = i
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

func newTaskID() string { return storage.NewTaskID() }

func kindFromIDL(k api.TaskKind) (string, error) {
	switch k {
	case api.TaskKind_REQUIREMENT_COMPLIANCE:
		return requirement.KindRequirementCompliance, nil
	case api.TaskKind_CODE_REVIEW:
		return requirement.KindCodeReview, nil
	case api.TaskKind_VISUAL_CHECK:
		return requirement.KindVisualCheck, nil
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

func readBody(c *app.RequestContext) ([]byte, error) {
	body := c.Request.Body()
	if len(body) == 0 {
		return nil, fmt.Errorf("空 body")
	}
	return body, nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "record not found")
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
