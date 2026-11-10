package adapter_remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

// fakeA2A 模拟 A2A server：Agent Card 发现 + message/send 同步完成 + artifacts。
func fakeA2A(t *testing.T, state string, pollMode bool) *httptest.Server {
	t.Helper()
	var taskState atomic.Value
	taskState.Store(state)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "visual-agent", "url": "", "capabilities": map[string]bool{"streaming": false},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch body.Method {
		case "message/send":
			if pollMode {
				taskState.Store("working")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"id": "task-1", "status": map[string]string{"state": taskState.Load().(string)}},
			})
		case "tasks/get":
			state := taskState.Load().(string)
			if state == "working" {
				taskState.Store("completed") // 第二次轮询完成
			}
			result := map[string]any{
				"id":     "task-1",
				"status": map[string]string{"state": taskState.Load().(string)},
			}
			if taskState.Load().(string) == "completed" {
				result["artifacts"] = []map[string]any{{
					"parts": []map[string]string{{"kind": "text", "text": `{"findings":[],"notes":"visual ok"}`}},
				}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": result})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestRemoteA2ASyncComplete message/send 同步 completed（无需轮询）。
func TestRemoteA2ASyncComplete(t *testing.T) {
	r := newRemote(t, plugin.ProtocolA2A, fakeA2A(t, "completed", false).URL, "")
	res, err := r.Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != "visual ok" || len(res.Findings) != 0 {
		t.Fatalf("result 不符: %+v", res)
	}
}

// TestRemoteA2APollUntilComplete working → 轮询 → completed。
func TestRemoteA2APollUntilComplete(t *testing.T) {
	r := newRemote(t, plugin.ProtocolA2A, fakeA2A(t, "working", true).URL, "")
	res, err := r.Invoke(context.Background(), remoteTask())
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != "visual ok" {
		t.Fatalf("result 不符: %+v", res)
	}
}

// TestRemoteA2AFailed 远端任务 failed → 上抛。
func TestRemoteA2AFailed(t *testing.T) {
	r := newRemote(t, plugin.ProtocolA2A, fakeA2A(t, "failed", false).URL, "")
	if _, err := r.Invoke(context.Background(), remoteTask()); err == nil {
		t.Fatal("failed 终态应上抛")
	}
}
