// service_repo_guard.go —— 外部入口仓库白名单（v3.134.0，ARGUS_REPO_ALLOWLIST）：
// 数据类工具可读 bot GitLab token 可见的一切仓库代码是设计边界，泄露一个
// ci token = 全仓沦陷。白名单把「一切」收成「清单」：MCP/TaskAPI/QA 等外部
// 入口只能触达清单内仓库；webhook/poller 受信路径不受此门约束。
package service

import (
	"context"
	"strings"
)

// tokenReposKey 请求 token 仓库约束的 ctx 键（BearerMiddleware 写入）。
type tokenReposKey struct{}

// ContextWithTokenRepos 写入 token 的仓库约束（v3.135.0）。
func ContextWithTokenRepos(ctx context.Context, repos string) context.Context {
	return context.WithValue(ctx, tokenReposKey{}, repos)
}

// TokenReposFromContext 取请求 token 的仓库约束（空 = 不限）。
func TokenReposFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tokenReposKey{}).(string)
	return s
}

// matchRepoPattern 条目匹配："platform:owner/repo" 精确或 /* 前缀通配。
func matchRepoPattern(entry, target string) bool {
	if entry == target {
		return true
	}
	return strings.HasSuffix(entry, "/*") && strings.HasPrefix(target, strings.TrimSuffix(entry, "*"))
}

// repoAllowedFor 目标仓库是否可触达（v3.135.0 双门）：全局 ARGUS_REPO_ALLOWLIST
// 总白名单 AND 本 token 的仓库约束（token:scope@pattern）——单 token 泄露半径
// 压到其 pattern 内。空清单/空 pattern = 该项不限（向后兼容）。
func (s *Service) repoAllowedFor(ctx context.Context, platform, owner, repo string) bool {
	target := platform + ":" + owner + "/" + repo
	if s.Cfg != nil && len(s.Cfg.RepoAllowlist) > 0 {
		hit := false
		for _, e := range s.Cfg.RepoAllowlist {
			if matchRepoPattern(e, target) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if tp := TokenReposFromContext(ctx); tp != "" && !matchRepoPattern(tp, target) {
		return false
	}
	return true
}

// repoAllowed 无 ctx 场景的总白名单判定（内部受信路径用；外部入口一律走
// repoAllowedFor）。
func (s *Service) repoAllowed(platform, owner, repo string) bool {
	return s.repoAllowedFor(context.Background(), platform, owner, repo)
}

// errRepoDenied 白名单拒绝的统一错误文案。
const errRepoDenied = "仓库不在 ARGUS_REPO_ALLOWLIST 允许清单内"
