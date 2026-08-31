// events_service.go GET /events SSE 任务进度流（v3.1；🔒 复用 Bearer 鉴权）。
// 数据源 = progress.Bus（service 发布 created，runner 发布 running/finished）。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const sseKeepalive = 15 * time.Second

// Events SSE 流：task.created / task.running / task.finished（JSON in data:）。
// @router /events [GET]
func Events(ctx context.Context, c *app.RequestContext) {
	if Svc == nil || Svc.Bus == nil {
		c.String(consts.StatusServiceUnavailable, "event bus not ready")
		return
	}
	ch, cancel := Svc.Bus.Subscribe(ctx)
	defer cancel()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Nginx 反代不缓冲
	c.Status(consts.StatusOK)

	pr, pw := io.Pipe()
	c.SetBodyStream(pr, -1)

	go func() {
		defer pw.Close()
		// 连接即注释行探测（SSE 规范：冒号开头为注释）
		if _, err := fmt.Fprint(pw, ": connected\n\n"); err != nil {
			return
		}
		keepalive := time.NewTicker(sseKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-keepalive.C:
				if _, err := fmt.Fprint(pw, ": ping\n\n"); err != nil {
					return
				}
			case ev, ok := <-ch:
				if !ok {
					return
				}
				b, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", ev.Type, b); err != nil {
					return
				}
			}
		}
	}()
	// SetBodyStream 接管响应写：handler 返回后 Hertz 持续冲洗 body 流直至 pw 关闭
	<-ctx.Done()
}
