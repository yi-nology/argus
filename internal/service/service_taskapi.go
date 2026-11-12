// service_taskapi.go —— TaskAPI/Agent 可观测面：任务创建/清单/围栏抑制/报告读取/agent 清单/MCP 副驾数据面。

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"

	"github.com/oekit/pkg/ptrx"
)

// CreateTask CLI/其他系统手动提交审查任务（🔒）。与 webhook 任务同语义（§5.1 规则 5）。
func (s *Service) CreateTask(ctx context.Context, req *argus.CreateTaskRequest) (*argus.CreateTaskResponse, int) {
	kind, err := kindFromIDL(req.Kind)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	return s.CreateTaskDirect(ctx, kind, req.Platform, req.Owner, req.Repo, req.Number, map[string]string(req.Artifacts))
}

// diffTaskMaxBytes diff_preview 任务输入上限（48KB）：artifacts_json 是 MySQL
// TEXT 列（64KB 上界），JSON 转义对换行密集的 diff 还有约一成膨胀——48KB 留足
// 余量。同步 review_diff 的 120KB 上限不适用（纯内存不落库）。MCP 侧错误文案
// 的"48KB"与本值同源手写，改此值须同步。
const diffTaskMaxBytes = 48 * 1024

// CreateDiffTask 本地 diff 异步预审任务（v3.123.0，MCP review_diff_async 数据面）：
// 无 PR 语义——platform 记 local、四元组空，diff 文本存 artifacts（ArtifactDiffInput），
// runner 按 kind 在 prepareContext 之前分派单轮写作执行体，报告落库供提交方
// get_report 轮询，不回帖。不做 PR 维去重：每次提交都是显式意图，各自独立任务。
func (s *Service) CreateDiffTask(ctx context.Context, diff string) (*argus.CreateTaskResponse, int) {
	if s.LLM == nil {
		return nil, http.StatusServiceUnavailable
	}
	if strings.TrimSpace(diff) == "" || len(diff) > diffTaskMaxBytes {
		return nil, http.StatusBadRequest
	}
	artifacts, _ := json.Marshal(map[string]string{plugin.ArtifactDiffInput: diff})
	task := storage.NewTask(domain.TaskKindDiffPreview, "local", "", "", "")
	task.ArtifactsJSON = string(artifacts)
	task.Priority = 10 // 人类在等（提交方拿着 task_id 轮询）
	if err := s.Store.CreateTask(ctx, task); err != nil {
		s.log().Error("argus.argus.diff_task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.publishCreated(task)
	s.log().Info("argus.argus.diff_task_created", "task", task.ID, "bytes", len(diff))
	return &argus.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// CreateTaskDirect 核心建任务（kind 为字符串词表），供 handler 与 MCP server 共用。
func (s *Service) CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
	artifacts map[string]string) (*argus.CreateTaskResponse, int) {
	// kind 白名单走 plugin 词表注册表单源（domain/taskkind.go canonical）。
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, http.StatusForbidden // ARGUS_REPO_ALLOWLIST 白名单（v3.134.0）
	}
	if !domain.IsKnownKind(kind) {
		return nil, http.StatusBadRequest
	}
	if _, _, ok := s.requirePlatform(platform); !ok {
		return nil, http.StatusBadRequest
	}
	if owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	// owner/repo 白名单（2026-10-01 评测补缺）：平台标识允许字母数字、. _ - 与
	// 子组斜杠（GitLab 多级组），拒掉引号/空白/控制符等进 API URL 的危险形态——
	// 此前任意字符串可入库并进入平台查询路径（评测实测 SQL 注入形态被建任务）。
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return nil, http.StatusBadRequest
	}
	// number 必须是纯十进制数字（平台 PR 序号）；strconv.Atoi 会放过 "+123" 等带符号写法，
	// 入库后运行期平台查询才失败。长度上限 10：超出 int64 的 20 位数字此前也会被
	// 建任务白跑一轮（评测实测）。
	if !isDigits(number) || len(number) > 10 {
		return nil, http.StatusBadRequest
	}
	// artifacts 总大小上限：任意大小 JSON 此前可直灌 DB（DoS 面）。
	if len(artifacts) > 0 {
		artifactsJSON, aerr := json.Marshal(artifacts)
		if aerr != nil || len(artifactsJSON) > artifactsMaxBytes {
			return nil, http.StatusBadRequest
		}
	}
	artifactsJSON, _ := json.Marshal(artifacts)
	task := storage.NewTask(kind, platform, owner, repo, number)
	task.ArtifactsJSON = string(artifactsJSON)
	// 人类在等（评论重审/API 手工/MCP）：优先于 poller 批量认领（v3.40.0）
	task.Priority = 10
	existing, created, err := s.Store.CreateTaskDirectIfIdle(ctx, task)
	if err != nil {
		s.log().Error("argus.argus.task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	if !created {
		// 同 PR 已有在飞任务：幂等返回既有任务，不再叠加一份重复审查（token 白烧）。
		s.log().Info("argus.argus.task_create_deduped", "existing", existing.ID,
			"status", existing.Status, "pr", owner+"/"+repo+"#"+number)
		return &argus.CreateTaskResponse{TaskId: existing.ID, Status: existing.Status, Duplicated: ptrx.Of(true)}, http.StatusOK
	}
	s.publishCreated(task)

	s.log().Info("argus.argus.task_created", "task", task.ID, "kind", kind,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number)
	if tfp := TokenFPFromContext(ctx); tfp != "" {
		// 外部 API 建任务取证（v3.137.0）：哪条 token 建的什么任务——webhook/
		// poller 内部路径无 token 指纹，不产生噪音。
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "kind", kind, "source", "api",
			"platform", platform, "owner", owner, "repo", repo, "number", number,
			"token_fp", tfp)
	}
	return &argus.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// ListTasks 任务列表（🔒）：status 过滤 + limit 收口，created_at 倒序。
func (s *Service) ListTasks(ctx context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	switch status {
	case "", storage.StatusPending, storage.StatusRunning, storage.StatusDone,
		storage.StatusFailed, storage.StatusStale:
	default:
		return nil, http.StatusBadRequest
	}
	tasks, err := s.Store.ListTasks(ctx, status, limit)
	if err != nil {
		s.log().Error("argus.argus.list_tasks_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	// 预分配空切片：nil 会被序列化成 "tasks":null，与 IDL required list 语义不符
	resp := &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{}}
	for i := range tasks {
		t := &tasks[i]
		if !s.taskListable(ctx, t.Platform, t.Owner, t.Repo) {
			continue // 数据可见性隔离（v3.140.0）：域外任务不进清单（含 local 元数据）
		}
		item := &argus.TaskSummary{
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
func (s *Service) SuppressFinding(ctx context.Context, req *argus.SuppressFindingRequest) (*argus.SuppressFindingResponse, int) {
	platform, _, ok := s.requirePlatform(req.Platform)
	if !ok {
		return nil, http.StatusBadRequest
	}
	// v6.7.0 P0-02：写面同样收口仓库维度——HTTP 直入此前无 repoAllowedFor
	//（MCP 面 SuppressFindingScoped 已接），约束内 admin 才可抑制。
	if !s.Authorize(ctx, req.Platform, req.Owner, req.Repo) {
		authzDeniedMetric(ctx, "/findings/suppress")
		return nil, http.StatusForbidden
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
			return &argus.SuppressFindingResponse{Matched: false, Fingerprint: str(req.Fingerprint)}, http.StatusOK
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
			s.log().Error("argus.argus.suppress_near_failed", "error", err2.Error())
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
			s.log().Error("argus.argus.suppress_failed", "error", err.Error())
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
	s.log().Info("argus.argus.suppressed", "pr", req.Owner+"/"+req.Repo+"#"+req.Number,
		"file", file, "matched", matched, "fp", fp[:8])
	s.Audit.Log(auditFindingSuppress,
		"platform", platform, "owner", req.Owner, "repo", req.Repo, "number", req.Number,
		"file", file, "fingerprint", fp[:8], "matched", matched,
		"token_fp", TokenFPFromContext(ctx))
	return &argus.SuppressFindingResponse{Matched: matched, Fingerprint: fp}, http.StatusOK
}

// publishCreated 广播 task.created（nil Bus 安全）+ 落 outbox（P1-05，#10 跨实例
// 分发：他实例 fanout 环回灌本地 Bus）。
func (s *Service) publishCreated(t *storage.Task) {
	if s.Bus == nil || t == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: progress.EventTaskCreated, TaskID: t.ID, Platform: t.Platform,
		Owner: t.Owner, Repo: t.Repo, Number: t.Number,
	})
}

// publishProgress 进程内快路径广播 + outbox 兜跨实例（P1-05，#10）：本实例
// SSE 客户端走 Bus（实时），他实例的 fanout 环轮询 outbox 回灌其本地 Bus——
// 双源经 Event.ID 去重（v6.12 P1：本地发布侧 Bus.Publish 前登记实例级
// EventSeen 环，fanout 回灌命中环即跳过——同实例 at-most-once，修复评估实锤
// 的「同实例收到两次」竞窗）。outbox 追加失败只计数+告警（观测面数据，
// at-least-once 尽力而为，不阻断任务主流程）。
func (s *Service) publishProgress(ev progress.Event) {
	ev.ID = progress.NewEventID()
	if s.EventSeen != nil {
		s.EventSeen.Add(ev.ID) // 本地发布登记：fanout 回灌同 ID 跳过
	}
	s.Bus.Publish(ev)
	if s.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped("outbox_failed")
		s.log().Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
}

// publishCommentReplied 广播 comment.replied（v3.50.0）：Q&A/意图路由回帖的终态
// 事件——此前评论链路无 Bus 事件，SSE 侧只见任务不见问答，观测口径分裂。
// status: ok=回帖成功 / error=回帖失败或 LLM 失败（nil Bus 安全）。
func (s *Service) publishCommentReplied(status, platform, owner, repo, number string) {
	if s.Bus == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: "comment.replied", Status: status, Platform: platform,
		Owner: owner, Repo: repo, Number: number,
	})
}

// UsageMonthly 本月各仓库 token 用量（GET /usage/monthly；🔒，v3.1）。
// v6.7.0 P0-02：token 带仓库约束时约束外仓库的行不透出（跨仓用量元数据）。
func (s *Service) UsageMonthly(ctx context.Context) (map[string]any, int) {
	since := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Now().Location())
	usage, err := s.Store.MonthlyTokensByRepo(ctx, since)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	filter := s.AuthorizeRowFilter(ctx)
	if filter != nil {
		authzDeniedMetric(ctx, "/usage/monthly") // 行过滤发生（约束外用量不可见）
	}
	items := make([]storage.RepoTokenUsage, 0, len(usage))
	var total int64
	for _, u := range usage {
		if filter != nil && !filter(u.Platform, u.Owner, u.Repo) {
			continue
		}
		items = append(items, u)
		total += u.Tokens
	}
	// 双账本（v6.9.0 P1-06，#11）：quota=done 口径（配额判定与报表，平台故障误烧
	// 不占额）+ real=全部终态（含 failed/stale，真实消费可观测可设上限）。
	var realTotal int64
	realItems := make([]storage.RepoTokenUsage, 0, len(usage))
	if usageReal, err := s.Store.MonthlyTokensByRepoReal(ctx, since); err == nil {
		for _, u := range usageReal {
			if filter != nil && !filter(u.Platform, u.Owner, u.Repo) {
				continue
			}
			realItems = append(realItems, u)
			realTotal += u.Tokens
		}
	} else {
		s.log().Warn("argus.api.usage_real_failed", "error", err.Error())
	}
	return map[string]any{
		"month":       since.Format("2006-01"),
		"total":       total,
		"budget":      s.Cfg.RepoMonthlyTokenBudget,
		"repos":       items,
		"real_total":  realTotal,
		"real_budget": s.Cfg.RealTokenBudget,
		"real_repos":  realItems,
	}, http.StatusOK
}

// GetReportByRef 按 head SHA 查最近一次成功报告（GET /reports/:ref；🔒，v3.1）。
// v6.7.0 P0-02：解析出的任务必须过 AuthorizeTask——head SHA 是全局寻址键，
// 此前无仓库约束判定，持任意 read 档 token 可按 SHA 跨仓读取完整审查报告。
// v6.12 P1：评估实锤「同 SHA 多仓（fork/镜像）碰撞」——token 带仓库约束时
// 先按约束精确寻址（查库阶段限定平台/仓），查不到再全局取列表逐条过授权
// 回退（第一条通过者返回）；无约束 token 保持旧形态。
func (s *Service) GetReportByRef(ctx context.Context, ref string) (*argus.GetTaskResponse, int) {
	if ref == "" {
		return nil, http.StatusBadRequest
	}
	// v6.12 P1：评估实锤「同 SHA 多仓（fork/镜像）碰撞」+ 门禁「跨仓存在性探测
	// 403」语义调和——
	// ① token 带仓库约束：按约束精确寻址（查库阶段限定平台/仓，防碰撞）。
	//    精确条目命中但越权 = 403（e2e 门禁「跨仓 ref 必须 403」语义，防存在性
	//    探测）；未命中 = 404（该 SHA 不在本仓，语义等价「本仓无此报告」——
	//    不暴露全局存在性）。
	// ② 通配/多约束条目（无法精确寻址）：取 SHA 全量列表逐条过授权，第一条
	//    通过的返回——评估语义（合法用户不被 fork 同 SHA 碰撞误 403）。
	if tp := TokenReposFromContext(ctx); tp != "" {
		if platform, owner, repo := parseExactRepoPattern(tp); platform != "" {
			if task, err := s.Store.FindReportByHead(ctx, ref, platform, owner, repo); err == nil {
				if s.AuthorizeTask(ctx, task.Platform, task.Owner, task.Repo) {
					return reportResponse(task), http.StatusOK
				}
				authzDeniedMetric(ctx, "/reports/:ref")
				return nil, http.StatusForbidden
			}
			return nil, http.StatusNotFound
		}
		// 通配/多约束：列表回退，第一条授权通过者返回；全部拒绝则 403——
		// 语义：该 SHA 存在但当前 token 无权访问（与 e2e 门禁「跨仓 ref 403」
		// 一致，防存在性探测泄露），与「本仓无此报告」404 区分。
		tasks, err := s.Store.FindReportsByHead(ctx, ref, 0)
		if err != nil {
			return nil, http.StatusInternalServerError
		}
		for i := range tasks {
			t := &tasks[i]
			if s.AuthorizeTask(ctx, t.Platform, t.Owner, t.Repo) {
				return reportResponse(t), http.StatusOK
			}
		}
		if len(tasks) > 0 {
			authzDeniedMetric(ctx, "/reports/:ref")
			return nil, http.StatusForbidden
		}
		return nil, http.StatusNotFound
	}
	// 无仓库约束：旧语义——全局取最新一条 + 授权
	task, err := s.Store.FindReportByHead(ctx, ref, "", "", "")
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.AuthorizeTask(ctx, task.Platform, task.Owner, task.Repo) {
		authzDeniedMetric(ctx, "/reports/:ref")
		return nil, http.StatusForbidden
	}
	return reportResponse(task), http.StatusOK
}

// reportResponse GetReportByRef 响应构造（单源，两处共用）。
func reportResponse(task *storage.Task) *argus.GetTaskResponse {
	return &argus.GetTaskResponse{
		TaskId:  task.ID,
		Status:  task.Status,
		Verdict: strPtr(task.Verdict),
		Report:  strPtr(task.Report),
	}
}

// parseExactRepoPattern 把 token 仓库约束的精确条目 "platform:owner/repo"
// 解析成三元组；非精确形态（空/含通配 */多条目）返回空串走列表回退。
func parseExactRepoPattern(tp string) (platform, owner, repo string) {
	if tp == "" || strings.Contains(tp, ",") || strings.Contains(tp, "*") {
		return "", "", ""
	}
	rest, ok := strings.CutPrefix(tp, ":")
	if !ok {
		return "", "", ""
	}
	var rest2 string
	platform, rest2, ok = strings.Cut(rest, ":")
	if !ok || platform == "" {
		return "", "", ""
	}
	owner, repo, ok = strings.Cut(rest2, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", ""
	}
	return platform, owner, repo
}

// GetTask 查询任务状态与结果（报告 markdown 内联返回，§5；🔒）。
func (s *Service) GetTask(ctx context.Context, taskID string) (*argus.GetTaskResponse, int) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, http.StatusForbidden // 数据可见性隔离（v3.140.0）：token 仓库域外任务
	}
	resp := &argus.GetTaskResponse{
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
	if findings, ok := taskFindings(task.ResultJSON); ok {
		for _, f := range findings {
			by := f.By
			resp.Findings = append(resp.Findings, &argus.Finding{
				File:     f.File,
				Line:     clampInt32(f.Line),
				Severity: f.Severity,
				Comment:  f.Comment,
				By:       strPtr(by),
			})
		}
	}
	if task.DebugJSON != "" {
		resp.Debug = strPtr(task.DebugJSON)
	}
	return resp, http.StatusOK
}
