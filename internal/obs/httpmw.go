// httpmw.go —— 请求级指标中间件（v3.9.2）：此前 5xx 率与时延只能靠日志聚合。
// path 用路由模板（FullPath()）避免高基数；未匹配路由（404 直落）记 "unmatched"。
package obs

import (
	"context"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// HTTPMiddleware 请求级指标：argus_http_requests_total + 时延分布。挂最外层
// （main 的 h.Use），覆盖全部路由（含 /webhook /tasks /mcp）。
func HTTPMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		start := time.Now()
		c.Next(ctx)
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		labels := []string{string(c.Method()), path, strconv.Itoa(c.Response.StatusCode())}
		httpRequests.WithLabelValues(labels...).Inc()
		httpDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
	}
}
