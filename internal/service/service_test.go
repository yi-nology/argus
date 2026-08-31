package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"argus/internal/config"
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
