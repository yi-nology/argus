package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
)

type nopLog struct{}

func (nopLog) Debug(string, ...any)  {}
func (nopLog) Info(string, ...any)   {}
func (nopLog) Warn(string, ...any)   {}
func (nopLog) Error(string, ...any)  {}
func (nopLog) Debugf(string, ...any) {}
func (nopLog) Infof(string, ...any)  {}
func (nopLog) Warnf(string, ...any)  {}
func (nopLog) Errorf(string, ...any) {}

func (nopLog) CtxDebug(context.Context, string, ...any)  {}
func (nopLog) CtxInfo(context.Context, string, ...any)   {}
func (nopLog) CtxWarn(context.Context, string, ...any)   {}
func (nopLog) CtxError(context.Context, string, ...any)  {}
func (nopLog) CtxDebugf(context.Context, string, ...any) {}
func (nopLog) CtxInfof(context.Context, string, ...any)  {}
func (nopLog) CtxWarnf(context.Context, string, ...any)  {}
func (nopLog) CtxErrorf(context.Context, string, ...any) {}

func (nopLog) WithCtx(context.Context) logx.Logger { return nopLog{} }
func (nopLog) WithService(string) logx.Logger      { return nopLog{} }
func (nopLog) WithAttrs(...logx.Attr) logx.Logger  { return nopLog{} }
func (nopLog) WithGroup(string) logx.Logger        { return nopLog{} }
func (nopLog) SetLevel(logx.Level)                 {}

var _ logx.Logger = nopLog{}

// TestNotifyTerminalSend 终态推送命中 octopus /api/v2/send/quick，带机器账号头。
func TestNotifyTerminalSend(t *testing.T) {
	var gotHeader, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-User-Id")
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
	}))
	defer ts.Close()

	n := &Notifier{BaseURL: ts.URL, ChannelID: 7, User: "argus-bot", Log: nopLog{}}
	n.NotifyTerminal(context.Background(), TaskEvent{
		TaskID: "t1", Owner: "acme", Repo: "app", Number: "5",
		Status: "done", Verdict: "request_changes", Rule: "R5.1",
		Findings: 2, Tokens: 1234, Elapsed: 90 * time.Second,
	})
	if gotHeader != "argus-bot" {
		t.Fatalf("X-User-Id 不符: %q", gotHeader)
	}
	if !contains(gotBody, `"channelId":7`) || !contains(gotBody, "request_changes") || !contains(gotBody, "acme/app#5") {
		t.Fatalf("推送内容不符: %s", gotBody)
	}
}

// TestNotifyDisabled 未配置 URL 时零调用不 panic。
func TestNotifyDisabled(t *testing.T) {
	var n *Notifier
	n.NotifyTerminal(context.Background(), TaskEvent{Status: "done"}) // nil 接收者安全
	n = &Notifier{Log: nopLog{}}
	n.NotifyTerminal(context.Background(), TaskEvent{Status: "done"}) // URL 空 no-op
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
