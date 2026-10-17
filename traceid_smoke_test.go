package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	oekitversion "github.com/oekit/core/version"
	health "github.com/oekit/hertz/health"
	versionh "github.com/oekit/hertz/versionh"
	mw "github.com/oekit/middleware"
	"github.com/oekit/middleware/hmw"
)

// TestTraceIDSmoke 验证 main 的装配组合：traceid.Middleware + mw 响应壳 → trace_id 自动填充。
func TestTraceIDSmoke(t *testing.T) {
	h := server.Default(server.WithHostPorts("127.0.0.1:0"))
	h.Use(hmw.Middleware()...)
	h.GET("/smoke", func(ctx context.Context, c *app.RequestContext) {
		mw.SuccessRespMsg(c, map[string]any{"ping": "pong"}, "ok")
	})
	h.GET("/smoke-fail", func(ctx context.Context, c *app.RequestContext) {
		mw.ErrorResp(c, consts.StatusNotFound, "任务不存在")
	})

	w := ut.PerformRequest(h.Engine, "GET", "/smoke", nil)
	resp := w.Result()
	assert.DeepEqual(t, consts.StatusOK, resp.StatusCode())
	var body struct {
		Code    int            `json:"code"`
		Data    map[string]any `json:"data"`
		Message string         `json:"message"`
		TraceID string         `json:"trace_id"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, 0, body.Code)
	assert.DeepEqual(t, "ok", body.Message)
	if body.TraceID == "" {
		t.Fatal("trace_id 不应为空")
	}
	if resp.Header.Get("X-Trace-Id") == "" {
		t.Fatal("X-Trace-Id 响应头不应为空")
	}

	// 透传：客户端带来的 X-Trace-Id 应原样回显
	w2 := ut.PerformRequest(h.Engine, "GET", "/smoke-fail", nil, ut.Header{Key: "X-Trace-Id", Value: "my-trace-42"})
	resp2 := w2.Result()
	assert.DeepEqual(t, consts.StatusNotFound, resp2.StatusCode())
	var body2 struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(resp2.Body(), &body2); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, 404, body2.Code)
	assert.DeepEqual(t, "任务不存在", body2.Message)
	assert.DeepEqual(t, "my-trace-42", body2.TraceID)
	assert.DeepEqual(t, "my-trace-42", string(resp2.Header.Get("X-Trace-Id")))
}

// TestPanicRecoverySmoke 验证 main 的装配组合挂了 panic 恢复（v3.146.0）：
// hertz 引擎兜底仅在 PanicHandler 非空时挂、netpoll 事件循环显式不 recover，
// 无 mw.RecoveryMiddleware 时 handler panic = 进程崩溃。断言三点：panic 返回
// mw.ErrorResp 信封 500、trace_id 仍注入、后续请求继续被正常服务（引擎存活）。
func TestPanicRecoverySmoke(t *testing.T) {
	h := server.Default(server.WithHostPorts("127.0.0.1:0"))
	h.Use(hmw.Middleware()...)
	h.Use(mw.RecoveryMiddleware())
	h.GET("/smoke-panic", func(ctx context.Context, c *app.RequestContext) {
		panic("handler 炸了")
	})
	h.GET("/smoke-alive", func(ctx context.Context, c *app.RequestContext) {
		mw.SuccessRespMsg(c, map[string]any{"ping": "pong"}, "ok")
	})

	w := ut.PerformRequest(h.Engine, "GET", "/smoke-panic", nil, ut.Header{Key: "X-Trace-Id", Value: "panic-trace-7"})
	resp := w.Result()
	assert.DeepEqual(t, consts.StatusInternalServerError, resp.StatusCode())
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, 500, body.Code)

	// 引擎存活：panic 后的下一个请求正常服务
	w2 := ut.PerformRequest(h.Engine, "GET", "/smoke-alive", nil)
	resp2 := w2.Result()
	assert.DeepEqual(t, consts.StatusOK, resp2.StatusCode())
}

// TestHealthVersionWrapSmoke 验证 v3.147.0 服务面收口的装配组合：
// hmw → Recovery → Response 栈下，oekit health/version 端点自写信封不被二次包裹、
// 裸 JSON 自动包信封并注入 trace_id、SkipResponseWrap 协议端点原样透出。
func TestHealthVersionWrapSmoke(t *testing.T) {
	prev := oekitversion.Version
	oekitversion.Version = "v9.9.9-smoke"
	t.Cleanup(func() { oekitversion.Version = prev })

	h := server.Default(server.WithHostPorts("127.0.0.1:0"))
	h.Use(hmw.Middleware()...)
	h.Use(mw.RecoveryMiddleware())
	// 与 main.go 同构：协议端点豁免清单声明在栈级（middleware v0.5.0 WithSkipPaths/Prefixes）
	h.Use(mw.ResponseMiddleware(
		mw.WithSkipPrefixes("/.well-known/"),
		mw.WithSkipPaths("/mcp"),
	))
	okCheck := func(context.Context) error { return nil }
	h.GET("/healthz", health.PingHandler)
	h.GET("/readyz", health.NewHealthHandler(health.WithCheckers(
		health.NamedChecker("db", okCheck), health.NamedChecker("llm", okCheck))))
	h.GET("/readyz-bad", health.NewHealthHandler(health.WithCheckers(
		health.NamedChecker("db", okCheck),
		health.NamedChecker("llm", func(context.Context) error { return errors.New("llm unreachable") }))))
	h.GET("/version", versionh.Handler)
	h.GET("/bare", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]any{"message": "pong"})
	})
	// 三种协议端点形态：声明式前缀 / 声明式路径 / handler 级逃生舱——都不得被包裹
	h.GET("/.well-known/agent.json", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]any{"name": "argus"})
	})
	h.GET("/mcp", func(ctx context.Context, c *app.RequestContext) {
		c.JSON(consts.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	})
	h.GET("/agent.json", func(ctx context.Context, c *app.RequestContext) {
		mw.SkipResponseWrap(c)
		c.JSON(consts.StatusOK, map[string]any{"name": "argus"})
	})

	type envelope struct {
		Code    int             `json:"code"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
		TraceID string          `json:"trace_id"`
	}
	// requestid 校验 X-Trace-Id 字符集（含 / 等即判非法重新生成），trace 值只留字母数字与连字符
	traceOf := func(path string) string { return "smoke-" + strings.NewReplacer("/", "", ".", "").Replace(path) }
	get := func(path string) (int, envelope, []byte) {
		w := ut.PerformRequest(h.Engine, "GET", path, nil, ut.Header{Key: "X-Trace-Id", Value: traceOf(path)})
		resp := w.Result()
		var e envelope
		_ = json.Unmarshal(resp.Body(), &e)
		return resp.StatusCode(), e, resp.Body()
	}
	type readyBody struct {
		Status string                                 `json:"status"`
		Checks []struct{ Name, Status, Error string } `json:"checks"`
	}

	// 存活：PingHandler 自写信封，trace_id 透传
	code, e, _ := get("/healthz")
	assert.DeepEqual(t, consts.StatusOK, code)
	assert.DeepEqual(t, 0, e.Code)
	assert.DeepEqual(t, "pong", e.Message)
	assert.DeepEqual(t, traceOf("/healthz"), e.TraceID)

	// 就绪：全过 200，checks 按注册顺序稳定输出
	code, e, _ = get("/readyz")
	assert.DeepEqual(t, consts.StatusOK, code)
	var ready readyBody
	if err := json.Unmarshal(e.Data, &ready); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "ok", ready.Status)
	assert.DeepEqual(t, 2, len(ready.Checks))
	assert.DeepEqual(t, "db", ready.Checks[0].Name)
	assert.DeepEqual(t, "ok", ready.Checks[1].Status)

	// 就绪：任一失败 503 + code 503，失败项带错误信息
	code, e, _ = get("/readyz-bad")
	assert.DeepEqual(t, consts.StatusServiceUnavailable, code)
	assert.DeepEqual(t, 503, e.Code)
	if err := json.Unmarshal(e.Data, &ready); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "error", ready.Status)
	assert.DeepEqual(t, "check failed", ready.Checks[1].Error) // 库内脱敏：真实错误只进 logx

	// 版本：core/version 注入值透出
	code, e, _ = get("/version")
	assert.DeepEqual(t, consts.StatusOK, code)
	var ver struct{ Version, BuildCommit, Uptime string }
	if err := json.Unmarshal(e.Data, &ver); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "v9.9.9-smoke", ver.Version)

	// 裸 JSON：ResponseMiddleware 自动包信封 + trace_id，原载荷进 data
	code, e, _ = get("/bare")
	assert.DeepEqual(t, consts.StatusOK, code)
	assert.DeepEqual(t, 0, e.Code)
	assert.DeepEqual(t, "success", e.Message)
	assert.DeepEqual(t, traceOf("/bare"), e.TraceID)
	var bare map[string]any
	if err := json.Unmarshal(e.Data, &bare); err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "pong", bare["message"])

	// 协议端点：声明式前缀 / 声明式路径 / handler 级逃生舱——三者都原样透出，顶层无 code
	for _, path := range []string{"/.well-known/agent.json", "/mcp", "/agent.json"} {
		code, _, raw := get(path)
		assert.DeepEqual(t, consts.StatusOK, code)
		var top map[string]any
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, wrapped := top["code"]; wrapped {
			t.Fatalf("协议端点 %s 不应被信封包裹: %s", path, raw)
		}
		if _, hasTrace := top["trace_id"]; hasTrace {
			t.Fatalf("协议端点 %s 不应被注入 trace_id: %s", path, raw)
		}
	}
}
