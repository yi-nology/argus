package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"argus/internal/config"
)

// rbacEngine 挂 BearerMiddleware 的最小测试引擎（回显放行）。
func rbacEngine(svc *Service, method, path string) *server.Hertz {
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	handler := append([]app.HandlerFunc{svc.BearerMiddleware()}, func(ctx context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})
	switch method {
	case http.MethodPost:
		h.POST(path, handler...)
	case http.MethodDelete:
		h.DELETE(path, handler...)
	default:
		h.GET(path, handler...)
	}
	return h
}

func doRBAC(h *server.Hertz, method, path, auth string) int {
	headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	if auth != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: auth})
	}
	var m string
	switch method {
	case http.MethodPost:
		m = "POST"
	case http.MethodDelete:
		m = "DELETE"
	default:
		m = "GET"
	}
	return ut.PerformRequest(h.Engine, m, path, nil, headers...).Code
}

// v3.87.0：scope 分级矩阵——admin ⊇ ci ⊇ read；POST /findings/* 需 admin，
// 其余 POST/DELETE 需 ci，读面 read；全空 fail-closed 503（语义不变）。
func TestBearerScopeMatrix(t *testing.T) {
	svc := &Service{
		Cfg: &config.Config{
			APIToken:  "legacy-admin",
			APITokens: config.ParseAPITokens("tok-read:read,tok-ci:ci,tok-admin:admin"),
		},
		Log: testLogger{},
	}
	cases := []struct {
		token, method, path string
		want                int
	}{
		// 读面：read 及以上全放行
		{"tok-read", "GET", "/tasks", 200},
		{"tok-ci", "GET", "/tasks", 200},
		{"tok-admin", "GET", "/tasks", 200},
		{"legacy-admin", "GET", "/tasks", 200},
		// 建任务：ci 及以上
		{"tok-read", "POST", "/tasks", 403},
		{"tok-ci", "POST", "/tasks", 200},
		{"legacy-admin", "POST", "/tasks", 200},
		// 误报抑制：仅 admin
		{"tok-ci", "POST", "/findings/suppress", 403},
		{"tok-admin", "POST", "/findings/suppress", 200},
		{"legacy-admin", "POST", "/findings/suppress", 200},
		// MCP 会话清理：ci 及以上
		{"tok-read", "DELETE", "/mcp", 403},
		{"tok-ci", "DELETE", "/mcp", 200},
		// 未知 token
		{"tok-nope", "GET", "/tasks", 401},
	}
	for _, tc := range cases {
		h := rbacEngine(svc, tc.method, tc.path)
		if got := doRBAC(h, tc.method, tc.path, "Bearer "+tc.token); got != tc.want {
			t.Errorf("%s %s with %s = %d, want %d", tc.method, tc.path, tc.token, got, tc.want)
		}
	}

	// 全空 = 503 fail-closed（v3.0.1 语义不变）
	empty := &Service{Cfg: &config.Config{}, Log: testLogger{}}
	h := rbacEngine(empty, "GET", "/tasks")
	if got := doRBAC(h, "GET", "/tasks", ""); got != http.StatusServiceUnavailable {
		t.Fatalf("token 全空应 503，got %d", got)
	}
}

func TestRequiredScope(t *testing.T) {
	if got := requiredScope("POST", "/findings/suppress"); got != "admin" {
		t.Fatalf("suppress 应 admin，got %s", got)
	}
	if got := requiredScope("POST", "/tasks"); got != "ci" {
		t.Fatalf("建任务应 ci，got %s", got)
	}
	if got := requiredScope("GET", "/stats/overview"); got != "read" {
		t.Fatalf("读面应 read，got %s", got)
	}
}

func TestParseAPITokens(t *testing.T) {
	toks := config.ParseAPITokens("a:admin, b:CI ,c:read,bad,,x:nonsense")
	if len(toks) != 3 {
		t.Fatalf("应只收 3 条合法条目: %+v", toks)
	}
	if toks[0].Token != "a" || toks[0].Scope != "admin" || toks[1].Scope != "ci" || toks[2].Scope != "read" {
		t.Fatalf("解析不符: %+v", toks)
	}
	if got := config.ParseAPITokens(""); got != nil {
		t.Fatalf("空串应 nil: %+v", got)
	}
	// service 侧 rank 表与 config 侧闭集一致（漂移会让 403 判断失真）
	for sc := range apiScopeSet() {
		if scopeRank[sc] == 0 {
			t.Fatalf("scopeRank 缺 %s", sc)
		}
	}
}

// apiScopeSet 测试辅助：与 config 侧同一闭集字面量。
func apiScopeSet() map[string]struct{} {
	return map[string]struct{}{"read": {}, "ci": {}, "admin": {}}
}

// v3.128.0：scope 透传——放行的请求 ctx 里必须带授予档位（工具级二次判定的输入）。
func TestBearer_ScopePropagatedToContext(t *testing.T) {
	svc := &Service{Cfg: &config.Config{
		APIToken:  "adm",
		APITokens: []config.APITokenScope{{Token: "ci1", Scope: "ci"}, {Token: "rd1", Scope: "read"}},
	}}
	got := ""
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/x", svc.BearerMiddleware(), func(ctx context.Context, c *app.RequestContext) {
		got = ScopeFromContext(ctx)
		c.String(http.StatusOK, "ok")
	})
	cases := []struct{ auth, want string }{
		{"Bearer adm", "admin"}, {"Bearer ci1", "ci"}, {"Bearer rd1", "read"},
	}
	for _, tc := range cases {
		got = "SENTINEL"
		code := doRBAC(h, http.MethodGet, "/x", tc.auth)
		if code != http.StatusOK || got != tc.want {
			t.Errorf("auth=%s: code=%d scope=%q want %q", tc.auth, code, got, tc.want)
		}
	}
}

// TestBearer_TokenExpired 过期 token 即拒（v3.136.0 轮换语义）。
func TestBearer_TokenExpired(t *testing.T) {
	svc := &Service{Cfg: &config.Config{
		APITokens: []config.APITokenScope{
			{Token: "live", Scope: "ci"},
			{Token: "dead", Scope: "ci", Expires: time.Now().Add(-time.Hour)},
		},
	}}
	got := ""
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/x", svc.BearerMiddleware(), func(ctx context.Context, c *app.RequestContext) {
		got = ScopeFromContext(ctx)
		c.String(http.StatusOK, "ok")
	})
	if code := doRBAC(h, http.MethodGet, "/x", "Bearer live"); code != http.StatusOK || got != "ci" {
		t.Errorf("未过期 token 应放行: code=%d scope=%q", code, got)
	}
	if code := doRBAC(h, http.MethodGet, "/x", "Bearer dead"); code != http.StatusUnauthorized {
		t.Errorf("过期 token 应 401: %d", code)
	}
}

// TestBearer_TokenFPPropagated token 指纹取证面（v3.137.0）：ctx 携带非原 token
// 的指纹，审计可溯源"哪条 token 干了什么"。
func TestBearer_TokenFPPropagated(t *testing.T) {
	svc := &Service{Cfg: &config.Config{APITokens: []config.APITokenScope{{Token: "tok-x", Scope: "ci"}}}}
	var fp string
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/x", svc.BearerMiddleware(), func(ctx context.Context, c *app.RequestContext) {
		fp = TokenFPFromContext(ctx)
		c.String(http.StatusOK, "ok")
	})
	if code := doRBAC(h, http.MethodGet, "/x", "Bearer tok-x"); code != http.StatusOK {
		t.Fatalf("应放行: %d", code)
	}
	if fp == "" || fp == "tok-x" || len(fp) != 8 {
		t.Errorf("token 指纹应为 8 位 hex 且非原 token: %q", fp)
	}
}
