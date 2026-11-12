package service

import (
	"context"
	"testing"

	"github.com/yi-nology/argus/internal/config"
)

// TestRepoAllowlist 外部入口仓库白名单语义（v3.134.0）：空=不限/精确/通配/拒绝。
func TestRepoAllowlist(t *testing.T) {
	mk := func(list []string) *Service {
		return &Service{Cfg: &config.Config{RepoAllowlist: list}}
	}
	// v5.4.2：repoAllowed（无 ctx 死代码）删除，断言改走活入口 repoAllowedFor。
	if !mk(nil).repoAllowedFor(context.Background(), "gitlab", "any", "repo") {
		t.Error("空清单应放行一切（向后兼容）")
	}
	s := mk([]string{"gitlab:demo-user/argus-validate", "gitlab:obs/*"})
	if !s.repoAllowedFor(context.Background(), "gitlab", "demo-user", "argus-validate") {
		t.Error("精确条目应放行")
	}
	if !s.repoAllowedFor(context.Background(), "gitlab", "obs", "insights-mcp-gateway") {
		t.Error("通配条目应放行")
	}
	if s.repoAllowedFor(context.Background(), "gitlab", "demo-user", "other-repo") {
		t.Error("清单外仓库应拒绝")
	}
	if s.repoAllowedFor(context.Background(), "gitlab", "demo-userx", "argus-validate") {
		t.Error("前缀相似不等于命中（owner 不同）")
	}
	if s.repoAllowedFor(context.Background(), "github", "demo-user", "argus-validate") {
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

// TestRepoAllowedFor_TokenRepos 双门语义（v3.135.0）：总白名单 AND token 自身
// 仓库约束——单 token 泄露半径压到其 pattern 内。
func TestRepoAllowedFor_TokenRepos(t *testing.T) {
	s := &Service{Cfg: &config.Config{
		RepoAllowlist: []string{"gitlab:demo-user/*", "gitlab:obs/*"},
		APITokens:     []config.APITokenScope{},
	}}
	// token 限定单仓：总白名单内但 token 外 → 拒
	ctx := ContextWithTokenRepos(context.Background(), "gitlab:demo-user/argus-validate")
	if !s.repoAllowedFor(ctx, "gitlab", "demo-user", "argus-validate") {
		t.Error("token pattern 内应放行")
	}
	if s.repoAllowedFor(ctx, "gitlab", "obs", "insights-mcp-gateway") {
		t.Error("token pattern 外应拒绝（即使总白名单内）")
	}
	// 无 token 约束 = 只看总白名单
	free := ContextWithTokenRepos(context.Background(), "")
	if !s.repoAllowedFor(free, "gitlab", "obs", "insights-mcp-gateway") {
		t.Error("无 token 约束应按总白名单放行")
	}
	// 双门同时限制
	scoped := ContextWithTokenRepos(context.Background(), "gitlab:obs/*")
	if s.repoAllowedFor(scoped, "gitlab", "demo-user", "argus-validate") {
		t.Error("总白名单内但 token 外应拒")
	}
	if s.repoAllowedFor(scoped, "github", "obs", "x") {
		t.Error("总白名单外应拒")
	}
}

// TestTokenRateOK 每 token 限速（v3.135.0）：突发超限返回 false，0=关闭放行。
func TestTokenRateOK(t *testing.T) {
	s := &Service{Cfg: &config.Config{APIRateRPM: 60}} // burst = max(10, 10) = 10
	allowed := 0
	for range 25 {
		if s.tokenRateOK("tok-a") {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("burst 内应恰放行 10 次，got %d", allowed)
	}
	// 不同 token 独立计数
	if !s.tokenRateOK("tok-b") {
		t.Error("另一 token 应有独立额度")
	}
	// 0 = 关闭
	s2 := &Service{Cfg: &config.Config{APIRateRPM: 0}}
	for range 100 {
		if !s2.tokenRateOK("tok-c") {
			t.Error("限速关闭时不应拒绝")
		}
	}
}
