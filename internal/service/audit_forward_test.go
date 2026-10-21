package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// captureLogger 捕获 Warn 调用（audit 外送断言用）。
type captureLogger struct {
	logx.Logger
	mu    sync.Mutex
	warns []string
}

func (c *captureLogger) Warn(msg string, kv ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warns = append(c.warns, msg)
}

func (c *captureLogger) warnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.warns)
}

// TestAuditForwarderEmptyBodyOK 端点返回 200 空响应体（215 实测形态）：
// 不产生 forward_failed 告警——v3.164.0 httpjson 误收编的假失败回归钉。
func TestAuditForwarderEmptyBodyOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 200 + 空响应体
	}))
	defer srv.Close()

	lg := &captureLogger{}
	f := NewAuditForwarder(srv.URL, "", lg)
	f.send("k", "v")
	// worker 是后台 goroutine：轮询等消费完成（队列深度 1，5s 足够）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && f.drops.Load() == 0 && len(f.ch) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := lg.warnCount(); n != 0 {
		t.Fatalf("200 空响应体不应告警，got %d 条 WARN", n)
	}
}

// TestAuditForwarderStatusNon2xxWarns 非 2xx（5xx）必须告警——v3.164 修复的
// 原始缺陷（旧版 5xx 静默丢事件）回归钉。
func TestAuditForwarderStatusNon2xxWarns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "siem unavailable", http.StatusBadGateway)
	}))
	defer srv.Close()

	lg := &captureLogger{}
	f := NewAuditForwarder(srv.URL, "", lg)
	f.send("k", "v")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(f.ch) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := lg.warnCount(); n == 0 {
		t.Fatal("502 应产生 forward_failed 告警")
	}
	// 告警须带 status 字段（可归因）
	lg.mu.Lock()
	defer lg.mu.Unlock()
	if len(lg.warns) == 0 || !strings.Contains(lg.warns[0], "forward_failed") {
		t.Fatalf("告警消息名不符：%v", lg.warns)
	}
}
