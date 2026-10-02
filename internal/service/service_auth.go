// service_auth.go —— API 鉴权面：ARGUS_API_TOKENS 多 token 三档 scope + Bearer 中间件（fail-closed）。
package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	mw "git.example.com/enjoydream/ekit/middleware"
	"github.com/cloudwego/hertz/pkg/app"
)

// BearerMiddleware 🔒 路由的 Bearer 静态 token 校验（§5；webhook 不挂，靠平台签名）。
// scopeRank scope→覆盖等级闭集（read < ci < admin）。与 config.apiTokenScopeRank
// 同值——service 侧自持避免依赖方向变化，两表一致性由 rbac_test 钉住。
var scopeRank = map[string]int{"read": 1, "ci": 2, "admin": 3}

// authToken 中间件视图里的一条 token（旧单 token 等价 admin 条目）。
type authToken struct {
	token string
	scope string
}

// authTokens 生效 token 表：ARGUS_APITOKEN（admin）+ ARGUS_API_TOKENS 条目。
func (s *Service) authTokens() []authToken {
	var out []authToken
	if s.Cfg.APIToken != "" {
		out = append(out, authToken{token: s.Cfg.APIToken, scope: "admin"})
	}
	for _, t := range s.Cfg.APITokens {
		out = append(out, authToken{token: t.Token, scope: t.Scope})
	}
	return out
}

// requiredScope 方法+路径 → 所需权限档：POST /findings/*（误报抑制写库）=
// admin；其余 POST/DELETE（建任务、MCP 会话）= ci；读面 = read。
func requiredScope(method, path string) string {
	switch {
	case method == http.MethodPost && strings.HasPrefix(path, "/findings/"):
		return "admin"
	case method == http.MethodPost || method == http.MethodDelete:
		return "ci"
	default:
		return "read"
	}
}

// fail-closed（v3.0.1 修订——原实现在 APIToken 未配置时直接放行，等于鉴权裸奔）：
// token 表全空（ARGUS_APITOKEN 与 ARGUS_API_TOKENS 均未配置）时 🔒 路由一律拒绝
// 服务（webhook/健康检查不受影响）。v3.87.0 起带 scope 分级：命中 token 后按
// 方法+路径所需档位校验（admin ⊇ ci ⊇ read），不足 403。
func (s *Service) BearerMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokens := s.authTokens()
		if len(tokens) == 0 {
			s.log().Warn("argus.auth.rejected_no_token",
				"path", string(c.URI().Path()), "hint", "ARGUS_APITOKEN/ARGUS_API_TOKENS 均未配置，🔒 路由拒绝服务")
			mw.ErrorResp(c, http.StatusServiceUnavailable,
				"鉴权未配置：服务端未设置 ARGUS_APITOKEN/ARGUS_API_TOKENS，🔒 路由拒绝服务（webhook 与健康检查不受影响）")
			return
		}
		auth := string(c.GetHeader("Authorization"))
		granted := ""
		for _, t := range tokens {
			// 常数时间比较：与 SDK webhook 签名校验的纪律对齐（内部静态 token，纵深防御）
			if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+t.token)) == 1 {
				granted = t.scope
				break
			}
		}
		if granted == "" {
			mw.ErrorResp(c, http.StatusUnauthorized, "缺少或错误的 Authorization: Bearer token")
			return
		}
		need := requiredScope(string(c.Method()), string(c.Path()))
		if scopeRank[granted] < scopeRank[need] {
			s.log().Warn("argus.auth.scope_denied", "path", string(c.URI().Path()),
				"granted", granted, "required", need)
			mw.ErrorResp(c, http.StatusForbidden,
				fmt.Sprintf("token 权限档不足（%s < %s）", granted, need))
			return
		}
		c.Next(ctx)
	}
}

// ---------- TaskAPI ----------
