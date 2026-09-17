package sink

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"argus/internal/plugin"
)

func sampleFindings() []plugin.Finding {
	return []plugin.Finding{
		{FP: "fp-1", File: "a.go", Line: 3, Severity: "high", Comment: "竞态", SuggestedFix: "加锁", By: "ocr"},
		{FP: "fp-2", File: "b.go", Line: 9, Severity: "low", Comment: "命名"},
	}
}

// 契约映射：字段逐一对齐 review-service ingest 契约（argus_task_id/业务键/
// verdict/findings 含 fp/severity/plugins/统计）。
func TestBuildPayloadContract(t *testing.T) {
	pr := &plugin.PRContext{
		Platform: "gitea", Owner: "demo-user", Repo: "demo", Number: "7",
		Title: "fix login", HeadSHA: "abc123", BaseBranch: "main",
	}
	now := time.Now()
	p := BuildPayload("task-1", pr, sampleFindings(), "comment",
		[]string{"correctness", "ocr"}, 1500, 42, now)
	if p.ArgusTaskID != "task-1" || p.Platform != "gitea" || p.Owner != "demo-user" ||
		p.Repo != "demo" || p.Number != "7" || p.HeadSHA != "abc123" || p.BaseBranch != "main" {
		t.Fatalf("业务键映射不符: %+v", p)
	}
	if p.Verdict != "comment" || len(p.Plugins) != 2 || p.DurationMS != 1500 || p.TokenUsage != 42 {
		t.Fatalf("统计映射不符: %+v", p)
	}
	if len(p.Findings) != 2 || p.Findings[0].FP != "fp-1" || p.Findings[0].Severity != "high" ||
		p.Findings[0].SuggestedFix != "加锁" {
		t.Fatalf("findings 映射不符: %+v", p.Findings)
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("payload 须可序列化: %v", err)
	}
}

// 意见超上限截断（保序）。
func TestBuildPayloadTruncates(t *testing.T) {
	fs := make([]plugin.Finding, MaxFindings+10)
	for i := range fs {
		fs[i] = plugin.Finding{FP: "x", File: "f.go", Severity: "low", Comment: "c"}
	}
	p := BuildPayload("t", &plugin.PRContext{}, fs, "comment", nil, 0, 0, time.Now())
	if len(p.Findings) != MaxFindings {
		t.Fatalf("应截断到 %d，got %d", MaxFindings, len(p.Findings))
	}
}

// 上报端到端：POST 契约 JSON + Bearer 头；对端 500 时重试后放弃且不 panic。
func TestReportPostsAndRetries(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	r := &Reporter{URL: srv.URL + "/api/v1/argus/reviews", Token: "tok-1"}
	pr := &plugin.PRContext{Platform: "gitea", Owner: "z", Repo: "d", Number: "1", HeadSHA: "h"}
	p := BuildPayload("task-9", pr, sampleFindings(), "approve", []string{"ocr"}, 100, 1, time.Now())
	r.reportWithRetry(p) // 直调内部同步版本，绕过 goroutine

	if calls.Load() != 2 {
		t.Fatalf("500 后应重试一次，got %d 次", calls.Load())
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("应带 Bearer 凭证: %q", gotAuth)
	}
	var back Payload
	if err := json.Unmarshal([]byte(gotBody), &back); err != nil {
		t.Fatalf("对端应收到合法契约 JSON: %v", err)
	}
	if back.ArgusTaskID != "task-9" || len(back.Findings) != 2 {
		t.Fatalf("对端收到的契约不符: %+v", back)
	}
}

// 未配置（nil / 空 URL）时静默跳过。
func TestReportDisabled(t *testing.T) {
	var nilRep *Reporter
	if nilRep.Enabled() {
		t.Fatal("nil reporter 应为关闭")
	}
	(&Reporter{}).Report(nil) // 空 URL：不发请求、不 panic
}
