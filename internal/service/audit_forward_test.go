package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
)

// TestAuditForwarder 外送器：audit.event 转发为 JSON POST（带 Bearer）、
// 非 audit 消息不转发、WithService 穿透保持外送。
func TestAuditForwarder(t *testing.T) {
	var got []map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var ev map[string]any
		_ = json.NewDecoder(r.Body).Decode(&ev)
		got = append(got, ev)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := NewAuditForwarder(srv.URL, "hook-tok", nil)
	// WithService 穿透（audit.New 的真实调用形态）
	wrapped := f.Wrap(logx.NewStdLogger("audit-test")).WithService("argus-audit")
	wrapped.Info("argus.other.event", "k", "v") // 非 audit：不转发
	wrapped.Info("audit.event", "action", "finding.suppress", "matched", true)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(got) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("应恰转发 1 条 audit.event，got %d", len(got))
	}
	if got[0]["action"] != "finding.suppress" || got[0]["matched"] != true {
		t.Errorf("载荷字段不符: %v", got[0])
	}
	if gotAuth != "Bearer hook-tok" {
		t.Errorf("Bearer 头不符: %q", gotAuth)
	}
}

// TestAuditForwarderDropPolicy 外送端故障不阻断：队列满即丢弃不阻塞调用方。
func TestAuditForwarderDropPolicy(t *testing.T) {
	f := NewAuditForwarder("http://127.0.0.1:1/dead", "", nil) // 必然失败的端点
	done := make(chan struct{})
	go func() {
		for i := 0; i < auditForwardQueue+50; i++ {
			f.send("action", "x", "i", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("队列满时 send 应非阻塞丢弃，不得卡住调用方")
	}
}
