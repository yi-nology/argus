// Package mcpserver L1/L2 门面：Argus as MCP Server（design §9.2 方向 B，v3.1 实装）。
// 把 Argus 的审查能力以 MCP 工具暴露给任意 MCP 客户端（review-service、agent 工具链等）：
//
//	review_pr / requirement_compliance  发起审查（可选等待终态，返回 verdict 与完整报告）
//	review_diff                          本地 staged diff 预审（v3.74.1）
//	ask_argus                            最新审查结论问答（v3.3 P5）
//	get_report                           按 taskId 取报告
//	list_tasks                           任务清单枚举（v3.122.0）
//	get_task_stages / list_findings / get_suggestion  副驾只读三件（v3.15.0）
//	search_code                          PR 工作副本受控检索（v3.122.0）
//	search_knowledge                     知识库语义检索（v3.125.0，R3/R4 同源）
//	search_sem                           工作副本语义检索（v3.126.0，embedding opt-in）
//	review_diff_async                    本地 diff 异步预审任务（v3.123.0，提交方轮询闭环）
//	fix_finding                          沙箱修复（v3.89.0，写侧）
//	suppress_finding                     误报抑制（v3.128.0，写侧，admin scope 门）
//	restore_finding                      恢复误抑制意见（v3.131.0，写侧，admin scope 门）
//	get_capabilities                     能力面快照（v3.129.0，部署开了什么一次看清）
//	search_findings                      本仓历史意见账本检索（v3.138.0）
//	get_impact                           改动影响面快照（v3.183.0，blast radius 结构化数据）
//	get_repo_config                      按仓配置与生效参数只读视图（v3.187.0，写入走 HTTP admin API）
//
// 传输：mark3labs/mcp-go streamable http，经 Hertz 桥接挂在 /mcp（GET/POST/DELETE），
// 鉴权与 🔒 路由一致（Bearer）。与 toolpool（方向 A：我们调用别人）分层不同，不合流。
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

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

// CodeSearcher 可选扩展（v3.122.0）：search_code 的数据面。*service.Service 实现。
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
	NewImpactTool(s, core)     // v3.183.0：core 实现 ImpactViewer（影响面快照）时注册
	NewRepoConfigTool(s, core) // v3.187.0：core 实现 RepoConfigViewer（按仓配置只读视图）时注册
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
