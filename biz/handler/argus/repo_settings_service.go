// repo_settings_service.go —— 服务侧按仓配置路由 handler（v3.187.0）。
// 路由自 v3.189.0 起由 idl/argus.thrift 的 RepoSettingsService 生成承载；
// 鉴权 authMw（routerapi.SetAuth），写档（PUT/DELETE）由 service.requiredScope
// 收口为 admin、读档 read。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mw "github.com/oekit/middleware"
)

// repoSettingsBodyMax 写入体上限：三个整数旋钮的规范 JSON 远小于此，上限只为
// 防滥用（错误内容 Parse 会拒）。
const repoSettingsBodyMax = 4_096

// RepoSettingsGet 单仓配置视图（覆盖集 + 全局生效值）。
// @router /repo-settings/:platform/:owner/:repo [GET]
func RepoSettingsGet(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.RepoSettingsView(ctx,
		c.Param("platform"), c.Param("owner"), c.Param("repo"))
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, viewErr(data))
		return
	}
	mw.SuccessRespMsg(c, data, "ok")
}

// RepoSettingsList 全部按仓配置（观测面）。
// @router /repo-settings [GET]
func RepoSettingsList(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.ListRepoSettings(ctx)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, "按仓配置查询失败")
		return
	}
	mw.SuccessRespMsg(c, data, "ok")
}

// RepoSettingsPut 写入单仓覆盖（admin 档；body 为 reposettings.Settings JSON）。
// @router /repo-settings/:platform/:owner/:repo [PUT]
func RepoSettingsPut(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	body := c.Request.Body()
	if len(body) == 0 {
		mw.ErrorResp(c, consts.StatusBadRequest, "请求体为空")
		return
	}
	if len(body) > repoSettingsBodyMax {
		mw.ErrorResp(c, consts.StatusBadRequest, "请求体超过 4KB 上限")
		return
	}
	data, status := Svc.UpsertRepoSettings(ctx,
		c.Param("platform"), c.Param("owner"), c.Param("repo"), body)
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, viewErr(data))
		return
	}
	mw.SuccessRespMsg(c, data, "repo settings updated")
}

// RepoSettingsDelete 移除单仓覆盖（admin 档；幂等）。
// @router /repo-settings/:platform/:owner/:repo [DELETE]
func RepoSettingsDelete(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.DeleteRepoSettings(ctx,
		c.Param("platform"), c.Param("owner"), c.Param("repo"))
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, viewErr(data))
		return
	}
	mw.SuccessRespMsg(c, data, "ok")
}

// viewErr service 返回 map 里携带的错误文案（校验失败时 data 非nil带 error 键）。
func viewErr(data map[string]any) string {
	if msg, ok := data["error"].(string); ok && msg != "" {
		return msg
	}
	return "请求失败"
}
