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

// repoAllowed 无 ctx 的总白名单判定〔v5.4.2 已删除〕：全仓零调用方（外部入口
// 一律 repoAllowedFor 携 ctx），deadcode 归零纪律下清除。

// errRepoDenied 白名单拒绝的统一错误文案。
const errRepoDenied = "仓库不在 ARGUS_REPO_ALLOWLIST 允许清单内"

// taskVisible 任务级读取的 token 仓库域校验（v3.140.0 数据可见性隔离）：
// token 带仓库约束时，task_id 维度读取（get_report/stages/findings/suggestion）
// 同样只能触达约束内仓库的任务——此前只闸了 repo 入参工具，持 task_id 即可
// 跨域读报告。platform=local（diff_preview 等无仓语义任务）按「持有 task_id ≈
// 持有能力」放行：任务 ID 是 24 位随机 hex 不可枚举，且预审闭环（提交方轮询
// 自己的 task_id）必须对被约束 token 可用。
func (s *Service) taskVisible(ctx context.Context, platform, owner, repo string) bool {
	tp := TokenReposFromContext(ctx)
	if tp == "" || platform == "local" {
		return true
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}

// taskListable 清单面的可见性（比单任务读取更严：local 任务也不列——任务存在性
// 与时间线本身是元数据，跨租户不泄露；自己的预审任务走 get_report 按 id 直读）。
func (s *Service) taskListable(ctx context.Context, platform, owner, repo string) bool {
	tp := TokenReposFromContext(ctx)
	if tp == "" {
		return true
	}
	if platform == "local" {
		return false
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}
