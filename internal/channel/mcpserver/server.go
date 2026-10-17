// Package mcpserver L1/L2 门面：Argus as MCP Server（design §9.2 方向 B，v3.1 实装）。
// 把 Argus 的审查能力以 MCP 工具暴露给任意 MCP 客户端（review-service、agent 工具链等）：
//
//	review_pr / requirement_compliance  发起审查（可选等待终态，返回 verdict 与完整报告）
//	review_diff                          本地 staged diff 预审（v3.74.1）
//	ask_argus                            最新审查结论问答（v3.3 P5）
//	get_report                           按 taskId 取报告
//	list_tasks                           任务清单枚举（v3.121.0）
//	get_task_stages / list_findings / get_suggestion  副驾只读三件（v3.15.0）
//	search_code                          PR 工作副本受控检索（v3.121.0）
//	search_knowledge                     知识库语义检索（v3.125.0，R3/R4 同源）
//	search_sem                           工作副本语义检索（v3.126.0，embedding opt-in）
//	review_diff_async                    本地 diff 异步预审任务（v3.123.0，提交方轮询闭环）
//	fix_finding                          沙箱修复（v3.89.0，写侧）
//	suppress_finding                     误报抑制（v3.128.0，写侧，admin scope 门）
//	restore_finding                      恢复误抑制意见（v3.131.0，写侧，admin scope 门）
//	get_capabilities                     能力面快照（v3.129.0，部署开了什么一次看清）
//	search_findings                      本仓历史意见账本检索（v3.138.0）
//
// 传输：mark3labs/mcp-go streamable http，经 Hertz 桥接挂在 /mcp（GET/POST/DELETE），
// 鉴权与 🔒 路由一致（Bearer）。与 toolpool（方向 A：我们调用别人）分层不同，不合流。
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	mw "github.com/oekit/middleware/middleware"

	api "argus/biz/model/api"
)

// Core Argus 能力面（由 *service.Service 实现，无 hertz 依赖）。
type Core interface {
	CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
		artifacts map[string]string) (*api.CreateTaskResponse, int)
	GetTask(ctx context.Context, taskID string) (*api.GetTaskResponse, int)
	ListTasks(ctx context.Context, status string, limit int) (*api.ListTasksResponse, int)
	ListAgents(ctx context.Context, platform, owner, repo string) *api.ListAgentsResponse
}

// DiffReviewer 可选扩展（v3.74.1）：review_diff 本地预审的数据面。
// *service.Service 实现（单轮 LLM，不建任务不落库）。
type DiffReviewer interface {
	ReviewDiff(ctx context.Context, diff string) (string, error)
}

// ReviewQA 可选扩展（v3.3 P5）：ask_argus 的数据面。*service.Service 实现。
type ReviewQA interface {
	// LatestReview 某 PR 最近一次审查结论（verdict/findings/报告摘录）。
	LatestReview(ctx context.Context, platform, owner, repo, number string) (map[string]any, bool)
	// AnswerAboutReview 基于最近报告回答追问（无模型/无报告返回 false）。
	AnswerAboutReview(ctx context.Context, platform, owner, repo, number, question string) (string, bool)
}

// CodeSearcher 可选扩展（v3.121.0）：search_code 的数据面。*service.Service 实现。
type CodeSearcher interface {
	// SearchCode PR 工作副本受控全文检索，返回围栏文本（错误回执内嵌）。
	SearchCode(ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error)
}

//	DiffTaskCreator 可选扩展（v3.123.0）：review_diff_async 的数据面。
//
// *service.Service 实现。
type DiffTaskCreator interface {
	// CreateDiffTask diff 文本 → 异步预审任务（秒回 task_id，报告落库供轮询）。
	CreateDiffTask(ctx context.Context, diff string) (*api.CreateTaskResponse, int)
}

// KnowledgeSearcher 可选扩展（v3.125.0）：search_knowledge 的数据面——R3/R4
// 审查面同源知识库（组织/仓库规范、历史审查结论、误报判例）暴露给外部 agent。
// *service.Service 实现（未配置知识源时运行期报错，工具不消失——fix_finding 同语义）。
type KnowledgeSearcher interface {
	// SearchKnowledge 知识库语义检索，返回围栏文本（错误回执内嵌为 error）。
	SearchKnowledge(ctx context.Context, query string, topK int) (string, error)
}

// SemSearcher 可选扩展（v3.126.0）：search_sem 的数据面——工作副本语义检索
// （结构化摘要 embedding + 进程内余弦，ARGUS_SEMSEARCH opt-in）。补 search_code
// 词法 grep 答不了的「按意图找代码」。*service.Service 实现。
type SemSearcher interface {
	// SearchSem 按意图检索 PR 工作副本，返回围栏命中列表（错误回执内嵌为 error）。
	SearchSem(ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error)
}

// New 构造 MCPServer 并注册工具组（副驾三工具 core 实现 Copilot 时挂载）。
func New(core Core, version string) *server.MCPServer {
	s := server.NewMCPServer("argus", version,
		server.WithToolCapabilities(false),
	)
	s.AddTool(reviewPrTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kind := argString(req, "kind", defaultReviewKind)
		return runReview(ctx, core, req, kind)
	})
	// review_diff（v3.74.1）：本地 staged diff 预审（argus-copilot review 子命令
	// 消费）；DiffReviewer 可选能力，模型未配置时不挂载该工具。
	if dr, ok := core.(DiffReviewer); ok {
		s.AddTool(mcp.NewTool("review_diff",
			mcp.WithDescription("本地预审一段 diff（提交前检查）。入参 diff 为完整 diff 文本（≤120KB），返回 markdown 审查意见。"),
			mcp.WithString("diff", mcp.Required(), mcp.Description("完整 diff 文本")),
		), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			diff, _ := req.GetArguments()["diff"].(string)
			if strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultError("diff 为空"), nil
			}
			if len(diff) > 120_000 {
				diff = diff[:120_000]
			}
			out, err := dr.ReviewDiff(ctx, diff)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
	s.AddTool(requirementComplianceTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runReview(ctx, core, req, defaultReviewKind)
	})
	s.AddTool(listTasksTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := argString(req, "status", "")
		// 与 service.ListTasks/storage.ListTasks 词表同口径（service 校验是运行期
		// 兜底，这里先拦给出可读错误，避免 400 裸状态码）。
		switch status {
		case "", "pending", "running", "done", "failed", "stale":
		default:
			return mcp.NewToolResultError("status 须为 pending/running/done/failed/stale 之一（空=全部）"), nil
		}
		limit := 0 // 0 = 服务端缺省 20、钳 100（storage.ListTasks 语义）
		if f, ok := req.GetArguments()["limit"].(float64); ok && f > 0 {
			limit = int(f)
		}
		resp, code := core.ListTasks(ctx, status, limit)
		if code != http.StatusOK {
			return mcp.NewToolResultError(fmt.Sprintf("查询失败（HTTP %d）", code)), nil
		}
		b, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	})
	s.AddTool(listAgentsTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data := core.ListAgents(ctx,
			argString(req, "platform", ""), argString(req, "owner", ""), argString(req, "repo", ""))
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	})
	s.AddTool(getReportTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "task_id")
		if rerr != nil {
			return rerr, nil
		}
		t, code := core.GetTask(ctx, vals[0])
		if code != http.StatusOK {
			return mcp.NewToolResultError(fmt.Sprintf("查询失败（HTTP %d）", code)), nil
		}
		out := taskView(t)
		decorateStages(ctx, core, out, vals[0])
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})
	s.AddTool(askArgusTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		qa, ok := core.(ReviewQA)
		if !ok {
			return mcp.NewToolResultError("ask_argus 未启用（core 未实现 ReviewQA）"), nil
		}
		// 必填校验（深挖修复）：schema 标了 Required 但此前漏传静默回空串，
		// 空参查询被伪装成"该 PR 尚无审查记录"，客户端装配错误无从排查。
		vals, rerr := requireArgs(req, "platform", "owner", "repo", "number")
		if rerr != nil {
			return rerr, nil
		}
		platform, owner, repo, number := vals[0], vals[1], vals[2], vals[3]
		for _, r := range number { // PR 序号必须纯数字（与 service.isDigits 同口径）
			if r < '0' || r > '9' {
				return mcp.NewToolResultError("number 必须为纯数字 PR 序号"), nil
			}
		}
		question := argString(req, "question", "")
		if len(question) > maxQuestionRunes {
			return mcp.NewToolResultError("question 过长（上限 " + fmt.Sprint(maxQuestionRunes) + " 字符）"), nil
		}
		if len(question) > maxQuestionRunes {
			return mcp.NewToolResultError("question 过长（上限 " + fmt.Sprint(maxQuestionRunes) + " 字符）"), nil
		}
		if question == "" {
			// 无追问：返回最近一次审查结论（verdict + findings + 报告摘录）
			lr, ok := qa.LatestReview(ctx, platform, owner, repo, number)
			if !ok {
				return mcp.NewToolResultText("该 PR 尚无已完成的 Argus 审查记录。"), nil
			}
			b, err := json.MarshalIndent(lr, "", "  ")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(b)), nil
		}
		// 带追问：基于最近报告回答
		ans, ok := qa.AnswerAboutReview(ctx, platform, owner, repo, number, question)
		if !ok {
			return mcp.NewToolResultText("无法回答：Argus 未配置 LLM，或该 PR 尚无已完成的审查记录。"), nil
		}
		return mcp.NewToolResultText(ans), nil
	})
	NewCopilotTools(s, core)
	NewFixerTool(s, core)      // v3.89.0：core 实现 Fixer（ARGUS_FIX_AGENT 启用语义在 service 侧守卫）时注册
	NewSuppressorTool(s, core) // v3.128.0：core 实现 FindingSuppressor（admin scope 透传二次判定）时注册
	NewCapabilityTool(s, core) // v3.129.0：core 实现 CapabilityFace 时注册（能力面一次看清）
	NewRestorerTool(s, core)   // v3.131.0：core 实现 FindingRestorer（admin scope 门，suppress 反向）时注册
	NewHistorianTool(s, core)  // v3.138.0：core 实现 FindingHistorian（本仓意见账本检索）时注册
	if cs, ok := core.(CodeSearcher); ok {
		s.AddTool(searchCodeTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "pattern")
			if rerr != nil {
				return rerr, nil
			}
			out, err := cs.SearchCode(ctx, vals[0], vals[1], vals[2], vals[3], vals[4],
				argString(req, "glob", ""), argBool(req, "ignore_case"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
	// search_knowledge（v3.125.0）：R3/R4 同源知识库暴露给外部 agent——
	// KnowledgeSearcher 可选能力；未配置知识源在 service 侧运行期报错。
	if ks, ok := core.(KnowledgeSearcher); ok {
		s.AddTool(searchKnowledgeTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			topK := 0 // 0 = 服务端缺省 5、钳 10（service.SearchKnowledge 语义）
			if f, ok := req.GetArguments()["top_k"].(float64); ok && f > 0 {
				topK = int(f)
			}
			out, kerr := ks.SearchKnowledge(ctx, query, topK)
			if kerr != nil {
				return mcp.NewToolResultError(kerr.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
	// search_sem（v3.126.0）：工作副本语义检索——SemSearcher 可选能力；
	// ARGUS_SEMSEARCH 未开在 service 侧运行期报错。
	if ss, ok := core.(SemSearcher); ok {
		s.AddTool(searchSemTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "query")
			if rerr != nil {
				return rerr, nil
			}
			topK := 0 // 0 = 服务端缺省 5、钳 10（qa.Engine.SearchSem 语义）
			if f, ok := req.GetArguments()["top_k"].(float64); ok && f > 0 {
				topK = int(f)
			}
			out, serr := ss.SearchSem(ctx, vals[0], vals[1], vals[2], vals[3], vals[4], topK)
			if serr != nil {
				return mcp.NewToolResultError(serr.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		})
	}
	if dtc, ok := core.(DiffTaskCreator); ok {
		s.AddTool(reviewDiffAsyncTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			diff, _ := req.GetArguments()["diff"].(string)
			if strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultError("diff 为空"), nil
			}
			created, code := dtc.CreateDiffTask(ctx, diff)
			switch code {
			case http.StatusOK:
				b, _ := json.Marshal(map[string]any{
					"task_id": created.TaskId, "status": created.Status,
					"note": "预审任务已入队，用 get_report(task_id) 轮询终态（建议 15-30s 间隔）",
				})
				return mcp.NewToolResultText(string(b)), nil
			case http.StatusBadRequest:
				return mcp.NewToolResultError("diff 为空或超过 48KB 上限（异步任务落库面；更长 diff 请分块提交）"), nil
			case http.StatusServiceUnavailable:
				return mcp.NewToolResultError("模型未配置（ARGUS_LLM_*），diff 预审不可用"), nil
			default:
				return mcp.NewToolResultError(fmt.Sprintf("创建任务失败（HTTP %d）", code)), nil
			}
		})
	}
	return s
}

// ---------- 工具定义 ----------

func reviewPrTool() mcp.Tool {
	return mcp.NewTool("review_pr",
		mcp.WithDescription("Argus 审查指定 PR：需求复核（R1→R5）+ 专家插件意见合并，回帖报告到 PR。wait=true 时阻塞至终态并返回结论与报告"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("kind", mcp.Description("审查类型：requirement_compliance（默认）| code_review | visual_check"),
			mcp.WithStringEnumItems([]string{"requirement_compliance", "code_review", "visual_check"})),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func requirementComplianceTool() mcp.Tool {
	return mcp.NewTool("requirement_compliance",
		mcp.WithDescription("Argus 需求复核：解析需求 issue/描述 → 逐条判定验收清单完成度（含 scope creep）→ 确定性裁决。等价 review_pr(kind=requirement_compliance)"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("列出 Argus 全部审查插件（名称/形态/技能/生效状态；platform+owner+repo 三参全传时附带该仓库 .reviewagents.yaml 配置视图与信任边界告警）"),
		mcp.WithString("platform", mcp.Description("可选：平台")),
		mcp.WithString("owner", mcp.Description("可选：仓库所属空间")),
		mcp.WithString("repo", mcp.Description("可选：仓库名")),
	)
}

func listTasksTool() mcp.Tool {
	return mcp.NewTool("list_tasks",
		mcp.WithDescription("枚举 Argus 审查任务（created_at 倒序，created_at/head_sha/verdict 摘要面）——巡检/找 task_id 的入口，配合 get_report/list_findings 消费"),
		mcp.WithString("status", mcp.Description("可选：按状态过滤，空=全部"),
			mcp.WithStringEnumItems([]string{"pending", "running", "done", "failed", "stale"})),
		mcp.WithNumber("limit", mcp.Description("可选：条数上限（缺省 20，上界 100）")),
	)
}

func searchCodeTool() mcp.Tool {
	return mcp.NewTool("search_code",
		mcp.WithDescription("在 PR 工作副本内做受控全文正则检索（跨文件找调用方/相似实现/字符串与配置线索，含非 Go 文件）——核查审查意见/修复前侦察用。pattern 为 Go 正则；命中不等于结论。服务未启用（ARGUS_CODESEARCH=0 或无工作副本池）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Go 正则表达式")),
		mcp.WithString("glob", mcp.Description("可选：文件过滤 glob（无 / 时按文件名匹配如 *.go，含 / 时按仓库相对路径匹配）")),
		mcp.WithBoolean("ignore_case", mcp.Description("可选：忽略大小写（默认 false）")),
	)
}

func searchKnowledgeTool() mcp.Tool {
	return mcp.NewTool("search_knowledge",
		mcp.WithDescription("检索 Argus 知识库：组织/仓库级规范文档、历史审查结论与误报判例（markdown 知识 + Milvus 语义/本地 TF-IDF 混合检索，与审查管线 R3/R4 的 search_knowledge 同源）——回答「团队怎么做/这条以前怎么裁」类问题、审查与修复前对齐团队规范用。命中不等于结论；服务未配置知识源时返回错误"),
		mcp.WithString("query", mcp.Required(), mcp.Description("自然语言查询")),
		mcp.WithNumber("top_k", mcp.Description("可选：返回条数（缺省 5，上界 10）")),
	)
}

func searchSemTool() mcp.Tool {
	return mcp.NewTool("search_sem",
		mcp.WithDescription("在 PR 工作副本内做语义检索（按意图/自然语言找代码，如「哪里处理认证」「哪里刷新 session token」——代码里未必有查询词，词法 search_code 查不到）。结构化摘要 embedding + 进程内余弦，命中按相似度降序带 file:line 与摘要——命中是线索，引用前须 get_file 核实。服务未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("query", mcp.Required(), mcp.Description("自然语言意图描述")),
		mcp.WithNumber("top_k", mcp.Description("可选：返回条数（缺省 5，上界 10）")),
	)
}

func reviewDiffAsyncTool() mcp.Tool {
	return mcp.NewTool("review_diff_async",
		mcp.WithDescription("把一段 diff 作为异步预审任务提交（≤48KB），立即返回 task_id——提交时触发、稍后轮询的闭环形态。用 get_report(task_id) 取结果（意见列表或「✅ 未发现问题」）；需同步等结果的快审用 review_diff"),
		mcp.WithString("diff", mcp.Required(), mcp.Description("完整 diff 文本")),
	)
}

func getReportTool() mcp.Tool {
	return mcp.NewTool("get_report",
		mcp.WithDescription("按 taskId 获取 Argus 审查结果：状态/verdict/完整报告 markdown"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}

// ---------- 工具执行 ----------

// runReview 建任务 →（可选）轮询至终态 → 结构化结论。
func runReview(ctx context.Context, core Core, req mcp.CallToolRequest, kind string) (*mcp.CallToolResult, error) {
	vals, rerr := requireArgs(req, "platform", "owner", "repo", "number")
	if rerr != nil {
		return rerr, nil
	}
	wait := true
	if b, ok := req.GetArguments()["wait"].(bool); ok {
		wait = b
	}
	timeout := 180 * time.Second
	if f, ok := req.GetArguments()["timeout_seconds"].(float64); ok && f > 0 {
		timeout = time.Duration(f * float64(time.Second))
	}
	// 上界 clamp：无上界的 wait 会变成不可回收的轮询协程（客户端断连也要到点才停），
	// 并发挂起会挤占单连接 SQLite。需要更长等待请用 wait=false + get_report 轮询。
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}

	created, code := core.CreateTaskDirect(ctx, kind, vals[0], vals[1], vals[2], vals[3], nil)
	if code != http.StatusOK {
		return mcp.NewToolResultError(fmt.Sprintf("创建任务失败（HTTP %d），检查平台是否接入/参数是否合法", code)), nil
	}
	taskID := created.TaskId
	if !wait {
		b, _ := json.Marshal(map[string]any{"task_id": taskID, "status": created.Status})
		return mcp.NewToolResultText(string(b)), nil
	}
	return pollReviewTask(ctx, core, taskID, timeout), nil
}

// pollReviewTask 轮询任务至终态或超时：done/failed/stale 即刻返回；deadline 后
// 返回当前态+提示。自适应退避：1 分钟后轮询间隔从 2s 拉长到 5s，减少 DB 压力。
func pollReviewTask(ctx context.Context, core Core, taskID string, timeout time.Duration) *mcp.CallToolResult {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second
	adaptiveAfter := time.Now().Add(1 * time.Minute) // 1 分钟后拉长轮询间隔
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		t, code := core.GetTask(ctx, taskID)
		if code != http.StatusOK {
			return mcp.NewToolResultError(fmt.Sprintf("查询任务失败（HTTP %d）", code))
		}
		switch t.Status {
		case "done":
			return renderTerminalTask(ctx, core, t, "verdict")
		case "failed":
			reason := ""
			if t.FailReason != nil {
				reason = *t.FailReason
			}
			return mcp.NewToolResultError("审查失败: " + reason)
		case "stale":
			return renderTerminalTask(ctx, core, t, "note")
		}
		if time.Now().After(deadline) {
			b, _ := json.Marshal(map[string]any{
				"task_id": taskID, "status": t.Status,
				"note": "等待超时，任务仍在执行；稍后用 get_report 查询",
			})
			return mcp.NewToolResultText(string(b))
		}
		if time.Now().After(adaptiveAfter) && pollInterval < 5*time.Second {
			pollInterval = 5 * time.Second
			ticker.Reset(pollInterval)
		}
		select {
		case <-ctx.Done():
			return mcp.NewToolResultError("调用方取消")
		case <-ticker.C:
		}
	}
}

// renderTerminalTask done/stale 终态的结构化输出（verdict/report 必带，note 按态
// 填写；阶段进度与 get_report 同装饰）。
func renderTerminalTask(ctx context.Context, core Core, t *api.GetTaskResponse, noteField string) *mcp.CallToolResult {
	out := taskView(t)
	decorateStages(ctx, core, out, t.TaskId)
	if noteField == "note" {
		out["note"] = "审查完成但已有更新的提交，报告未回帖（staleness）"
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcp.NewToolResultText(string(b))
}

// maxWaitTimeout review_pr 等待终态的时长上界。
const maxWaitTimeout = 10 * time.Minute

// maxQuestionRunes ask_argus 追问长度上限（v3.134.0，成本/DoS 约束）。
const maxQuestionRunes = 8000

// defaultReviewKind review_pr 缺省审查类型（与 requirement_compliance 工具、
// schema enum 同词表；本文件此前 4 处字面量散写）。
const defaultReviewKind = "requirement_compliance"

// requireArgs 按序取必填字符串参数，任一缺失即错误结果（schema Required 的
// 运行期复核——此前逐参样板在本包 11 处复读）。
func requireArgs(req mcp.CallToolRequest, keys ...string) ([]string, *mcp.CallToolResult) {
	vals := make([]string, len(keys))
	for i, k := range keys {
		v, err := req.RequireString(k)
		if err != nil {
			return nil, mcp.NewToolResultError(err.Error())
		}
		vals[i] = v
	}
	return vals, nil
}

// taskView GetTaskResponse → 工具输出投影单源（get_report 与 pollReviewTask
// 终态渲染共用）：task_id/status 恒带，verdict/report/fail_reason 非空才带；
// 终态任务 FailReason 恒空，故对轮询渲染输出无形变。
func taskView(t *api.GetTaskResponse) map[string]any {
	out := map[string]any{"task_id": t.TaskId, "status": t.Status}
	if t.Verdict != nil {
		out["verdict"] = *t.Verdict
	}
	if t.Report != nil {
		out["report"] = *t.Report
	}
	if t.FailReason != nil {
		out["fail_reason"] = *t.FailReason
	}
	return out
}

// decorateStages 阶段进度并入任务视图（v3.127.0）：core 实现 Copilot 时附
// stages 列表与 stage_current（首个未完成阶段）——此前轮询 get_report 只见
// status=running，要进展必须再调 get_task_stages，多一跳且轮询循环里无从展示。
func decorateStages(ctx context.Context, core Core, out map[string]any, taskID string) {
	cop, ok := core.(Copilot)
	if !ok {
		return
	}
	sv, ok := cop.TaskStages(ctx, taskID)
	if !ok {
		return
	}
	stages, ok := sv["stages"].([]map[string]any)
	if !ok {
		return
	}
	out["stages"] = stages
	for _, st := range stages {
		if done, _ := st["done"].(bool); !done {
			if name, _ := st["stage"].(string); name != "" {
				out["stage_current"] = name
			}
			break
		}
	}
}

// 指纹短码合法长度与共享错误文案（get_suggestion / fix_finding 同口径）。
const (
	fpShortMin, fpShortMax = 6, 64
	fpShortMsg             = "fp 须为报告中的指纹短码（6-64 位十六进制）"
)

// validFPShort 指纹短码校验。
func validFPShort(fp string) bool { return len(fp) >= fpShortMin && len(fp) <= fpShortMax }

// askArgusTool ask_argus 工具 schema。
func askArgusTool() mcp.Tool {
	return mcp.NewTool("ask_argus",
		mcp.WithDescription("查询 Argus 对某 MR/PR 的最新审查结论（verdict + findings + 报告摘录）；"+
			"带 question 时基于该报告回答追问（如 \"为什么这条是 high\"）"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台：gitea/github/gitlab")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("question", mcp.Description("可选追问；缺省返回完整审查结论")),
	)
}

// argString 取可选字符串参数。
func argString(req mcp.CallToolRequest, key, def string) string {
	if v, ok := req.GetArguments()[key].(string); ok && v != "" {
		return v
	}
	return def
}

// argBool 取可选布尔参数（缺省/类型不符回 false）。
func argBool(req mcp.CallToolRequest, key string) bool {
	b, _ := req.GetArguments()[key].(bool)
	return b
}

// ---------- Hertz 桥接 ----------

// HertzBridge 把 mcp-go 的 streamable http handler 桥接为 hertz handler。
// 桥接保留响应头（Mcp-Session-Id 等）与状态码；GET 长连流不支持服务端推送刷新
// （工具调用为 POST 一问一答，不受影响）。
func HertzBridge(mcpSrv *server.MCPServer) app.HandlerFunc {
	inner := server.NewStreamableHTTPServer(mcpSrv)
	return func(ctx context.Context, c *app.RequestContext) {
		mw.SkipResponseWrap(c) // MCP JSON-RPC 协议载荷：客户端按规范解析顶层字段，禁止信封包裹
		req, err := http.NewRequest(string(c.Method()), string(c.URI().RequestURI()), bytes.NewReader(c.Request.Body()))
		if err != nil {
			c.SetStatusCode(http.StatusBadRequest)
			return
		}
		// 传播 hertz 请求的 ctx：客户端断连/超时能取消 inner handler（runReview 轮询随之退出），
		// 否则 ctx 恒为 background，取消永不传播。
		req = req.WithContext(ctx)
		req.URL.Scheme = "http"
		req.URL.Host = string(c.Host())
		c.Request.Header.VisitAll(func(k, v []byte) {
			req.Header.Add(string(k), string(v))
		})
		w := &bridgeWriter{c: c, header: http.Header{}}
		inner.ServeHTTP(w, req)
		for k, vs := range w.header {
			for _, v := range vs {
				c.Header(k, v)
			}
		}
	}
}

// bridgeWriter net/http ResponseWriter → hertz RequestContext 桥。
type bridgeWriter struct {
	c           *app.RequestContext
	header      http.Header
	wroteHeader bool
}

func (w *bridgeWriter) Header() http.Header { return w.header }

func (w *bridgeWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.c.Write(p)
}

func (w *bridgeWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.c.SetStatusCode(code)
	}
}

// Flush 将缓冲数据推送给客户端（Hertz hijack writer 或裸 Response）。
func (w *bridgeWriter) Flush() {
	_ = w.c.Flush()
}
