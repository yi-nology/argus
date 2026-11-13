// server_handlers.go —— MCP 工具执行体与参数助手（v3.165.0 自 server.go 拆出；
// 零逻辑变更）。

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

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
func renderTerminalTask(ctx context.Context, core Core, t *argus.GetTaskResponse, noteField string) *mcp.CallToolResult {
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
func taskView(t *argus.GetTaskResponse) map[string]any {
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
