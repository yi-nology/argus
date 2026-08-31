package adapter_remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"argus/internal/plugin"
)

// TestRemoteMCP_EmptyPayload payload 缺字段 → 工具报错。
func TestRemoteMCP_EmptyPayload(t *testing.T) {
	ts := spawnMCPServer(t, true) // fail=true → 工具返回 error
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "review")
	// 无 Body 的任务 → payload 缺 owner → 工具应报错
	task := plugin.Task{PR: plugin.PRContext{}}
	if _, err := r.Invoke(context.Background(), task); err == nil {
		t.Fatal("缺字段应上抛错误")
	}
}

// TestRemoteMCP_Timeout 超短超时 → 连接报错。
func TestRemoteMCP_Timeout(t *testing.T) {
	ts := spawnMCPServer(t, false)
	r := newRemote(t, plugin.ProtocolMCP, ts.URL, "review")
	// 传入已取消的 ctx → Invoke 内部 WithTimeout 立即失败
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Invoke(ctx, remoteTask()); err == nil {
		t.Fatal("超时应上抛")
	}
}

// TestRemoteA2AInputRequired 远端任务需要输入 → 上抛（无法自动继续）。
func TestRemoteA2AInputRequired(t *testing.T) {
	ts := fakeA2A(t, "input-required", false)
	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("input-required 应上抛")
	}
}

// TestRemoteA2AUnknownState 未知状态 → 轮询直到 ctx 超时。
func TestRemoteA2AUnknownState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "x"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"id": "t1", "status": map[string]string{"state": "thinking"}},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	r.manifest.Timeout = 100 * time.Millisecond
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("未知状态应超时报错")
	}
}

// TestRemoteA2ACanceled 远端任务被取消 → 上抛。
func TestRemoteA2ACanceled(t *testing.T) {
	ts := fakeA2A(t, "canceled", false)
	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("canceled 终态应上抛")
	}
}

// TestRemoteUnknownProtocol 未知协议 → 上抛。
func TestRemoteUnknownProtocol(t *testing.T) {
	r := &Remote{
		manifest: plugin.AgentManifest{Name: "x", Protocol: "grpc", Timeout: time.Second},
		Endpoint: "http://x",
		Tool:     "review",
	}
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("未知协议应上抛")
	}
}

// TestRemoteA2AMalformedResponse 响应格式错误 → 上抛。
func TestRemoteA2AMalformedResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "x"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not json"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	r := newRemote(t, plugin.ProtocolA2A, ts.URL, "")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("格式错误应上抛")
	}
}
