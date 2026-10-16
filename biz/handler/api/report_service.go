// report_service.go v3.1 新增路由的 handler（GET /reports/:ref、GET /usage/monthly）。
// 路由在 router.go customizedRegister 注册（非 IDL 生成面）；鉴权复用 api.SetAuth。
package api

import (
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mw "github.com/oekit/middleware/middleware"
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

// StatsOverview 审查概览聚合（按仓任务量/verdict 分布/抑制计数，仪表盘数据面）。
// @router /stats/overview [GET]
func StatsOverview(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	days := 0
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			days = n
		}
	}
	data, status := Svc.StatsOverview(ctx, c.Query("platform"), c.Query("owner"), c.Query("repo"), days)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, "概览查询失败")
		return
	}
	mw.SuccessRespMsg(c, data, "ok")
}
