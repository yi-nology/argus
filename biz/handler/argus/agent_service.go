// agent_service.go ListAgents handler（v6.3.0 归位 AgentService：原挂 TaskService、
// 落 task_service.go，IDL 拆分时随 service 归位迁移）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/oekit/hertz/validatorh"
	mw "github.com/oekit/middleware"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ListAgents .
// @router /agents [GET]
func ListAgents(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	var req argus.ListAgentsRequest
	_ = validatorh.BindAndValidate(c, &req) // query 绑定（可全缺省=纯全局清单）
	platform, owner, repo := "", "", ""
	if req.Platform != nil {
		platform = *req.Platform
	}
	if req.Owner != nil {
		owner = *req.Owner
	}
	if req.Repo != nil {
		repo = *req.Repo
	}
	mw.SuccessRespMsg(c, Svc.ListAgents(ctx, platform, owner, repo), "ok")
}
