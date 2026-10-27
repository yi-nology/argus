// service_taskapi.go —— TaskAPI/Agent 可观测面：任务创建/清单/围栏抑制/报告读取/agent 清单/MCP 副驾数据面。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	api "argus/biz/model/api"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/storage"

	"github.com/oekit/pkg/ptrx"
)

// CreateTask CLI/其他系统手动提交审查任务（🔒）。与 webhook 任务同语义（§5.1 规则 5）。
func (s *Service) CreateTask(ctx context.Context, req *api.CreateTaskRequest) (*api.CreateTaskResponse, int) {
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
func (s *Service) CreateDiffTask(ctx context.Context, diff string) (*api.CreateTaskResponse, int) {
	if s.LLM == nil {
		return nil, http.StatusServiceUnavailable
	}
	if strings.TrimSpace(diff) == "" || len(diff) > diffTaskMaxBytes {
		return nil, http.StatusBadRequest
	}
	artifacts, _ := json.Marshal(map[string]string{plugin.ArtifactDiffInput: diff})
	task := storage.NewTask(plugin.TaskKindDiffPreview, "local", "", "", "")
	task.ArtifactsJSON = string(artifacts)
	task.Priority = 10 // 人类在等（提交方拿着 task_id 轮询）
	if err := s.Store.CreateTask(ctx, task); err != nil {
		s.log().Error("argus.api.diff_task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.publishCreated(task)
	s.log().Info("argus.api.diff_task_created", "task", task.ID, "bytes", len(diff))
	return &api.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// CreateTaskDirect 核心建任务（kind 为字符串词表），供 handler 与 MCP server 共用。
func (s *Service) CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
	artifacts map[string]string) (*api.CreateTaskResponse, int) {
	// kind 白名单走 plugin 词表注册表单源（domain/taskkind.go canonical）。
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, http.StatusForbidden // ARGUS_REPO_ALLOWLIST 白名单（v3.134.0）
	}
	if !plugin.IsKnownKind(kind) {
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
		s.log().Error("argus.api.task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	if !created {
		// 同 PR 已有在飞任务：幂等返回既有任务，不再叠加一份重复审查（token 白烧）。
		s.log().Info("argus.api.task_create_deduped", "existing", existing.ID,
			"status", existing.Status, "pr", owner+"/"+repo+"#"+number)
		return &api.CreateTaskResponse{TaskId: existing.ID, Status: existing.Status, Duplicated: ptrx.Of(true)}, http.StatusOK
	}
	s.publishCreated(task)

	s.log().Info("argus.api.task_created", "task", task.ID, "kind", kind,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number)
	if tfp := TokenFPFromContext(ctx); tfp != "" {
		// 外部 API 建任务取证（v3.137.0）：哪条 token 建的什么任务——webhook/
		// poller 内部路径无 token 指纹，不产生噪音。
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "kind", kind, "source", "api",
			"platform", platform, "owner", owner, "repo", repo, "number", number,
			"token_fp", tfp)
	}
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
		if !s.taskListable(ctx, t.Platform, t.Owner, t.Repo) {
			continue // 数据可见性隔离（v3.140.0）：域外任务不进清单（含 local 元数据）
		}
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
	s.Audit.Log(auditFindingSuppress,
		"platform", platform, "owner", req.Owner, "repo", req.Repo, "number", req.Number,
		"file", file, "fingerprint", fp[:8], "matched", matched,
		"token_fp", TokenFPFromContext(ctx))
	return &api.SuppressFindingResponse{Matched: matched, Fingerprint: fp}, http.StatusOK
}

// publishCreated 广播 task.created（nil Bus 安全）。
func (s *Service) publishCreated(t *storage.Task) {
	if s.Bus == nil || t == nil {
		return
	}
	s.Bus.Publish(progress.Event{
		Type: progress.EventTaskCreated, TaskID: t.ID, Platform: t.Platform,
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
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, http.StatusForbidden // 数据可见性隔离（v3.140.0）：token 仓库域外任务
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
	if findings, ok := taskFindings(task.ResultJSON); ok {
		for _, f := range findings {
			by := f.By
			resp.Findings = append(resp.Findings, &api.Finding{
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
