// Package service handler 的薄实现层：绑定请求 → 调编排 → 统一响应壳 {code,data,message,trace_id}。
// webhook 同步路径实现 §5.1 全部过滤规则 + §5.2 幂等；异步执行交给 worker pool（§10.4 DB 即队列）。
package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/yi-nology/go-git-platform/provider"

	"git.example.com/enjoydream/agentkit/workcopy"
	"git.example.com/enjoydream/ekit/concurrency/async"
	mw "git.example.com/enjoydream/ekit/middleware"

	api "argus/biz/model/api"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/memory"
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/qa"
	"argus/internal/storage"

	"git.example.com/enjoydream/agentkit/audit"
	"git.example.com/enjoydream/agentkit/fence"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
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

	// WC 工作副本池（v3.22.0 testgen 沙箱验证；nil = 验证不可用）。
	WC *workcopy.Pool

	// Q&A 节流：per PR 30 秒冷却，防评论风暴。
	qaThrottleMu sync.Mutex
	qaThrottles  map[string]time.Time // PR 级 30s 节流
	// qaEngine 问答引擎单例（Engine 无自有状态、字段全共享引用，会话在 Store）。
	// 此前每次 Ask 都重建，纯浪费；sync.Once 保证并发首建安全。
	qaOnce sync.Once
	qaEng  *qa.Engine

	// 评论链路资源闸（v3.8.0）：有界并发 + 超时 + 停机排水。此前评论事件挂
	// 无超时的 Background ctx、无并发上限、停机直接杀在途 ReAct（用户永无回复）。
	commentOnce  sync.Once
	commentSlots chan struct{}  // 有界并发（容量 Cfg.CommentConcurrency）
	commentWG    sync.WaitGroup // 在途评论事件（DrainComments 等待）
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

// initCommentGate 惰性初始化评论并发闸（容量来自 Cfg.CommentConcurrency）。
func (s *Service) initCommentGate() {
	s.commentOnce.Do(func() {
		n := s.Cfg.CommentConcurrency
		if n <= 0 {
			n = 2
		}
		s.commentSlots = make(chan struct{}, n)
	})
}

// SubmitComment 评论事件入口（v3.8.0 治理）：有界并发 + 整体超时 + 停机排水。
// ctx 治理：WithoutCancel 保留请求 ctx 的 trace_id（此前 Background 直接丢弃），
// 同时脱钩请求生命周期（平台 10s 断连不再杀在途 ReAct）；再包 CommentTimeout
// 整体上界（此前理论可达 30 分钟级、无任何 deadline）。
// 返回 false = 并发闸满载（调用方回"忙"，不排队——排队会把 webhook 线程池拖垮）。
func (s *Service) SubmitComment(requestCtx context.Context, ev *provider.NormalizedEvent, delivery string) bool {
	s.initCommentGate()
	select {
	case s.commentSlots <- struct{}{}:
	default:
		obs.IncrCommentBusy()
		return false
	}
	timeout := s.Cfg.CommentTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), timeout)
	s.commentWG.Add(1)
	async.GoSafe(func() {
		defer cancel()
		defer s.commentWG.Done()
		defer func() { <-s.commentSlots }()
		s.HandleComment(ctx, ev, delivery)
	})
	return true
}

// DrainComments 停机排水：等待在途评论事件收尾（最多 timeout，超时放行——
// 与 worker 两阶段停机同一哲学，避免卡死整个 shutdown）。
func (s *Service) DrainComments(timeout time.Duration) {
	s.initCommentGate()
	done := make(chan struct{})
	go func() {
		s.commentWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.log().Warn("argus.comment.drain_timeout", "timeout", timeout.String())
	}
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
		// 人类在等（评论重审/API 手工/MCP）：优先于 poller 批量认领（v3.40.0）
		Priority:  10,
		CreatedAt: time.Now(),
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
		return &api.CreateTaskResponse{TaskId: existing.ID, Status: existing.Status, Duplicated: true}, http.StatusOK
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
		// resolvedFinding 模式一解析出的入库原始身份（模式二为零值，仍用请求身份）
		resolvedFinding plugin.Finding
	)
	switch {
	case str(req.Fingerprint) != "":
		// 模式一：报告意见行的 fp 短码直达（前缀解析完整行，含别名回映射）。
		// v3.8.0：取回 prints 行的原始 file/line/severity/comment/plugin——
		// 抑制回写与 pattern 记账都必须用入库身份；请求里的评语（评论路径下是
		// 整条评论原文）只会把学习环账本记到垃圾键上。
		rec, found := s.Store.ResolvePrintRecord(ctx, platform, req.Owner, req.Repo, req.Number, str(req.Fingerprint))
		if !found {
			return &api.SuppressFindingResponse{Matched: false, Fingerprint: str(req.Fingerprint)}, http.StatusOK
		}
		fp = rec.Print
		pluginName = rec.Plugin
		file = rec.File
		resolvedFinding = plugin.Finding{
			File: rec.File, Line: rec.Line, Severity: rec.Severity,
			Comment: rec.Comment, By: rec.Plugin,
		}
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

	// 学习环/知识飞轮/回写统一用入库原始身份（模式一 resolvedFinding；模式二用请求身份）
	ledgerFinding := resolvedFinding
	if ledgerFinding.Comment == "" && ledgerFinding.File == "" {
		ledgerFinding = plugin.Finding{File: file, Comment: comment, By: pluginName}
	}
	if !suppressDone {
		var err error
		matched, err = s.Store.SuppressPrint(ctx, platform, req.Owner, req.Repo, req.Number,
			fp, ledgerFinding, time.Now())
		if err != nil {
			s.log().Error("argus.api.suppress_failed", "error", err.Error())
			return nil, http.StatusInternalServerError
		}
	}
	if matched {
		// 知识飞轮（v3.3）：误报结论回流知识库——R3 search_knowledge 命中
		// "本仓库历史误报"即带抑制上下文，同型误报率随使用下降
		s.recordFalsePositiveKnowledge(ctx, platform, req.Owner, req.Repo, req.Number,
			fp, ledgerFinding.File, ledgerFinding.Comment)
	}
	// 学习环：人工判误报 → PatternSuppress（失败只告警）。
	// 必须用入库原始评语派生 pattern 键——请求评语（评论路径=整条评论原文）
	// 剥离指令词/hex 后塌缩到垃圾键，真实 pattern 永远收不到人工负票（v3.8.0 修复）。
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if err := s.Store.PatternSuppress(ctx, platform, req.Owner, req.Repo, pluginName,
			ledgerFinding, time.Now()); err != nil {
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

// publishCommentReplied 广播 comment.replied（v3.50.0）：Q&A/意图路由回帖的终态
// 事件——此前评论链路无 Bus 事件，SSE 侧只见任务不见问答，观测口径分裂。
// status: ok=回帖成功 / error=回帖失败或 LLM 失败（nil Bus 安全）。
func (s *Service) publishCommentReplied(status, platform, owner, repo, number string) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(progress.Event{
		Type: "comment.replied", Status: status, Platform: platform,
		Owner: owner, Repo: repo, Number: number,
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
		if errors.Is(err, storage.ErrNotFound) {
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
		if errors.Is(err, storage.ErrNotFound) {
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
	b.WriteString("原审查意见：\n\n> " + fence.EscapeUntrusted(comment) + "\n\n")
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
// 单例缓存（qaOnce）：此前每次问答都重建 Engine，纯分配浪费；并发首建安全。
func (s *Service) qa() *qa.Engine {
	s.qaOnce.Do(func() {
		e := &qa.Engine{
			SDK:       s.SDK,
			Store:     s.Store,
			Knowledge: s.Knowledge,
			Model:     s.Model,
			QAModel:   s.QAModel,
			Log:       s.log(),
		}
		// 本仓评审记忆索引（v3.39.0）：Q&A 作答可参考既有 dream 结论
		if s.Cfg != nil && s.Cfg.MemoryDir != "" {
			e.MemoryIndexFor = func(platform, owner, repo string) string {
				return memory.LoadIndex(s.Cfg.MemoryDir, memory.Ref{Platform: platform, Owner: owner, Repo: repo}, 0)
			}
		}
		s.qaEng = e
	})
	return s.qaEng
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

// ---------- MCP 副驾数据面（v3.15.0 P2-7；channel/mcpserver.Copilot 实现）----------

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
	arts := decodeArtifactsMap(task.StageArtifacts)
	if _, ok := arts[plugin.ArtifactCkptR1Spec]; ok {
		stageDone[plugin.LayerR1] = true
	}
	if _, ok := arts[plugin.ArtifactCkptR2Summary]; ok {
		stageDone[plugin.LayerR2] = true
	}
	debug := decodeArtifactsMap(task.DebugJSON)
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

// decodeArtifactsMap JSON 对象解码（坏数据安全返回空表）。
func decodeArtifactsMap(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
