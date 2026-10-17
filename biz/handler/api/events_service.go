// events_service.go GET /events SSE 任务进度流（v3.1；🔒 复用 Bearer 鉴权）。
// 数据源 = progress.Bus（service 发布 created，runner 发布 running/finished）。
// 流式写入走 ekit hertz.NewStream（底层 hertzsse.Writer：自动设头、逐事件 flush、
// 断连时写失败返回 error 自然退出，消灭 io.Pipe + 手拼帧 + 专用 goroutine）。
package api

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	hertzsse "github.com/oekit/hertz/sse"
	mw "github.com/oekit/middleware"

	"argus/internal/obs"
)

const sseKeepalive = 15 * time.Second

// Events SSE 流：task.created / task.running / task.finished（JSON in data:）。
// @router /events [GET]
func Events(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	if Svc.Bus == nil {
		mw.ErrorResp(c, consts.StatusServiceUnavailable, "event bus not ready")
		return
	}
	ch, cancel := Svc.Bus.Subscribe(ctx)
	defer cancel()
	obs.AddSSEConnections(1)
	defer obs.AddSSEConnections(-1)
	dropped0 := Svc.Bus.Dropped()
	defer func() {
		// 连接期丢弃增量 > 0：该客户端曾积压（进度条停在中间态可归因）
		if d := Svc.Bus.Dropped() - dropped0; d > 0 && Svc.Log != nil {
			Svc.Log.Warn("argus.events.dropped", "drops", d)
		}
	}()

	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Nginx 反代不缓冲（Content-Type/Cache-Control 由 sse.Writer 设置）

	s := hertzsse.NewStream(c)
	defer func() { _ = s.Close() }() // 关闭错误对客户端不可见（连接已在断开路径上）
	_ = s.KeepAlive()                // 连接即注释行探测（SSE 规范：冒号开头为注释，客户端忽略）

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if err := s.KeepAlive(); err != nil {
				return // 客户端断连：写失败即退出
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := s.Send(ev.Type, ev); err != nil {
				return
			}
		}
	}
}
