// respond.go —— handler 响应壳单源（v6.3.0 收敛）：
// 此前每 handler 重复 status!=200 → ErrorResp → SuccessRespMsg 三段式，
// 收敛为 bind/reply/replyMsg；文件名不在 hz 的 *_service.go 命名空间，
// hz update 不会触碰本文件。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/oekit/hertz/validatorh"
	mw "github.com/oekit/middleware"
)

// bind 请求绑定单源：失败 → mw.Fail（400 族），返回是否可继续。
func bind(ctx context.Context, c *app.RequestContext, req any) bool {
	if err := validatorh.BindAndValidate(c, req); err != nil {
		mw.Fail(ctx, c, err)
		return false
	}
	return true
}

// reply 统一响应壳：200 → SuccessRespMsg(ok)；否则 ErrorResp(failMsg)。
// (data, status) 双值服务返回形态的单点收敛。
func reply(c *app.RequestContext, status int, resp any, failMsg string) {
	replyMsg(c, status, resp, "ok", failMsg)
}

// replyMsg 同 reply，ok 文案可定制（repo-settings put / webhook 用）。
func replyMsg(c *app.RequestContext, status int, resp any, okMsg, failMsg string) {
	if status != consts.StatusOK {
		mw.ErrorResp(c, status, failMsg)
		return
	}
	mw.SuccessRespMsg(c, resp, okMsg)
}
