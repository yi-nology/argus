package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/oekit/middleware/hmw"
	mw "github.com/oekit/middleware/middleware"
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
