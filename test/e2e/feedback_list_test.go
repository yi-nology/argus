package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// v3.0.1 打磨回归：误报反馈闭环（§3.3 反馈通道最小入口）。
// 流程：一轮审查产生 findings → suppress 指纹 → 再审 → 报告剔除该条 + 抑制计数提示。
func TestScenario13_SuppressFinding(t *testing.T) {
	s := newStack(t, stackOpts{
		correctness: `{"findings":[{"file":"a.go","line":7,"severity":"medium","comment":"存在空指针风险"}],"notes":""}`,
	})
	suppressViaAPI := func() (int, bool) {
		body := `{"platform":"gitea","owner":"acme","repo":"app","number":"7","file":"a.go","comment":"存在空指针风险"}`
		rec := performRequest(s.engine, "POST", "/findings/suppress", body, []utHeader{
			{Key: "Content-Type", Value: "application/json"},
			{Key: "Authorization", Value: "Bearer e2e-token"},
		})
		var env struct {
			Code int `json:"code"`
			Data struct {
				Matched     bool   `json:"matched"`
				Fingerprint string `json:"fingerprint"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return rec.Code, env.Data.Matched
	}

	// ① 第一轮：报告中含该意见
	t1 := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), t1); err != nil {
		t.Fatal(err)
	}
	task1 := s.waitForStatus(t, t1, "done")
	if !strings.Contains(task1.Report, "空指针风险") {
		t.Fatalf("第一轮报告应含该意见:\n%s", task1.Report)
	}

	// ② 抑制（指纹已在库 → matched=true）
	code, matched := suppressViaAPI()
	if code != 200 || !matched {
		t.Fatalf("抑制应命中既有指纹: code=%d matched=%v", code, matched)
	}

	// ③ 第二轮：报告剔除该条 + 出现抑制提示
	t2 := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), t2); err != nil {
		t.Fatal(err)
	}
	task2 := s.waitForStatus(t, t2, "done")
	if strings.Contains(task2.Report, "空指针风险") {
		t.Fatalf("抑制后报告不应再含该意见:\n%s", task2.Report)
	}
	if !strings.Contains(task2.Report, "已被标记为误报") {
		t.Fatalf("报告应提示抑制计数:\n%s", task2.Report)
	}
}

// v3.0.1 打磨回归：GET /tasks 任务列表（status 过滤 + limit）。
func TestScenario14_ListTasks(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0})
	tDone := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), tDone); err != nil {
		t.Fatal(err)
	}
	s.waitForStatus(t, tDone, "done")
	tPending := s.createTaskViaAPI(t, 0) // 保持 pending

	list := func(query string) []map[string]any {
		rec := performRequest(s.engine, "GET", "/tasks"+query, "", []utHeader{
			{Key: "Authorization", Value: "Bearer e2e-token"},
		})
		if rec.Code != 200 {
			t.Fatalf("HTTP %d", rec.Code)
		}
		var env struct {
			Data struct {
				Tasks []map[string]any `json:"tasks"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Data.Tasks
	}

	// status 过滤
	if n := len(list("?status=done")); n < 1 {
		t.Fatalf("done 列表至少 1 条，got %d", n)
	}
	for _, it := range list("?status=pending") {
		if it["status"] != "pending" {
			t.Fatalf("过滤失效: %+v", it)
		}
	}
	// limit 收口
	if n := len(list("?limit=1")); n > 1 {
		t.Fatalf("limit=1 应最多 1 条，got %d", n)
	}
	// 非法 status → 400
	if rec := performRequest(s.engine, "GET", "/tasks?status=bogus", "", []utHeader{
		{Key: "Authorization", Value: "Bearer e2e-token"},
	}); rec.Code != 400 {
		t.Fatalf("非法 status 应 400，got %d", rec.Code)
	}
	_ = tPending
	_ = fmt.Sprintf
}

// v3.0.1 打磨回归：报告意见行带指纹短码 + suppress 按 fp 直达 + GetTask debug 字段。
func TestScenario15_FingerprintSuppressAndDebug(t *testing.T) {
	s := newStack(t, stackOpts{
		debug:       true,
		correctness: `{"findings":[{"file":"a.go","line":7,"severity":"medium","comment":"存在空指针风险"}],"notes":""}`,
	})
	// 第一轮：报告中出现 fp 短码
	t1 := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), t1); err != nil {
		t.Fatal(err)
	}
	task1 := s.waitForStatus(t, t1, "done")
	i := strings.Index(task1.Report, "（fp:")
	if i < 0 {
		t.Fatalf("报告意见行应带指纹短码:\n%s", task1.Report)
	}
	fpShort := task1.Report[i+len("（fp:") : i+len("（fp:")+8]

	// 按 fp 直达抑制
	body := fmt.Sprintf(`{"platform":"gitea","owner":"acme","repo":"app","number":"7","fingerprint":"%s"}`, fpShort)
	rec := performRequest(s.engine, "POST", "/findings/suppress", body, []utHeader{
		{Key: "Content-Type", Value: "application/json"},
		{Key: "Authorization", Value: "Bearer e2e-token"},
	})
	var env struct {
		Data struct {
			Matched bool `json:"matched"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if !env.Data.Matched {
		t.Fatalf("fp 直达抑制应命中: %s", rec.Body.String())
	}

	// GetTask 返回 debug 字段（ARGUS_DEBUG=1）
	rec = performRequest(s.engine, "GET", "/tasks/"+t1, "", []utHeader{
		{Key: "Authorization", Value: "Bearer e2e-token"},
	})
	var resp struct {
		Data struct {
			Debug string `json:"debug"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Debug == "" || !strings.Contains(resp.Data.Debug, "r1_spec") {
		t.Fatalf("GetTask 应返回阶段调试产物: %q", resp.Data.Debug)
	}
}
