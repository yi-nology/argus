package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"argus/internal/config"
	"argus/internal/storage"
)

type testLogger struct{}

func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

func bearerEngine(token string) *server.Hertz {
	svc := &Service{Cfg: &config.Config{APIToken: token}, Log: testLogger{}}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/agents", append([]app.HandlerFunc{svc.BearerMiddleware()}, func(ctx context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})...)
	return h
}

func doGet(h *server.Hertz, auth string) *ut.ResponseRecorder {
	headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	if auth != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: auth})
	}
	return ut.PerformRequest(h.Engine, http.MethodGet, "/agents", nil, headers...)
}

// v3.0.2 回归：平台 secret 未配置时 webhook fail-closed 503（SDK 空 secret 会跳过
// 签名校验，此前请求期照常放行等于无鉴权）。
func TestWebhookNoSecretFailsClosed(t *testing.T) {
	svc := &Service{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"gitea": {Token: "tk", Secret: ""}, // 有 token 无 secret：升级后必须拒绝
	}}, Log: testLogger{}}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})
	resp := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("空 secret 应 503 fail-closed，got %d", resp.Code)
	}
	// 对照：配置了 secret 的平台放行进入后续流程（签名会失败→401，而不是 503）
	svc.Cfg.Platforms["gitea"] = config.PlatformConfig{Token: "tk", Secret: "s"}
	h2 := server.New(server.WithHostPorts("127.0.0.1:0"))
	h2.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})
	resp2 := ut.PerformRequest(h2.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"})
	if resp2.Code == http.StatusServiceUnavailable {
		t.Fatal("配了 secret 不应 503（应走到签名校验 400/401）")
	}
}

// 已配置 token：401 / 401 / 200。
func TestBearerWithToken(t *testing.T) {
	h := bearerEngine("secret-1")
	if code := doGet(h, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("无 header 应 401，got %d", code)
	}
	if code := doGet(h, "Bearer wrong").Code; code != http.StatusUnauthorized {
		t.Fatalf("错 token 应 401，got %d", code)
	}
	if code := doGet(h, "Bearer secret-1").Code; code != http.StatusOK {
		t.Fatalf("对 token 应 200，got %d", code)
	}
}

// v3.0.1 修复回归：token 未配置时 🔒 路由 fail-closed（503），不再放行。
func TestBearerNoTokenFailsClosed(t *testing.T) {
	h := bearerEngine("")
	resp := doGet(h, "")
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 token 应 503 拒绝服务（原实现放行是鉴权裸奔），got %d", resp.Code)
	}
	if resp.Code == http.StatusOK {
		t.Fatal("fail-open 回归")
	}
}

// Content-Length 预检：超大 body 返回 413（在 body 读取前拒绝）。
func TestWebhook_BodyTooLarge(t *testing.T) {
	svc := &Service{
		Cfg: &config.Config{
			WebhookMaxBody: 1024, // 1KB 限制
			Platforms: map[string]config.PlatformConfig{
				"gitea": {Token: "tk", Secret: "s"},
			},
		},
		Log: testLogger{},
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})

	// Content-Length 超限 → 413（body 读取前即拒绝，不依赖 SDK）
	resp := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Content-Length", Value: "9999"},
	)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应 413，got %d", resp.Code)
	}

	// WebhookMaxBody=0 时跳过预检（不返回 413）
	svc.Cfg.WebhookMaxBody = 0
	resp2 := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Content-Length", Value: "9999"},
	)
	if resp2.Code == http.StatusRequestEntityTooLarge {
		t.Fatal("WebhookMaxBody=0 时不应 413")
	}
}

func TestExtractSuppressFP(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"suppress fp:abcd1234", "abcd1234", true},
		{"suppress fp:ABCD1234 更正", "abcd1234", true},
		{"suppress fp:abc", "", false},     // 短码过短
		{"suppress fp:xyz123!", "", false}, // 非十六进制
		{"ask 这段代码为什么这样写", "", false}, // 非 suppress 指令
	}
	for _, c := range cases {
		got, ok := extractSuppressFP(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("extractSuppressFP(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestExtractRestoreFP(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"restore fp:abcd1234", "abcd1234", true},
		{"restore fp:ABCD1234 更正", "abcd1234", true},
		{"restore fp:abc", "", false},     // 短码过短
		{"restore fp:xyz123!", "", false}, // 非十六进制
		{"suppress fp:abcd1234", "", false}, // 反向指令不得命中 restore
		{"ask 这段代码为什么这样写", "", false},     // 非 restore 指令
	}
	for _, c := range cases {
		got, ok := extractRestoreFP(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("extractRestoreFP(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRenderHistoryCapsAndEscapes(t *testing.T) {
	var long strings.Builder
	for i := 0; i < 500; i++ {
		long.WriteRune('长')
	}
	hist := []storage.QATurn{
		{Question: "q1", Answer: long.String(), At: "2026-09-07T00:00:00Z"},
	}
	out := renderHistory(hist)
	// 单条回答截断到 400 rune + 省略号：不应把 500 字全文带回上下文
	if strings.Count(out, "长") > 401 {
		t.Fatalf("历史应截断: %d", strings.Count(out, "长"))
	}
	if !strings.Contains(out, "q1") {
		t.Fatalf("问题应保留: %q", out)
	}
}
