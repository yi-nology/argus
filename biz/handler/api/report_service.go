// report_service.go v3.1 新增路由的 handler（GET /reports/:ref、GET /usage/monthly）。
// 路由在 router.go customizedRegister 注册（非 IDL 生成面）；鉴权复用 api.SetAuth。
package api

import (
	"context"

	mw "git.example.com/enjoydream/ekit/middleware"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// ReportByRef 按 head SHA 查最近一次成功审查报告。
// @router /reports/:ref [GET]
func ReportByRef(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		mw.ErrorResp(c, consts.StatusBadRequest, "缺少 ref")
		return
	}
	resp, status := Svc.GetReportByRef(ctx, ref)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, "未找到该 commit 的报告")
		return
	}
	mw.SuccessRespMsg(c, resp, "ok")
}

// UsageMonthly 本月各仓库 token 用量与预算水位。
// @router /usage/monthly [GET]
func UsageMonthly(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.UsageMonthly(ctx)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, "用量查询失败")
		return
	}
	mw.SuccessRespMsg(c, data, "ok")
}
