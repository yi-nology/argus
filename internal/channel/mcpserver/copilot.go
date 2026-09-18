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
		taskID, err := req.RequireString("task_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, ok := cop.TaskStages(ctx, taskID)
		if !ok {
			return mcp.NewToolResultError("任务不存在: " + taskID), nil
		}
		return marshalToolResult(data)
	})
	s.AddTool(listFindingsTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := req.RequireString("task_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, ok := cop.Findings(ctx, taskID)
		if !ok {
			return mcp.NewToolResultError("任务不存在: " + taskID), nil
		}
		return marshalToolResult(data)
	})
	s.AddTool(getSuggestionTool(), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID, err := req.RequireString("task_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fp, err := req.RequireString("fp")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(fp) < 6 || len(fp) > 64 {
			return mcp.NewToolResultError("fp 须为报告中的指纹短码（6-64 位十六进制）"), nil
		}
		data, ok := cop.Suggestion(ctx, taskID, strings.ToLower(fp))
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

// marshalToolResult 统一 JSON 缩进输出（与既有工具同风格）。
func marshalToolResult(data any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
