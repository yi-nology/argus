package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestAuditForwarder 外送器：audit.event 转发为 JSON POST（带 Bearer）、
// 非 audit 消息不转发、WithService 穿透保持外送。
func TestAuditForwarder(t *testing.T) {
	var mu sync.Mutex // server goroutine 写、测试主 goroutine 轮询读，必须互斥
	var got []map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
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

	snapshot := func() ([]map[string]any, string) {
		mu.Lock()
		defer mu.Unlock()
		return got, gotAuth
	}
	deadline := time.Now().Add(3 * time.Second)
	cur, _ := snapshot()
	for time.Now().Before(deadline) && len(cur) == 0 {
		time.Sleep(20 * time.Millisecond)
		cur, _ = snapshot()
	}
	cur, auth := snapshot()
	if len(cur) != 1 {
		t.Fatalf("应恰转发 1 条 audit.event，got %d", len(cur))
	}
	if cur[0]["action"] != "finding.suppress" || cur[0]["matched"] != true {
		t.Errorf("载荷字段不符: %v", cur[0])
	}
	if auth != "Bearer hook-tok" {
		t.Errorf("Bearer 头不符: %q", auth)
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
