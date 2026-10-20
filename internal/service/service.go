// Package service handler 的薄实现层：绑定请求 → 调编排 → 统一响应壳 {code,data,message,trace_id}。
// webhook 同步路径实现 §5.1 全部过滤规则 + §5.2 幂等；异步执行交给 worker pool（§10.4 DB 即队列）。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/ratelimit"
	ekcache "github.com/oekit/data/cache"

	api "argus/biz/model/api"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/memory"
	"argus/internal/obs"
	"argus/internal/plugin"
	"argus/internal/progress"
	"argus/internal/qa"
	"argus/internal/semsearch"
	"argus/internal/storage"
	"argus/internal/writing"

	"github.com/oagentkit/guardx/fence"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/obs/audit"
	"github.com/oagentkit/rag"
)

// Argus-specific audit actions (agentkit/audit provides the Logger, actions are domain-specific).
const (
	auditTaskCreate      audit.Action = "task.create"
	auditFindingSuppress audit.Action = "finding.suppress"
	// v3.86.0 补面：verdict 终态/误抑制恢复/修复命令（fix_requested 与 fix_done
	// 在 service，verdict.posted 与 fix.push 在 runner——同一 auditLog 单源）。
	auditVerdictPosted  audit.Action = "verdict.posted"
	auditFixPush        audit.Action = "fix.push"
	auditFindingRestore audit.Action = "finding.restore"
	auditFindingFixReq  audit.Action = "finding.fix_requested"
	auditFindingFixDone audit.Action = "finding.fix_done"
)

// Service 门面：biz handler 只跟它说话。
type Service struct {
	Cfg       *config.Config
	Audit     *audit.Logger // 审计日志（nil 安全：Logger.Log 对 nil receiver no-op，调用处免判空）
	Store     *storage.Store
	SDK       *argusctx.SDKCtx
	Registry  *plugin.Registry
	Log       Logger
	Version   string
	Bus       *progress.Bus        // 任务进度事件（v3.1 SSE 数据源；nil 安全）
	Knowledge rag.KnowledgeService // 知识库（误报反馈回流；nil/Noop = 只抑制不回流）
	QAModel   model.BaseChatModel  // Q&A 专用模型（P6 覆盖；nil = 用 Model）
	Model     model.BaseChatModel  // LLM 模型（Q&A 直接调用；nil = Q&A 禁用）
	// LLM 生成器（v3.123.0，models.Generate 同源接线）：diff 预审等单轮写作
	// 委派 writing.Service 消费；nil = ReviewDiff/CreateDiffTask 禁用。
	LLM llm.Generator
	// RouterModel 评论意图路由分类模型（v3.4 ARGUS_ROUTER_MODEL 覆盖；nil = QAModel → Model）。
	// 仅 ARGUS_COMMENT_ROUTER=1 时消费。
	RouterModel model.BaseChatModel

	// WC 工作副本池（v3.22.0 testgen 沙箱验证；nil = 验证不可用）。
	WC *workcopy.Pool

	// SemSearch 工作副本语义检索底座（v3.126.0，bootstrap 按 ARGUS_SEMSEARCH +
	// ARGUS_EMBEDDING_* 双门构建；nil = search_sem 运行期报未启用）。
	SemSearch *semsearch.Manager

	// qaEngine 问答引擎单例（Engine 无自有状态、字段全共享引用，会话在 Store）。
	// 此前每次 Ask 都重建，纯浪费；sync.Once 保证并发首建安全。
	qaOnce sync.Once
	qaEng  *qa.Engine

	// 评论链路资源闸（v3.8.0）：有界并发 + 超时 + 停机排水。此前评论事件挂
	// 无超时的 Background ctx、无并发上限、停机直接杀在途 ReAct（用户永无回复）。
	commentOnce  sync.Once
	commentSlots chan struct{}  // 有界并发（容量 Cfg.CommentConcurrency）
	commentWG    sync.WaitGroup // 在途评论事件（DrainComments 等待）

	// qaThrottle Q&A 评论 per-PR 30s 冷却缓存（v3.84.0 回归 per-Service 实例：
	// v3.82.0 曾改进程级单例，e2e 同进程多 Service 场景互相污染节流窗口）。
	// 懒建（qaThrottleMu 护栏），生产行为不变——进程内恒一个 Service。
	qaThrottleMu  sync.Mutex
	qaThrottleCch *ekcache.LocalCache[bool]

	// rateLimiter 每 token 速率限制器（v3.135.0 引入；v3.147.0 换 oekit
	// ratelimit.KeyedLimiter：按 key 隔离令牌桶 + TTL 驱逐闲置 key；tokenRateOK
	// 懒建，key=token 指纹）。
	rateOnce    sync.Once
	rateLimiter *ratelimit.KeyedLimiter

	// fixInflight @argus fix 在途去重（v3.85.0）：key=pr+fp，防同 finding 并发
	// 双跑烧双倍 agent token。进程级即可（修复是交互式命令，不跨重启）。
	fixInflight sync.Map
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
	s.Audit.Log("github_app_installation",
		"action", payload.Action,
		"installation_id", payload.Installation.ID,
		"account", payload.Installation.Account.Login,
		"repos", repoNames,
	)
	return &api.WebhookResponse{
		Status:  "accepted",
		Reason:  strPtr("installation event logged"),
		EventId: strPtr(delivery),
	}
}

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
			// search_code 全文检索（v3.91.0）：QA 与审查面共享工作副本池，
			// ARGUS_CODESEARCH 同源开关
			WC:         s.WC,
			CodeSearch: s.Cfg != nil && s.Cfg.CodeSearch,
			// search_sem 语义检索（v3.126.0）：底座由 bootstrap 双门构建，
			// 配置开关在此同源判定
			SemSearch:   s.SemSearch,
			SemSearchOn: s.Cfg != nil && s.Cfg.SemSearch,
		}
		// 本仓评审记忆索引（v3.39.0）：Q&A 作答可参考既有 dream 结论
		if s.Cfg != nil && s.Cfg.MemoryDir != "" {
			e.MemoryIndexFor = func(platform, owner, repo string) string {
				return memory.LoadIndexWithOrg(s.Cfg.MemoryDir, memory.Ref{Platform: platform, Owner: owner, Repo: repo}, 0)
			}
		}
		s.qaEng = e
	})
	return s.qaEng
}

// LatestReview 某 PR 最近一次审查结论（mcpserver ask_argus 数据源）。
// 返回 {task_id, finished_at, verdict, findings, report}；无记录返回 (nil, false)。
func (s *Service) LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return map[string]any{"error": errRepoDenied}, true // 拒绝以内容态可见，不伪装成"无记录"
	}
	return s.qa().LatestReview(ctx, platform, owner, repo, number)
}

// AnswerAboutReview 基于最近一次报告回答追问（ask_argus 带 question 时走这里；
// 单轮直调——报告已在手，无需工具链）。模型未配置或无报告返回 ("" , false)。
func (s *Service) AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return errRepoDenied, true // 拒绝以答案态可见，不伪装成"无法回答"
	}
	return s.qa().AnswerAboutReview(ctx, platform, owner, repo, number, question)
}

// ---------- MCP 副驾数据面（v3.15.0 P2-7；channel/mcpserver.Copilot 实现）----------

// ReviewDiff 本地 diff 审查（v3.74.1，argus-copilot pre-commit 路径）：
// 单轮 LLM 审查任意 diff 文本，返回 markdown 意见列表。不建任务、不落库、
// 不回帖——本地快审语义；模型未配置返回错误（调用方显式降级）。
// diff 上限 120KB（与写作面同量级；超限截断由调用方负责，工具入参侧也拦）。
// v3.123.0 起委派 writing.Service.ReviewDiff（与 diff_preview 异步任务同一
// 执行体，prompt 单源）——本层只留 nil 守卫与语义注释。
func (s *Service) ReviewDiff(ctx context.Context, diff string) (string, error) {
	return (&writing.Service{LLM: s.LLM}).ReviewDiff(ctx, diff)
}

// StatsOverview 审查概览聚合（GET /stats/overview，🔒 read 档，v3.88.0）：
// 按仓任务量 / verdict 分布 / token 用量 / 抑制计数。days 窗口缺省 30，
// 钳制 1..365；platform/owner/repo 过滤可选。仪表盘（review-service 前端）
// 的数据面——argus 自身不做 UI（v3.84 轮裁定）。
func (s *Service) StatsOverview(ctx context.Context, platform, owner, repo string, days int) (map[string]any, int) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	rows, err := s.Store.StatsOverview(ctx, time.Now().AddDate(0, 0, -days), platform, owner, repo)
	if err != nil {
		s.log().Error("argus.api.stats_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	var totals struct {
		repos, tasks, approved, commented, rejected, tokens, suppressed int64
	}
	for _, r := range rows {
		totals.repos++
		totals.tasks += r.TasksDone
		totals.approved += r.Approved
		totals.commented += r.Commented
		totals.rejected += r.Rejected
		totals.tokens += r.Tokens
		totals.suppressed += r.Suppressed
	}
	return map[string]any{
		"window_days":  days,
		"generated_at": time.Now().Format(time.RFC3339),
		"repos":        rows,
		"repo_count":   totals.repos,
		"tasks_done":   totals.tasks,
		"verdicts": map[string]int64{
			"approve":         totals.approved,
			"comment":         totals.commented,
			"request_changes": totals.rejected,
		},
		"tokens_used":         totals.tokens,
		"findings_suppressed": totals.suppressed,
	}, http.StatusOK
}
