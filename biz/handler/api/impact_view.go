// impact_view.go —— 影响面视图路由 handler（GET /tasks/:taskId/impact，v3.183.0）。
// 路由在 router.go customizedRegister 注册（非 IDL 生成面）；鉴权复用 routerapi.Auth。
// 返回自包含交互 HTML（零外链，内网/离线可用）。
package api

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mw "github.com/oekit/middleware"
)

// TaskImpact 任务影响面视图（text/html 自包含单文件）。
// @router /tasks/:taskId/impact [GET]
func TaskImpact(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	taskID := c.Param("taskId")
	if taskID == "" {
		mw.ErrorResp(c, consts.StatusBadRequest, "缺少 taskId")
		return
	}
	page, ok := Svc.ImpactViewHTML(ctx, taskID)
	if !ok {
		mw.ErrorResp(c, consts.StatusNotFound, "任务不存在或尚无影响面快照")
		return
	}
	// c.Data 直写字节体——c.String 会覆盖先前 set 的 Content-Type 为
	// text/plain（hertz 行为，实浏览器调试抓出），HTML 必须显式以 text/html 下发。
	c.Data(consts.StatusOK, "text/html; charset=utf-8", []byte(page))
}
