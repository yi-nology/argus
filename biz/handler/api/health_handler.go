// health_handler.go —— GET /reports/health 薄壳（v3.13.0 G5 数据面）。
// 路由在 main.go 手工注册（IDL 不承载聚合端点），Bearer 门控与 /tasks 同策略。
package api

import (
	"context"

	mw "git.example.com/enjoydream/ekit/middleware"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// HealthReport 学习环飞轮状态查询（噪声处置建议 / guidelines 增量建议 / 计数）。
// @router /reports/health [GET]
func HealthReport(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	resp, status := Svc.HealthReport(ctx)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, "健康报告构建失败")
		return
	}
	mw.SuccessRespMsg(c, resp, "ok")
}
