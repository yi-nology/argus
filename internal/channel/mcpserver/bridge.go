// bridge.go —— MCP streamable http ↔ Hertz 桥接（v3.165.0 自 server.go 拆出；
// 零逻辑变更）。

package mcpserver

import (
	"bytes"
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/mark3labs/mcp-go/server"
	mw "github.com/oekit/middleware"
)

// ---------- Hertz 桥接 ----------

// HertzBridge 把 mcp-go 的 streamable http handler 桥接为 hertz handler。
// 桥接保留响应头（Mcp-Session-Id 等）与状态码；GET 长连流不支持服务端推送刷新
// （工具调用为 POST 一问一答，不受影响）。
func HertzBridge(mcpSrv *server.MCPServer) app.HandlerFunc {
	inner := server.NewStreamableHTTPServer(mcpSrv)
	return func(ctx context.Context, c *app.RequestContext) {
		mw.SkipResponseWrap(c) // MCP JSON-RPC 协议载荷：客户端按规范解析顶层字段，禁止信封包裹
		req, err := http.NewRequest(string(c.Method()), string(c.URI().RequestURI()), bytes.NewReader(c.Request.Body()))
		if err != nil {
			c.SetStatusCode(http.StatusBadRequest)
			return
		}
		// 传播 hertz 请求的 ctx：客户端断连/超时能取消 inner handler（runReview 轮询随之退出），
		// 否则 ctx 恒为 background，取消永不传播。
		req = req.WithContext(ctx)
		req.URL.Scheme = "http"
		req.URL.Host = string(c.Host())
		c.Request.Header.VisitAll(func(k, v []byte) {
			req.Header.Add(string(k), string(v))
		})
		w := &bridgeWriter{c: c, header: http.Header{}}
		inner.ServeHTTP(w, req)
		for k, vs := range w.header {
			for _, v := range vs {
				c.Header(k, v)
			}
		}
	}
}

// bridgeWriter net/http ResponseWriter → hertz RequestContext 桥。
type bridgeWriter struct {
	c           *app.RequestContext
	header      http.Header
	wroteHeader bool
}

func (w *bridgeWriter) Header() http.Header { return w.header }

func (w *bridgeWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.c.Write(p)
}

func (w *bridgeWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.c.SetStatusCode(code)
	}
}

// Flush 将缓冲数据推送给客户端（Hertz hijack writer 或裸 Response）。
func (w *bridgeWriter) Flush() {
	_ = w.c.Flush()
}
