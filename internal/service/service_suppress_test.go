package service

import (
	"context"
	"strings"
	"testing"

	"argus/internal/config"
)

// TestSuppressFindingScoped_Gate scope 二次判定的 fail-closed 语义：
// 无 scope（未过中间件）与非 admin 档一律拒，admin 才进业务校验。
func TestSuppressFindingScoped_Gate(t *testing.T) {
	s := &Service{Cfg: &config.Config{}}
	_, err := s.SuppressFindingScoped(context.Background(), "gitlab", "acme", "app", "1", "abc123")
	if err == nil || !strings.Contains(err.Error(), "需 admin 档") {
		t.Errorf("ctx 无 scope 应拒（fail-closed），got %v", err)
	}
	ciCtx := ContextWithScope(context.Background(), "ci")
	_, err = s.SuppressFindingScoped(ciCtx, "gitlab", "acme", "app", "1", "abc123")
	if err == nil || !strings.Contains(err.Error(), "需 admin 档") {
		t.Errorf("ci 档应拒，got %v", err)
	}
	rdCtx := ContextWithScope(context.Background(), "read")
	if _, err = s.SuppressFindingScoped(rdCtx, "gitlab", "acme", "app", "1", "abc123"); err == nil {
		t.Error("read 档应拒")
	}
}

// TestSuppressFindingScoped_Validation admin 档进业务前的入参校验（不触 Store）。
func TestSuppressFindingScoped_Validation(t *testing.T) {
	s := &Service{Cfg: &config.Config{}}
	ctx := ContextWithScope(context.Background(), "admin")
	cases := []struct{ owner, repo, number, fp, want string }{
		{"", "app", "1", "abc123", "owner/repo"},
		{"acme", "app", "1x", "abc123", "纯数字"},
		{"acme", "app", "1", "abc", "指纹短码"},
		{"acme", "app@evil", "1", "abc123", "非法字符"},
	}
	for _, tc := range cases {
		_, err := s.SuppressFindingScoped(ctx, "gitlab", tc.owner, tc.repo, tc.number, tc.fp)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("入参 (%q,%q,%q,%q) 应报含 %q 的错，got %v", tc.owner, tc.repo, tc.number, tc.fp, tc.want, err)
		}
	}
}

// TestRestoreFindingScoped_Gate restore 的 scope 二次判定与入参校验（同 suppress
// 口径，fail-closed）。
func TestRestoreFindingScoped_Gate(t *testing.T) {
	s := &Service{Cfg: &config.Config{}}
	_, err := s.RestoreFindingScoped(context.Background(), "gitlab", "acme", "app", "1", "abc123")
	if err == nil || !strings.Contains(err.Error(), "需 admin 档") {
		t.Errorf("ctx 无 scope 应拒，got %v", err)
	}
	ciCtx := ContextWithScope(context.Background(), "ci")
	if _, err = s.RestoreFindingScoped(ciCtx, "gitlab", "acme", "app", "1", "abc123"); err == nil {
		t.Error("ci 档应拒")
	}
	ctx := ContextWithScope(context.Background(), "admin")
	for _, tc := range []struct{ owner, number, fp, want string }{
		{"", "1", "abc123", "owner/repo"},
		{"acme", "1x", "abc123", "纯数字"},
		{"acme", "1", "abc", "指纹短码"},
	} {
		if _, err := s.RestoreFindingScoped(ctx, "gitlab", tc.owner, "app", tc.number, tc.fp); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("入参(%q,%q,%q) 应报含 %q 的错，got %v", tc.owner, tc.number, tc.fp, tc.want, err)
		}
	}
}
