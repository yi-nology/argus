// service_repo_guard.go —— 外部入口仓库白名单（v3.134.0，ARGUS_REPO_ALLOWLIST）：
// 数据类工具可读 bot GitLab token 可见的一切仓库代码是设计边界，泄露一个
// ci token = 全仓沦陷。白名单把「一切」收成「清单」：MCP/TaskAPI/QA 等外部
// 入口只能触达清单内仓库；webhook/poller 受信路径不受此门约束。
package service

import "strings"

// repoAllowed 目标仓库是否在允许清单（空清单 = 不限，向后兼容）。
// 条目形如 "platform:owner/repo"，repo 可用 /* 后缀按前缀放行。
func (s *Service) repoAllowed(platform, owner, repo string) bool {
	if s.Cfg == nil || len(s.Cfg.RepoAllowlist) == 0 {
		return true
	}
	target := platform + ":" + owner + "/" + repo
	for _, e := range s.Cfg.RepoAllowlist {
		if e == target {
			return true
		}
		if strings.HasSuffix(e, "/*") && strings.HasPrefix(target, strings.TrimSuffix(e, "*")) {
			return true
		}
	}
	return false
}

// errRepoDenied 白名单拒绝的统一错误文案。
const errRepoDenied = "仓库不在 ARGUS_REPO_ALLOWLIST 允许清单内"
