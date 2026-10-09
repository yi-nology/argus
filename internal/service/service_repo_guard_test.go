package service

import (
	"context"
	"testing"

	"argus/internal/config"
)

// TestRepoAllowlist 外部入口仓库白名单语义（v3.134.0）：空=不限/精确/通配/拒绝。
func TestRepoAllowlist(t *testing.T) {
	mk := func(list []string) *Service {
		return &Service{Cfg: &config.Config{RepoAllowlist: list}}
	}
	if !mk(nil).repoAllowed("gitlab", "any", "repo") {
		t.Error("空清单应放行一切（向后兼容）")
	}
	s := mk([]string{"gitlab:demo-user/argus-validate", "gitlab:obs/*"})
	if !s.repoAllowed("gitlab", "demo-user", "argus-validate") {
		t.Error("精确条目应放行")
	}
	if !s.repoAllowed("gitlab", "obs", "insights-mcp-gateway") {
		t.Error("通配条目应放行")
	}
	if s.repoAllowed("gitlab", "demo-user", "other-repo") {
		t.Error("清单外仓库应拒绝")
	}
	if s.repoAllowed("gitlab", "demo-userx", "argus-validate") {
		t.Error("前缀相似不等于命中（owner 不同）")
	}
	if s.repoAllowed("github", "demo-user", "argus-validate") {
		t.Error("平台不同应拒绝")
	}
	// 拒绝以内容态/错误态可见，不伪装
	ctx := ContextWithScope(context.Background(), "admin")
	if _, err := s.SearchCode(ctx, "gitlab", "demo-user", "other-repo", "1", "x", "", false); err == nil || err.Error() != errRepoDenied {
		t.Errorf("SearchCode 拒绝应为统一文案, got %v", err)
	}
	if m, ok := s.LatestReview(ctx, "gitlab", "demo-user", "other-repo", "1"); !ok || m["error"] != errRepoDenied {
		t.Errorf("LatestReview 拒绝应内容态可见, got %v", m)
	}
}
