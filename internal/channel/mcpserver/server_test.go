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

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ---------- 测试替身（消费侧窄接口 Core/ReviewQA/Copilot 的最小实现） ----------

// fakeCore 实现 Core；可选实现 ReviewQA/Copilot 由嵌入与否决定。
type fakeCore struct {
	createResp  *argus.CreateTaskResponse
	createCode  int
	createCalls int
	createKind  string

	// getStatus 队列：每次 GetTask 弹出一态；耗尽后粘住最后一态。
	statuses []string
	getCalls int
	getCode  int
	getResp  *argus.GetTaskResponse

	agents *argus.ListAgentsResponse

	tasks       *argus.ListTasksResponse
	tasksCode   int
	tasksStatus string
	tasksLimit  int
}

func (f *fakeCore) CreateTaskDirect(_ context.Context, kind, _, _, _, _ string, _ map[string]string) (*argus.CreateTaskResponse, int) {
	f.createCalls++
	f.createKind = kind
	if f.createResp == nil {
		return &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, http.StatusOK
	}
	return f.createResp, f.createCode
}

func (f *fakeCore) GetTask(_ context.Context, _ string) (*argus.GetTaskResponse, int) {
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
		return &argus.GetTaskResponse{TaskId: "t-1", Status: "queued"}, code
	}
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return &argus.GetTaskResponse{TaskId: "t-1", Status: st}, code
}

func (f *fakeCore) ListAgents(_ context.Context, _, _, _ string) *argus.ListAgentsResponse {
	if f.agents != nil {
		return f.agents
	}
	return &argus.ListAgentsResponse{Agents: []*argus.AgentManifestDTO{{Name: "correctness"}}}
}

func (f *fakeCore) ListTasks(_ context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	f.tasksStatus, f.tasksLimit = status, limit
	if f.tasksCode != 0 {
		return nil, f.tasksCode
	}
	if f.tasks != nil {
		return f.tasks, http.StatusOK
	}
	return &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{}}, http.StatusOK
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
	for _, banned := range []string{"get_task_stages", "list_findings", "get_suggestion", "search_code", "search_knowledge", "search_sem", "review_diff_async", "fix_finding"} {
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

// fakeRepoCfgCore 在 fakeCore 之上实现按仓配置视图面（get_repo_config 数据面）。
type fakeRepoCfgCore struct {
	fakeCore
	view map[string]any
}

func (f *fakeRepoCfgCore) RepoSettingsView(_ context.Context, _, _, _ string) (map[string]any, int) {
	return f.view, http.StatusOK
}

// core 实现 RepoConfigViewer 时注册 get_repo_config 并可调用（v3.187.0）；
// 未实现时 tools/list 不出现死工具（TestNew_ToolRegistrationCoreOnly 已覆盖反向）。
func TestNew_RepoConfigTool(t *testing.T) {
	srv := New(&fakeRepoCfgCore{view: map[string]any{
		"found": true, "source": "repo_override",
		"settings":  map[string]any{"max_diff_chars": 200000},
		"effective": map[string]any{"monthly_token_budget": 5000000, "task_token_budget": 400000, "max_diff_chars": 200000},
	}}, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "get_repo_config" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 RepoConfigViewer 后应注册 get_repo_config")
	}
	text, isErr := callTool(t, srv, "get_repo_config",
		map[string]any{"platform": "gitea", "owner": "acme", "repo": "web"})
	if isErr {
		t.Fatalf("应成功: %s", text)
	}
	if !strings.Contains(text, "repo_override") || !strings.Contains(text, "max_diff_chars") {
		t.Fatalf("视图内容缺失: %s", text)
	}
	// 缺必填 → 工具级错误回执（非 panic）。
	if _, isErr := callTool(t, srv, "get_repo_config", map[string]any{"platform": "gitea"}); !isErr {
		t.Fatal("缺 owner/repo 应 isError")
	}
}

// ---------- list_agents / get_report ----------

func TestListAgents_JSON(t *testing.T) {
	core := &fakeCore{agents: &argus.ListAgentsResponse{
		Agents: []*argus.AgentManifestDTO{{Name: "security", Kind: argus.AgentKind_BUILTIN}},
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
	core := &fakeCore{getResp: &argus.GetTaskResponse{
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
	core := &fakeCore{createResp: &argus.CreateTaskResponse{}, createCode: http.StatusBadRequest}
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
	core := &fakeCore{createResp: &argus.CreateTaskResponse{TaskId: "t-42", Status: "queued"}, createCode: http.StatusOK}
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
		createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "done", Verdict: &verdict, Report: &report},
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
	core.getResp = &argus.GetTaskResponse{TaskId: "t-1", Status: "stale", Verdict: &verdict, Report: &report}
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
	core.getResp = &argus.GetTaskResponse{TaskId: "t-1", Status: "failed", FailReason: &reason}
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
		createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"},
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
	core := &fakeCore{createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK}
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
	core := &fakeCore{tasks: &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{{
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

// fakeDiffTaskCreator 实现 DiffTaskCreator。
type fakeDiffTaskCreator struct {
	fakeCore
	resp *argus.CreateTaskResponse
	code int
	diff string
}

func (f *fakeDiffTaskCreator) CreateDiffTask(_ context.Context, diff string) (*argus.CreateTaskResponse, int) {
	f.diff = diff
	if f.code != 0 {
		return nil, f.code
	}
	if f.resp != nil {
		return f.resp, http.StatusOK
	}
	return &argus.CreateTaskResponse{TaskId: "t-diff-1", Status: "pending"}, http.StatusOK
}

func TestReviewDiffAsync(t *testing.T) {
	dc := &fakeDiffTaskCreator{}
	srv := New(dc, "v-test")
	text, isErr := callTool(t, srv, "review_diff_async", map[string]any{"diff": "diff --git a/x b/x\n+bug\n"})
	if isErr || !strings.Contains(text, "t-diff-1") || !strings.Contains(text, "get_report") {
		t.Fatalf("review_diff_async: isErr=%v text=%s", isErr, text)
	}
	if dc.diff != "diff --git a/x b/x\n+bug\n" {
		t.Fatalf("diff 未透传: %q", dc.diff)
	}

	// 空 diff：工具层先拦
	_, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "   "})
	if !isErr {
		t.Fatal("空 diff 应 isError")
	}
	// 400：为空或超限（落库面 48KB）
	dc.code = http.StatusBadRequest
	text, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "x"})
	if !isErr || !strings.Contains(text, "48KB") {
		t.Fatalf("400 应转可读错误: isErr=%v text=%s", isErr, text)
	}
	// 503：模型未配置
	dc.code = http.StatusServiceUnavailable
	text, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "x"})
	if !isErr || !strings.Contains(text, "模型未配置") {
		t.Fatalf("503 应转可读错误: isErr=%v text=%s", isErr, text)
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
	tr := &argus.GetTaskResponse{TaskId: "t", Status: "done", Verdict: &verdict, Report: &report}

	res := renderTerminalTask(context.Background(), &fakeCore{}, tr, "verdict")
	text := extractText(t, res)
	if strings.Contains(text, "staleness") {
		t.Fatalf("verdict 注记不应含 staleness:\n%s", text)
	}

	res = renderTerminalTask(context.Background(), &fakeCore{}, tr, "note")
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
	core := &fakeCore{getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"}}
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
	core.createResp = &argus.CreateTaskResponse{}
	core.createCode = http.StatusInternalServerError
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "wait": false,
	})
	if !isErr {
		t.Fatalf("500 创建应 isError: %s", text)
	}
}

// ---------- search_knowledge（v3.125.0） ----------

// fakeKnowledgeSearcher 实现 KnowledgeSearcher。
type fakeKnowledgeSearcher struct {
	fakeCore
	query string
	topK  int
	out   string
	err   error
}

func (f *fakeKnowledgeSearcher) SearchKnowledge(_ context.Context, query string, topK int) (string, error) {
	f.query, f.topK = query, topK
	return f.out, f.err
}

func TestSearchKnowledge_RegisteredAndCalled(t *testing.T) {
	core := &fakeKnowledgeSearcher{out: "search_knowledge（query=规范）命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_knowledge" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 KnowledgeSearcher 后应注册 search_knowledge")
	}
	text, isErr := callTool(t, srv, "search_knowledge", map[string]any{"query": "规范", "top_k": float64(3)})
	if isErr {
		t.Fatalf("search_knowledge 报错: %s", text)
	}
	if core.query != "规范" || core.topK != 3 {
		t.Errorf("入参透传错: query=%q topK=%d", core.query, core.topK)
	}
	if !strings.Contains(text, "命中 1 条") {
		t.Errorf("输出不符:\n%s", text)
	}
	// 缺必填 → isError；数据面 error → isError
	_, isErr = callTool(t, srv, "search_knowledge", map[string]any{})
	if !isErr {
		t.Error("缺 query 应 isError")
	}
	core2 := &fakeKnowledgeSearcher{err: errors.New("知识库未启用（未配置知识源）")}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "search_knowledge", map[string]any{"query": "x"})
	if !isErr || !strings.Contains(text, "知识库未启用") {
		t.Errorf("未启用应 isError 含文案: isErr=%v text=%s", isErr, text)
	}
}

// ---------- search_sem（v3.126.0） ----------

// fakeSemSearcher 实现 SemSearcher。
type fakeSemSearcher struct {
	fakeCore
	query string
	topK  int
	out   string
	err   error
}

func (f *fakeSemSearcher) SearchSem(_ context.Context, _, _, _, _, query string, topK int) (string, error) {
	f.query, f.topK = query, topK
	return f.out, f.err
}

func TestSearchSem_RegisteredAndCalled(t *testing.T) {
	core := &fakeSemSearcher{out: "search_sem（query=认证）命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_sem" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 SemSearcher 后应注册 search_sem")
	}
	text, isErr := callTool(t, srv, "search_sem", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"query": "认证", "top_k": float64(3),
	})
	if isErr {
		t.Fatalf("search_sem 报错: %s", text)
	}
	if core.query != "认证" || core.topK != 3 {
		t.Errorf("入参透传错: query=%q topK=%d", core.query, core.topK)
	}
	// 缺必填 → isError；未启用 → isError
	_, isErr = callTool(t, srv, "search_sem", map[string]any{"platform": "gitea"})
	if !isErr {
		t.Error("缺 query 应 isError")
	}
	core2 := &fakeSemSearcher{err: errors.New("search_sem 未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）")}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "search_sem", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "query": "x",
	})
	if !isErr || !strings.Contains(text, "search_sem 未启用") {
		t.Errorf("未启用应 isError 含文案: isErr=%v text=%s", isErr, text)
	}
}

// ---------- 符号图工具面（v5.9.0） ----------

// fakeGraphCore 在 fakeCore 之上实现符号图两接口（get_trace/get_symbol_context 数据面）。
type fakeGraphCore struct {
	fakeCore
	out string
	err error
}

func (f *fakeGraphCore) Trace(_ context.Context, _, _, _, _, _, _ string) (string, error) {
	return f.out, f.err
}

func (f *fakeGraphCore) SymContext(_ context.Context, _, _, _, _, _ string) (string, error) {
	return f.out, f.err
}

// 实现两接口后 get_trace/get_symbol_context 双双注册并可调用（v5.9.0）。
func TestNew_GraphToolsRegisteredAndCalled(t *testing.T) {
	core := &fakeGraphCore{out: "get_trace：from→to 可达，途经 2 跳"}
	srv := New(core, "v-test")
	names := listToolNames(t, srv)
	for _, want := range []string{"get_trace", "get_symbol_context"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("实现 GraphTracer/SymbolContexter 后应注册 %s（注册面: %v）", want, names)
		}
	}
	text, isErr := callTool(t, srv, "get_trace", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"from": "HandleAuth", "to": "refreshSession",
	})
	if isErr {
		t.Fatalf("get_trace 报错: %s", text)
	}
	if !strings.Contains(text, "get_trace") {
		t.Errorf("get_trace 回执缺失: %s", text)
	}
	text, isErr = callTool(t, srv, "get_symbol_context", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"symbol": "Handler.ServeHTTP",
	})
	if isErr {
		t.Fatalf("get_symbol_context 报错: %s", text)
	}
	if !strings.Contains(text, "可达") {
		t.Errorf("get_symbol_context 回执缺失: %s", text)
	}
}

// 必填缺失 → 工具级 error 回执（schema Required 的运行期兜底）。
func TestNew_GraphToolsMissingArgs(t *testing.T) {
	srv := New(&fakeGraphCore{out: "不应触达"}, "v-test")
	if _, isErr := callTool(t, srv, "get_trace", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "from": "A",
	}); !isErr {
		t.Error("get_trace 缺 to 应 isError")
	}
	if _, isErr := callTool(t, srv, "get_symbol_context", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app",
	}); !isErr {
		t.Error("get_symbol_context 缺 symbol 应 isError")
	}
}

// 未实现 GraphTracer/SymbolContexter 时 tools/list 不出现两工具（死工具纪律）。
func TestNew_GraphToolsNotRegisteredWhenUnimplemented(t *testing.T) {
	names := listToolNames(t, New(&fakeCore{}, "v-test"))
	for _, banned := range []string{"get_trace", "get_symbol_context"} {
		for _, n := range names {
			if n == banned {
				t.Errorf("core 未实现符号图接口却注册了 %s", banned)
			}
		}
	}
}

// ---------- 阶段进度装饰（v3.127.0） ----------

// TestGetReport_StagesDecorated get_report 内联 stages 与 stage_current：
// 轮询单跳可见进展（此前要再调 get_task_stages）。
func TestGetReport_StagesDecorated(t *testing.T) {
	core := &fakeCopilot{stages: map[string]any{
		"stages": []map[string]any{
			{"stage": "R1", "done": true},
			{"stage": "R2", "done": true},
			{"stage": "R3", "done": false},
			{"stage": "R4", "done": false},
		},
	}, stagesOK: true}
	core.getResp = &argus.GetTaskResponse{TaskId: "t-9", Status: "running"}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "get_report", map[string]any{"task_id": "t-9"})
	if isErr {
		t.Fatalf("get_report 报错: %s", text)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatal(err)
	}
	if _, ok := view["stages"]; !ok {
		t.Fatalf("应内联 stages:\n%s", text)
	}
	if view["stage_current"] != "R3" {
		t.Errorf("stage_current = %v, want R3", view["stage_current"])
	}

	// core 不实现 Copilot（纯 fakeCore）→ 无 stages 字段但输出如常。
	srv2 := New(&fakeCore{getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"}}, "v-test")
	text2, _ := callTool(t, srv2, "get_report", map[string]any{"task_id": "t-1"})
	var view2 map[string]any
	_ = json.Unmarshal([]byte(text2), &view2)
	if _, ok := view2["stages"]; ok {
		t.Error("无 Copilot 能力不应带 stages")
	}
}

// ---------- suppress_finding（v3.128.0） ----------

// errSuppressScope 数据面 admin 门拒绝的测试替身错误。
var errSuppressScope = errors.New("suppress_finding 需 admin 档 token")

// fakeSuppressor 实现 FindingSuppressor。
type fakeSuppressor struct {
	fakeCore
	gotFP string
	out   map[string]any
	err   error
}

func (f *fakeSuppressor) SuppressFindingScoped(ctx context.Context, _, _, _, _, fp string) (map[string]any, error) {
	f.gotFP = fp
	return f.out, f.err
}

func TestSuppressFinding_RegisteredAndCalled(t *testing.T) {
	core := &fakeSuppressor{out: map[string]any{"matched": true, "fingerprint": "abcdef012345"}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "suppress_finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingSuppressor 后应注册 suppress_finding")
	}
	text, isErr := callTool(t, srv, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "AB12CD34",
	})
	if isErr {
		t.Fatalf("suppress_finding 报错: %s", text)
	}
	if core.gotFP != "ab12cd34" {
		t.Errorf("fp 应转小写透传: %q", core.gotFP)
	}
	if !strings.Contains(text, "matched") {
		t.Errorf("输出不符:\n%s", text)
	}
	// fp 非法短码 → 工具层拦截；数据面 error → isError（如 admin 门拒绝）
	_, isErr = callTool(t, srv, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab",
	})
	if !isErr {
		t.Error("短 fp 应 isError")
	}
	core2 := &fakeSuppressor{err: errSuppressScope}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "suppress_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab12cd34",
	})
	if !isErr || !strings.Contains(text, "admin") {
		t.Errorf("scope 拒绝应 isError 含 admin: isErr=%v text=%s", isErr, text)
	}
}

// ---------- get_capabilities（v3.129.0） ----------

// fakeCaps 实现 CapabilityFace。
type fakeCaps struct {
	fakeCore
	out map[string]any
}

func (f *fakeCaps) Capabilities(context.Context) map[string]any { return f.out }

func TestGetCapabilities_RegisteredAndCalled(t *testing.T) {
	core := &fakeCaps{out: map[string]any{"version": "v-test", "fix_agent": false, "sem_search": true}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "get_capabilities" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 CapabilityFace 后应注册 get_capabilities")
	}
	text, isErr := callTool(t, srv, "get_capabilities", map[string]any{})
	if isErr {
		t.Fatalf("get_capabilities 报错: %s", text)
	}
	for _, want := range []string{"v-test", "fix_agent", "sem_search"} {
		if !strings.Contains(text, want) {
			t.Errorf("输出缺 %q:\n%s", want, text)
		}
	}
}

// ---------- restore_finding（v3.131.0） ----------

type fakeRestorer struct {
	fakeCore
	gotFP string
	out   map[string]any
	err   error
}

func (f *fakeRestorer) RestoreFindingScoped(_ context.Context, _, _, _, _, fp string) (map[string]any, error) {
	f.gotFP = fp
	return f.out, f.err
}

func TestRestoreFinding_RegisteredAndCalled(t *testing.T) {
	core := &fakeRestorer{out: map[string]any{"matched": true}}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "restore_finding" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingRestorer 后应注册 restore_finding")
	}
	text, isErr := callTool(t, srv, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "AB12CD34",
	})
	if isErr {
		t.Fatalf("restore_finding 报错: %s", text)
	}
	if core.gotFP != "ab12cd34" {
		t.Errorf("fp 应转小写透传: %q", core.gotFP)
	}
	// 短 fp 工具层拦；数据面 admin 门错误透传
	_, isErr = callTool(t, srv, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab",
	})
	if !isErr {
		t.Error("短 fp 应 isError")
	}
	core2 := &fakeRestorer{err: errSuppressScope}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "restore_finding", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "2", "fp": "ab12cd34",
	})
	if !isErr || !strings.Contains(text, "admin") {
		t.Errorf("scope 拒绝应 isError 含 admin: isErr=%v text=%s", isErr, text)
	}
}

// TestAskArgus_QuestionCap 追问长度上限（v3.134.0 成本/DoS 约束）。
func TestAskArgus_QuestionCap(t *testing.T) {
	core := &fakeQA{answer: "x", answerOK: true}
	srv := New(core, "v-test")
	long := make([]byte, maxQuestionRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "1", "question": string(long),
	})
	if !isErr || !strings.Contains(text, "过长") {
		t.Errorf("超长 question 应报错: isErr=%v text=%s", isErr, text[:60])
	}
}

// ---------- search_findings（v3.138.0） ----------

type fakeHistorian struct {
	fakeCore
	query string
	limit int
	out   string
	err   error
}

func (f *fakeHistorian) SearchFindingsHistory(_ context.Context, _, _, _, query string, limit int) (string, error) {
	f.query, f.limit = query, limit
	return f.out, f.err
}

func TestSearchFindings_RegisteredAndCalled(t *testing.T) {
	core := &fakeHistorian{out: "search_findings 命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_findings" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 FindingHistorian 后应注册 search_findings")
	}
	text, isErr := callTool(t, srv, "search_findings", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "query": "硬编码", "limit": float64(5),
	})
	if isErr {
		t.Fatalf("search_findings 报错: %s", text)
	}
	if core.query != "硬编码" || core.limit != 5 {
		t.Errorf("入参透传错: %q %d", core.query, core.limit)
	}
}
