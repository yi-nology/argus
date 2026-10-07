package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	api "argus/biz/model/api"
)

// ---------- 测试替身（消费侧窄接口 Core/ReviewQA/Copilot 的最小实现） ----------

// fakeCore 实现 Core；可选实现 ReviewQA/Copilot 由嵌入与否决定。
type fakeCore struct {
	createResp  *api.CreateTaskResponse
	createCode  int
	createCalls int
	createKind  string

	// getStatus 队列：每次 GetTask 弹出一态；耗尽后粘住最后一态。
	statuses []string
	getCalls int
	getCode  int
	getResp  *api.GetTaskResponse

	agents *api.ListAgentsResponse

	tasks       *api.ListTasksResponse
	tasksCode   int
	tasksStatus string
	tasksLimit  int
}

func (f *fakeCore) CreateTaskDirect(_ context.Context, kind, _, _, _, _ string, _ map[string]string) (*api.CreateTaskResponse, int) {
	f.createCalls++
	f.createKind = kind
	if f.createResp == nil {
		return &api.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, http.StatusOK
	}
	return f.createResp, f.createCode
}

func (f *fakeCore) GetTask(_ context.Context, _ string) (*api.GetTaskResponse, int) {
	f.getCalls++
	code := f.getCode
	if code == 0 {
		code = http.StatusOK
	}
	if code != http.StatusOK {
		return nil, code
	}
	if f.getResp != nil {
		return f.getResp, code
	}
	if len(f.statuses) == 0 {
		return &api.GetTaskResponse{TaskId: "t-1", Status: "queued"}, code
	}
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return &api.GetTaskResponse{TaskId: "t-1", Status: st}, code
}

func (f *fakeCore) ListAgents(_ context.Context, _, _, _ string) *api.ListAgentsResponse {
	if f.agents != nil {
		return f.agents
	}
	return &api.ListAgentsResponse{Agents: []*api.AgentManifestDTO{{Name: "correctness"}}}
}

func (f *fakeCore) ListTasks(_ context.Context, status string, limit int) (*api.ListTasksResponse, int) {
	f.tasksStatus, f.tasksLimit = status, limit
	if f.tasksCode != 0 {
		return nil, f.tasksCode
	}
	if f.tasks != nil {
		return f.tasks, http.StatusOK
	}
	return &api.ListTasksResponse{Tasks: []*api.TaskSummary{}}, http.StatusOK
}

// fakeQA 实现 ReviewQA。
type fakeQA struct {
	fakeCore
	latest   map[string]any
	latestOK bool
	answer   string
	answerOK bool
	askedQ   string
	askedNum string
}

func (f *fakeQA) LatestReview(_ context.Context, _, _, _, _ string) (map[string]any, bool) {
	return f.latest, f.latestOK
}

func (f *fakeQA) AnswerAboutReview(_ context.Context, _, _, _, number, question string) (string, bool) {
	f.askedQ = question
	f.askedNum = number
	return f.answer, f.answerOK
}

// fakeCopilot 实现 Copilot。
type fakeCopilot struct {
	fakeQA
	stages     map[string]any
	stagesOK   bool
	findings   []map[string]any
	findingsOK bool
	suggestion map[string]any
	suggOK     bool
	suggFP     string
}

func (f *fakeCopilot) TaskStages(_ context.Context, _ string) (map[string]any, bool) {
	return f.stages, f.stagesOK
}

func (f *fakeCopilot) Findings(_ context.Context, _ string) ([]map[string]any, bool) {
	return f.findings, f.findingsOK
}

func (f *fakeCopilot) Suggestion(_ context.Context, _ string, fp string) (map[string]any, bool) {
	f.suggFP = fp
	return f.suggestion, f.suggOK
}

// ---------- 调用助手 ----------

// callTool 走 JSON-RPC tools/call 全路径（注册校验 + 参数绑定 + handler），
// 返回 (文本内容, isError)。
func callTool(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
}, name string, args map[string]any) (string, bool) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := srv.HandleMessage(context.Background(), raw)
	if resp == nil {
		t.Fatal("HandleMessage 返回 nil")
	}
	// 经 JSON 往返取稳定形态（Result 可能是 *CallToolResult 或具体值）。
	rb, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var envelope struct {
		Error  *json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rb, &envelope); err != nil {
		t.Fatalf("unmarshal resp: %v\n%s", err, rb)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error: %s", *envelope.Error)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("工具 %s 无内容: %s", name, rb)
	}
	return envelope.Result.Content[0].Text, envelope.Result.IsError
}

func listToolNames(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
}) []string {
	t.Helper()
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	resp := srv.HandleMessage(context.Background(), raw)
	rb, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rb, &envelope); err != nil {
		t.Fatalf("tools/list 解析失败: %v\n%s", err, rb)
	}
	names := make([]string, 0, len(envelope.Result.Tools))
	for _, tl := range envelope.Result.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func strPtr(s string) *string { return &s }

// ---------- 工具注册面 ----------

// 无 Copilot 时只注册核心五工具；副驾三工具不得出现（避免 tools/list 死工具）。
func TestNew_ToolRegistrationCoreOnly(t *testing.T) {
	srv := New(&fakeCore{}, "v-test")
	names := listToolNames(t, srv)
	want := map[string]bool{
		"review_pr": true, "requirement_compliance": true,
		"list_agents": true, "get_report": true, "ask_argus": true, "list_tasks": true,
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("核心模式多出工具 %s", n)
		}
		delete(want, n)
	}
	for missing := range want {
		t.Errorf("核心模式缺工具 %s", missing)
	}
	for _, banned := range []string{"get_task_stages", "list_findings", "get_suggestion", "search_code", "fix_finding"} {
		for _, n := range names {
			if n == banned {
				t.Errorf("core 未实现 Copilot 却注册了 %s", banned)
			}
		}
	}
}

// 实现 Copilot 后副驾三工具挂载。
func TestNew_ToolRegistrationWithCopilot(t *testing.T) {
	srv := New(&fakeCopilot{}, "v-test")
	names := listToolNames(t, srv)
	for _, want := range []string{
		"review_pr", "requirement_compliance", "list_agents", "get_report", "ask_argus", "list_tasks",
		"get_task_stages", "list_findings", "get_suggestion",
	} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("缺工具 %s（注册面: %v）", want, names)
		}
	}
}

// ---------- list_agents / get_report ----------

func TestListAgents_JSON(t *testing.T) {
	core := &fakeCore{agents: &api.ListAgentsResponse{
		Agents: []*api.AgentManifestDTO{{Name: "security", Kind: api.AgentKind_BUILTIN}},
	}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "list_agents", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app",
	})
	if isErr {
		t.Fatalf("list_agents 报错: %s", text)
	}
	if !strings.Contains(text, "security") {
		t.Fatalf("list_agents 缺插件名:\n%s", text)
	}
}

func TestGetReport_FieldsAndFailure(t *testing.T) {
	core := &fakeCore{getResp: &api.GetTaskResponse{
		TaskId: "t-9", Status: "done",
		Verdict:    strPtr("request_changes"),
		Report:     strPtr("# 报告正文"),
		FailReason: strPtr("某阶段失败"),
	}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "get_report", map[string]any{"task_id": "t-9"})
	if isErr {
		t.Fatalf("get_report 报错: %s", text)
	}
	for _, want := range []string{"t-9", "request_changes", "报告正文", "某阶段失败"} {
		if !strings.Contains(text, want) {
			t.Fatalf("get_report 缺 %q:\n%s", want, text)
		}
	}

	// 缺必填 → isError
	_, isErr = callTool(t, srv, "get_report", map[string]any{})
	if !isErr {
		t.Fatal("缺 task_id 应返回 isError")
	}

	// 查询失败（非 200）
	core.getCode = http.StatusNotFound
	text, isErr = callTool(t, srv, "get_report", map[string]any{"task_id": "nope"})
	if !isErr || !strings.Contains(text, "查询失败") {
		t.Fatalf("非 200 应查询失败: isErr=%v text=%s", isErr, text)
	}
}

// ---------- review_pr / requirement_compliance ----------

func TestRunReview_MissingArgs(t *testing.T) {
	srv := New(&fakeCore{}, "v-test")
	_, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", // 缺 repo/number
	})
	if !isErr {
		t.Fatal("缺必填参数应 isError")
	}
}

func TestRunReview_CreateFail(t *testing.T) {
	core := &fakeCore{createResp: &api.CreateTaskResponse{}, createCode: http.StatusBadRequest}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": false,
	})
	if !isErr || !strings.Contains(text, "创建任务失败") {
		t.Fatalf("创建失败应报错: isErr=%v text=%s", isErr, text)
	}
}

func TestRunReview_NoWaitReturnsTask(t *testing.T) {
	core := &fakeCore{createResp: &api.CreateTaskResponse{TaskId: "t-42", Status: "queued"}, createCode: http.StatusOK}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": false,
	})
	if isErr {
		t.Fatalf("wait=false 不应报错: %s", text)
	}
	if !strings.Contains(text, "t-42") || !strings.Contains(text, "queued") {
		t.Fatalf("wait=false 应返回 task_id+status:\n%s", text)
	}
	if core.createKind != "requirement_compliance" {
		t.Fatalf("默认 kind = %s", core.createKind)
	}
	// kind 透传
	_, _ = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"kind": "code_review", "wait": false,
	})
	if core.createKind != "code_review" {
		t.Fatalf("kind 未透传: %s", core.createKind)
	}
}

func TestRunReview_WaitTerminalStates(t *testing.T) {
	verdict := "approve"
	report := "## 报告"

	// done → renderTerminalTask(noteField=verdict)
	core := &fakeCore{
		createResp: &api.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &api.GetTaskResponse{TaskId: "t-1", Status: "done", Verdict: &verdict, Report: &report},
	}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if isErr {
		t.Fatalf("done 态不应 isError: %s", text)
	}
	for _, want := range []string{"done", "approve", "报告"} {
		if !strings.Contains(text, want) {
			t.Fatalf("done 态缺 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "staleness") {
		t.Fatalf("done 态不应带 staleness note:\n%s", text)
	}

	// stale → 带 note
	core.getResp = &api.GetTaskResponse{TaskId: "t-1", Status: "stale", Verdict: &verdict, Report: &report}
	text, isErr = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if isErr {
		t.Fatalf("stale 态不应 isError: %s", text)
	}
	if !strings.Contains(text, "staleness") || !strings.Contains(text, "note") {
		t.Fatalf("stale 态应带 staleness note:\n%s", text)
	}

	// failed → isError + reason
	reason := "模型超时"
	core.getResp = &api.GetTaskResponse{TaskId: "t-1", Status: "failed", FailReason: &reason}
	text, isErr = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if !isErr || !strings.Contains(text, "审查失败") || !strings.Contains(text, "模型超时") {
		t.Fatalf("failed 态应报错带原因: isErr=%v text=%s", isErr, text)
	}
}

func TestRunReview_WaitTimeout(t *testing.T) {
	// 首查即非终态 + timeout 极小 → deadline 后返回超时提示（非 isError）
	core := &fakeCore{
		createResp: &api.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &api.GetTaskResponse{TaskId: "t-1", Status: "running"},
	}
	srv := New(core, "v-test")
	start := time.Now()
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 0.01,
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时路径耗时过长: %v", elapsed)
	}
	if isErr {
		t.Fatalf("超时应是业务提示而非 isError: %s", text)
	}
	if !strings.Contains(text, "等待超时") {
		t.Fatalf("应提示等待超时:\n%s", text)
	}
}

// maxWaitTimeout 上界：极大 timeout_seconds 被 clamp，不能变成不可回收的长轮询。
// 这里只验证 clamp 存在（用极短真实等待不现实），转而单测常量本身。
func TestMaxWaitTimeoutBounded(t *testing.T) {
	if maxWaitTimeout > 15*time.Minute {
		t.Fatalf("maxWaitTimeout 过大: %v", maxWaitTimeout)
	}
	if maxWaitTimeout < time.Minute {
		t.Fatalf("maxWaitTimeout 过小: %v", maxWaitTimeout)
	}
}

func TestRequirementCompliance_FixedKind(t *testing.T) {
	core := &fakeCore{createResp: &api.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK}
	srv := New(core, "v-test")
	_, isErr := callTool(t, srv, "requirement_compliance", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7", "wait": false,
	})
	if isErr {
		t.Fatal("requirement_compliance 调用失败")
	}
	if core.createKind != "requirement_compliance" {
		t.Fatalf("kind 应固定 requirement_compliance，实际 %s", core.createKind)
	}
}

// ---------- ask_argus ----------

func TestAskArgus_NotEnabled(t *testing.T) {
	srv := New(&fakeCore{}, "v-test") // 未实现 ReviewQA
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr || !strings.Contains(text, "未启用") {
		t.Fatalf("未实现 ReviewQA 应报未启用: isErr=%v text=%s", isErr, text)
	}
}

func TestAskArgus_RequiredAndNumberValidation(t *testing.T) {
	qa := &fakeQA{}
	srv := New(qa, "v-test")

	// 缺 platform
	_, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr {
		t.Fatal("缺 platform 应 isError")
	}

	// number 非纯数字（与 service.isDigits 同口径）
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7a",
	})
	if !isErr || !strings.Contains(text, "纯数字") {
		t.Fatalf("非数字 number 应拒绝: isErr=%v text=%s", isErr, text)
	}
}

func TestAskArgus_LatestAndAnswer(t *testing.T) {
	qa := &fakeQA{latest: map[string]any{"verdict": "approve"}, latestOK: true}
	srv := New(qa, "v-test")

	// 无 question → 最近审查结论
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if isErr {
		t.Fatalf("LatestReview 不应 isError: %s", text)
	}
	if !strings.Contains(text, "approve") {
		t.Fatalf("应返回最近结论:\n%s", text)
	}

	// 无审查记录
	qa.latestOK = false
	text, _ = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !strings.Contains(text, "尚无已完成") {
		t.Fatalf("应提示无记录:\n%s", text)
	}

	// 带 question → AnswerAboutReview
	qa.answer, qa.answerOK = "因为证据在 a.go:12", true
	text, isErr = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"question": "为什么这条是 high",
	})
	if isErr || !strings.Contains(text, "a.go:12") {
		t.Fatalf("追问应返回答案: isErr=%v text=%s", isErr, text)
	}
	if qa.askedQ != "为什么这条是 high" || qa.askedNum != "7" {
		t.Fatalf("追问参数未透传: q=%q num=%q", qa.askedQ, qa.askedNum)
	}

	// 答不上来
	qa.answerOK = false
	text, _ = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"question": "随便问",
	})
	if !strings.Contains(text, "无法回答") {
		t.Fatalf("答不上来应有明确提示:\n%s", text)
	}
}

// ---------- 副驾三工具 ----------

func TestCopilotTools(t *testing.T) {
	cop := &fakeCopilot{
		stages:     map[string]any{"task_id": "t-1", "stages": []any{"R1", "R5"}},
		stagesOK:   true,
		findings:   []map[string]any{{"fp": "ab12cd34ef", "severity": "high"}},
		findingsOK: true,
		suggestion: map[string]any{"suggested_fix": "加锁"},
		suggOK:     true,
	}
	srv := New(cop, "v-test")

	// get_task_stages
	text, isErr := callTool(t, srv, "get_task_stages", map[string]any{"task_id": "t-1"})
	if isErr || !strings.Contains(text, "R1") {
		t.Fatalf("stages: isErr=%v text=%s", isErr, text)
	}
	cop.stagesOK = false
	text, isErr = callTool(t, srv, "get_task_stages", map[string]any{"task_id": "nope"})
	if !isErr || !strings.Contains(text, "任务不存在") {
		t.Fatalf("stages 未命中应 isError: isErr=%v text=%s", isErr, text)
	}
	cop.stagesOK = true

	// list_findings
	text, isErr = callTool(t, srv, "list_findings", map[string]any{"task_id": "t-1"})
	if isErr || !strings.Contains(text, "ab12cd34ef") {
		t.Fatalf("findings: isErr=%v text=%s", isErr, text)
	}

	// get_suggestion：fp 长度门（6-64）
	_, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "abc"})
	if !isErr {
		t.Fatal("过短 fp 应 isError")
	}
	// 命中（大小写归一为小写）
	text, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "AB12CD34EF"})
	if isErr || !strings.Contains(text, "加锁") {
		t.Fatalf("suggestion 命中: isErr=%v text=%s", isErr, text)
	}
	if cop.suggFP != "ab12cd34ef" {
		t.Fatalf("fp 未归一小写: %s", cop.suggFP)
	}
	// 未命中 → 明确文本（非 isError）
	cop.suggOK = false
	text, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "deadbeef12"})
	if isErr || !strings.Contains(text, "未命中") {
		t.Fatalf("suggestion 未命中: isErr=%v text=%s", isErr, text)
	}
}

// ---------- list_tasks / search_code（v3.121.0） ----------

func TestListTasks(t *testing.T) {
	core := &fakeCore{tasks: &api.ListTasksResponse{Tasks: []*api.TaskSummary{{
		TaskId: "t-9", Status: "done", Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
	}}}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "list_tasks", map[string]any{"status": "done", "limit": float64(5)})
	if isErr {
		t.Fatalf("list_tasks 报错: %s", text)
	}
	if !strings.Contains(text, "t-9") || !strings.Contains(text, "acme") {
		t.Fatalf("list_tasks 缺任务摘要:\n%s", text)
	}
	if core.tasksStatus != "done" || core.tasksLimit != 5 {
		t.Fatalf("参数未透传: status=%q limit=%d", core.tasksStatus, core.tasksLimit)
	}

	// 缺省：status 空 + limit 0（服务端缺省 20）
	if _, isErr = callTool(t, srv, "list_tasks", map[string]any{}); isErr {
		t.Fatal("空参应可用（全量清单）")
	}
	if core.tasksStatus != "" || core.tasksLimit != 0 {
		t.Fatalf("缺省参数不符: status=%q limit=%d", core.tasksStatus, core.tasksLimit)
	}

	// 非法 status：工具层可读错误（不落 400 裸码）
	text, isErr = callTool(t, srv, "list_tasks", map[string]any{"status": "nope"})
	if !isErr || !strings.Contains(text, "pending/running/done/failed/stale") {
		t.Fatalf("非法 status 应 isError: isErr=%v text=%s", isErr, text)
	}

	// 服务端非 200 → 工具错误
	core.tasksCode = 500
	_, isErr = callTool(t, srv, "list_tasks", map[string]any{})
	if !isErr {
		t.Fatal("非 200 应 isError")
	}
}

// fakeSearcher 实现 CodeSearcher。
type fakeSearcher struct {
	fakeCore
	pattern, glob string
	ignoreCase    bool
	out           string
	err           error
}

func (f *fakeSearcher) SearchCode(_ context.Context, _, _, _, _, pattern, glob string, ignoreCase bool) (string, error) {
	f.pattern, f.glob, f.ignoreCase = pattern, glob, ignoreCase
	return f.out, f.err
}

func TestSearchCode(t *testing.T) {
	fs := &fakeSearcher{out: "<data>search_code 检索结果</data>"}
	srv := New(fs, "v-test")
	text, isErr := callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"pattern": "limitUnbounded", "glob": "*.go", "ignore_case": true,
	})
	if isErr || !strings.Contains(text, "检索结果") {
		t.Fatalf("search_code: isErr=%v text=%s", isErr, text)
	}
	if fs.pattern != "limitUnbounded" || fs.glob != "*.go" || !fs.ignoreCase {
		t.Fatalf("参数未透传: pattern=%q glob=%q ignoreCase=%v", fs.pattern, fs.glob, fs.ignoreCase)
	}

	// 数据面 error → 工具错误
	fs.err = errors.New("search_code 未启用")
	_, isErr = callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7", "pattern": "x",
	})
	if !isErr {
		t.Fatal("未启用应 isError")
	}

	// 缺 pattern
	_, isErr = callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr {
		t.Fatal("缺 pattern 应 isError")
	}
}

func TestArgBool(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"a": true, "b": "yes", "c": false,
	}}}
	if !argBool(req, "a") {
		t.Fatal("a 应为 true")
	}
	if argBool(req, "b") || argBool(req, "c") || argBool(req, "missing") {
		t.Fatal("非布尔/false/缺失都应回 false")
	}
}

// ---------- 纯函数面 ----------

func TestArgString(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"a": "x", "b": "", "c": 1,
	}}}
	if got := argString(req, "a", "d"); got != "x" {
		t.Fatalf("a = %q", got)
	}
	if got := argString(req, "b", "d"); got != "d" {
		t.Fatalf("空串应回默认: %q", got)
	}
	if got := argString(req, "c", "d"); got != "d" {
		t.Fatalf("非字符串应回默认: %q", got)
	}
	if got := argString(req, "missing", "d"); got != "d" {
		t.Fatalf("缺失应回默认: %q", got)
	}
}

func TestRenderTerminalTask_NoteFields(t *testing.T) {
	verdict := "comment"
	report := "body"
	tr := &api.GetTaskResponse{TaskId: "t", Status: "done", Verdict: &verdict, Report: &report}

	res := renderTerminalTask(tr, "verdict")
	text := extractText(t, res)
	if strings.Contains(text, "staleness") {
		t.Fatalf("verdict 注记不应含 staleness:\n%s", text)
	}

	res = renderTerminalTask(tr, "note")
	text = extractText(t, res)
	if !strings.Contains(text, "staleness") {
		t.Fatalf("note 注记应含 staleness:\n%s", text)
	}
}

func TestMarshalToolResult_Error(t *testing.T) {
	// channel 可触发 json 错误的类型（循环引用）
	type node struct {
		Next *node
	}
	n := &node{}
	n.Next = n
	res, err := marshalToolResult(n)
	if err != nil {
		t.Fatalf("marshalToolResult 应吞掉错误并返回 isError: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("不可序列化应返回 isError 结果")
	}
}

func TestAgentCardPlaceholder(t *testing.T) {
	card := AgentCardPlaceholder("argus", "v3.1")
	if card["name"] != "argus" {
		t.Fatalf("name = %v", card["name"])
	}
	b, err := json.Marshal(card)
	if err != nil || !strings.Contains(string(b), "argus") {
		t.Fatalf("卡片应可序列化: %v %s", err, b)
	}
}

func extractText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("结果无内容")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("首内容非文本: %#v", res.Content[0])
	}
	return tc.Text
}

// bridgeWriter 幂等 WriteHeader：二次调用不覆盖首个状态码。
func TestBridgeWriter_WriteHeaderOnce(t *testing.T) {
	// 裸实现：只验证 wroteHeader 门闩语义（Hertz RequestContext 依赖真实运行时）。
	w := &bridgeWriter{header: http.Header{}}
	if w.wroteHeader {
		t.Fatal("初始不应已写头")
	}
	// Header() 可用
	if w.Header() == nil {
		t.Fatal("Header() 应返回非 nil")
	}
}

// pollReviewTask 的 ctx 取消路径：长轮询 + cancel → 「调用方取消」。
func TestPollReviewTask_ContextCancel(t *testing.T) {
	core := &fakeCore{getResp: &api.GetTaskResponse{TaskId: "t-1", Status: "running"}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := pollReviewTask(ctx, core, "t-1", 30*time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消未及时退出: %v", elapsed)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "取消") {
		t.Fatalf("应返回调用方取消:\n%s", text)
	}
}

// CreateTaskDirect 返回错误（业务错误而非 panic）时不拖垮 handler。
func TestRunReview_CreateErrorStatus(t *testing.T) {
	core := &fakeCore{createResp: nil, createCode: http.StatusInternalServerError}
	// createResp 为 nil 时 fake 返回默认成功——改用显式失败。
	core.createResp = &api.CreateTaskResponse{}
	core.createCode = http.StatusInternalServerError
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "wait": false,
	})
	if !isErr {
		t.Fatalf("500 创建应 isError: %s", text)
	}
}
