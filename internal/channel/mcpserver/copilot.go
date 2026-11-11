// copilot.go —— MCP 副驾服务端底座（v3.15.0 P2-7，批次口径 v3.17.0）：
// 「IDE 追问 / 阶段可视化 / 一键回写」三能力的数据面。工具只读、零 LLM：
//
//	get_task_stages  任务阶段时间线（stage_artifacts/debug/终态推导，审得全不全可观测）
//	list_findings    结构化 findings（含 fp 短码与 suggested_fix，替代解析 markdown）
//	get_suggestion   按指纹短码取回单条建议（补丁内容由 IDE 侧执行应用）
//
// 可选接口模式（同 ReviewQA）：core 未实现时工具返回明确错误文本。
package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Copilot 可选扩展：副驾数据面。*service.Service 实现（nil 安全）。
type Copilot interface {
	// TaskStages 任务阶段时间线；无任务返回 false。
	TaskStages(ctx context.Context, taskID string) (map[string]any, bool)
	// Findings 结构化意见清单（含 fp/suggested_fix）；无任务返回 false。
	Findings(ctx context.Context, taskID string) ([]map[string]any, bool)
	// Suggestion 按指纹短码（≥6 位）取回单条建议；未命中返回 false。
	Suggestion(ctx context.Context, taskID, fpPrefix string) (map[string]any, bool)
}

// NewCopilotTools 注册三副驾工具（New 内调用；core 未实现 Copilot 时跳过注册，
// /mcp tools/list 不出现死工具）。
func NewCopilotTools(s *server.MCPServer, core Core) {
	cop, ok := core.(Copilot)
	if !ok {
		return
	}
	s.AddTool(taskStagesTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "task_id")
		if rerr != nil {
			return rerr, nil
		}
		data, ok := cop.TaskStages(ctx, vals[0])
		if !ok {
			return mcp.NewToolResultError("任务不存在: " + vals[0]), nil
		}
		return marshalToolResult(data)
	})
	s.AddTool(listFindingsTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "task_id")
		if rerr != nil {
			return rerr, nil
		}
		data, ok := cop.Findings(ctx, vals[0])
		if !ok {
			return mcp.NewToolResultError("任务不存在: " + vals[0]), nil
		}
		return marshalToolResult(data)
	})
	s.AddTool(getSuggestionTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "task_id", "fp")
		if rerr != nil {
			return rerr, nil
		}
		if !validFPShort(vals[1]) {
			return mcp.NewToolResultError(fpShortMsg), nil
		}
		data, ok := cop.Suggestion(ctx, vals[0], strings.ToLower(vals[1]))
		if !ok {
			return mcp.NewToolResultText("未命中该指纹短码，请核对 list_findings 返回的 fp 后重试。"), nil
		}
		return marshalToolResult(data)
	})
}

func taskStagesTool() mcp.Tool {
	return mcp.NewTool("get_task_stages",
		mcp.WithDescription("查询 Argus 审查任务的阶段时间线（R1 需求解析/R2 变更理解/R3 完成度判定/R3.5 反思/R4 专家委派/R5 裁决各自的完成状态与依据）——审得全不全可观测"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}

func listFindingsTool() mcp.Tool {
	return mcp.NewTool("list_findings",
		mcp.WithDescription("按 taskId 获取结构化审查意见（file/line/severity/comment/by/fp 指纹短码/suggested_fix），无需解析 markdown 报告"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}

func getSuggestionTool() mcp.Tool {
	return mcp.NewTool("get_suggestion",
		mcp.WithDescription("按 fp 指纹短码取回单条意见的修复建议内容（IDE/客户端据此执行一键应用；本工具只读取回，不改动任何代码）"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
		mcp.WithString("fp", mcp.Required(), mcp.Description("意见指纹短码（报告/list_findings 中 fp: 后的十六进制串）")),
	)
}

// Fixer 修复执行扩展（v3.89.0 可选接口）：core 未实现时 fix_finding 不注册。
// 修复面是 /mcp 唯一的写侧工具：只落在隔离沙箱、绝不 push，结果由客户端/人确认。
type Fixer interface {
	// FixFinding 同步执行单条意见的沙箱修复（含确定性验证），返回结果 JSON 面。
	FixFinding(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error)
}

// NewFixerTool 注册 fix_finding（New 内调用；core 未实现 Fixer 时跳过注册）。
func NewFixerTool(s *server.MCPServer, core Core) {
	fix, ok := core.(Fixer)
	if !ok {
		return
	}
	s.AddTool(fixFindingTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "fp")
		if rerr != nil {
			return rerr, nil
		}
		if !validFPShort(vals[4]) {
			return mcp.NewToolResultError(fpShortMsg), nil
		}
		data, err := fix.FixFinding(ctx, vals[0], vals[1], vals[2], vals[3], strings.ToLower(vals[4]))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return marshalToolResult(data)
	})
}

func fixFindingTool() mcp.Tool {
	return mcp.NewTool("fix_finding",
		mcp.WithDescription("按 PR 与指纹短码执行单条审查意见的自动修复：隔离沙箱内编码 agent 出 patch + 确定性验证（ARGUS_FIX_VERIFY_CMD），返回 diff 与验证结论。绝不推送分支；服务未启用 ARGUS_FIX_AGENT 时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("平台（"+platformVocab+"）")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库属主")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 编号")),
		mcp.WithString("fp", mcp.Required(), mcp.Description("意见指纹短码（报告/list_findings 中 fp: 后的十六进制串）")),
	)
}

// CapabilityFace 能力快照扩展（v3.129.0）：get_capabilities 的数据面。
// *service.Service 实现；返回部署启用面（llm/fix/检索等）与版本。
type CapabilityFace interface {
	Capabilities(ctx context.Context) map[string]any
}

// NewCapabilityTool 注册 get_capabilities（New 内调用；core 未实现时跳过）。
func NewCapabilityTool(s *server.MCPServer, core Core) {
	cf, ok := core.(CapabilityFace)
	if !ok {
		return
	}
	s.AddTool(capabilitiesTool(), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return marshalToolResult(cf.Capabilities(ctx))
	})
}

func capabilitiesTool() mcp.Tool {
	return mcp.NewTool("get_capabilities",
		mcp.WithDescription("查询本部署启用的能力面与版本（llm/diff_preview/qa/code_search/sem_search/knowledge/fix_agent/fix_push/codegraph/related_repos/ocr）——调用受限工具（fix_finding/suppress_finding/search_sem/search_knowledge）前先看这里，免盲调报错；纯读配置态，零副作用"),
	)
}

// FindingSuppressor 误报抑制扩展（v3.128.0 可选接口）：core 未实现时工具不注册。
// 与 fix_finding 同为写侧工具，但权限更严：服务端按 scope 透传做 admin 二次判定
// （/mcp 路由只需 ci 档），非 admin 调用返回明确错误——绝不静默放行。
type FindingSuppressor interface {
	// SuppressFindingScoped 按 fp 短码抑制单条意见（入库指纹解析，近重复容忍），
	// 返回 {matched, fingerprint}。
	SuppressFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error)
}

// NewSuppressorTool 注册 suppress_finding（New 内调用；core 未实现 FindingSuppressor
// 时跳过注册）。
func NewSuppressorTool(s *server.MCPServer, core Core) {
	sup, ok := core.(FindingSuppressor)
	if !ok {
		return
	}
	s.AddTool(suppressFindingTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "fp")
		if rerr != nil {
			return rerr, nil
		}
		if !validFPShort(vals[4]) {
			return mcp.NewToolResultError(fpShortMsg), nil
		}
		data, err := sup.SuppressFindingScoped(ctx, vals[0], vals[1], vals[2], vals[3], strings.ToLower(vals[4]))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return marshalToolResult(data)
	})
}

func suppressFindingTool() mcp.Tool {
	return mcp.NewTool("suppress_finding",
		mcp.WithDescription("把一条审查意见标记为误报并抑制（按 fp 指纹短码，行号/措辞漂移容忍）：后续报告自动剔除该意见，结论回流知识库与学习环。**需 admin 档 token**（服务端 scope 透传二次判定，ci 档调用会报权限错）——确认是误报再调，这是信任边界写操作"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("平台（"+platformVocab+"）")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库属主")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 编号")),
		mcp.WithString("fp", mcp.Required(), mcp.Description("意见指纹短码（报告/list_findings 中 fp: 后的十六进制串）")),
	)
}

// FindingRestorer 恢复扩展（v3.131.0 可选接口）：core 未实现时工具不注册。
// suppress_finding 的反向——被误抑制的意见置回 open。权限同 suppress：服务端
// scope 透传做 admin 二次判定。
type FindingRestorer interface {
	// RestoreFindingScoped 按 fp 短码恢复意见，返回 {matched}。
	RestoreFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error)
}

// NewRestorerTool 注册 restore_finding（New 内调用；core 未实现 FindingRestorer
// 时跳过注册）。
func NewRestorerTool(s *server.MCPServer, core Core) {
	res, ok := core.(FindingRestorer)
	if !ok {
		return
	}
	s.AddTool(restoreFindingTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo", "number", "fp")
		if rerr != nil {
			return rerr, nil
		}
		if !validFPShort(vals[4]) {
			return mcp.NewToolResultError(fpShortMsg), nil
		}
		data, err := res.RestoreFindingScoped(ctx, vals[0], vals[1], vals[2], vals[3], strings.ToLower(vals[4]))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return marshalToolResult(data)
	})
}

func restoreFindingTool() mcp.Tool {
	return mcp.NewTool("restore_finding",
		mcp.WithDescription("恢复一条被误抑制的审查意见（suppress_finding 的反向，按 fp 指纹短码）：置回 open，后续报告重新可见。**需 admin 档 token**（服务端 scope 透传二次判定，ci 档调用会报权限错）——抑制错了用它撤销"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("平台（"+platformVocab+"）")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库属主")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 编号")),
		mcp.WithString("fp", mcp.Required(), mcp.Description("意见指纹短码（报告/list_findings 中 fp: 后的十六进制串；抑制记录的短码同样可解析）")),
	)
}

// FindingHistorian 历史意见检索扩展（v3.138.0）：core 未实现时工具不注册。
type FindingHistorian interface {
	// SearchFindingsHistory 本仓 prints 账本跨轮检索，返回围栏文本。
	SearchFindingsHistory(ctx context.Context, platform, owner, repo, query string, limit int) (string, error)
}

// NewHistorianTool 注册 search_findings（New 内调用；core 未实现 FindingHistorian
// 时跳过注册）。
func NewHistorianTool(s *server.MCPServer, core Core) {
	hist, ok := core.(FindingHistorian)
	if !ok {
		return
	}
	s.AddTool(searchFindingsTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		vals, rerr := requireArgs(req, "platform", "owner", "repo", "query")
		if rerr != nil {
			return rerr, nil
		}
		limit := 0 // 0 = 服务端缺省 10、钳 50
		if f, ok := req.GetArguments()["limit"].(float64); ok && f > 0 {
			limit = int(f)
		}
		out, err := hist.SearchFindingsHistory(ctx, vals[0], vals[1], vals[2], vals[3], limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func searchFindingsTool() mcp.Tool {
	return mcp.NewTool("search_findings",
		mcp.WithDescription("检索本仓库的历史审查意见流水（跨轮次/跨任务，prints 账本）：「这个问题以前报过吗/当时怎么裁的/被误报抑制过吗/复现了几次」——带 status（open/suppressed）与复现次数，fp 短码可直接喂 suppress/restore/fix。与 search_knowledge（组织级规范/结论）分工：这里查本仓意见账本"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("query", mcp.Required(), mcp.Description("关键词（匹配评语/文件路径；含 % 按通配）")),
		mcp.WithNumber("limit", mcp.Description("可选：条数（缺省 10，上界 50）")),
	)
}

// marshalToolResult 统一 JSON 缩进输出（与既有工具同风格）。
func marshalToolResult(data any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
