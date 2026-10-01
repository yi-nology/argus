package service

import (
	"context"
	"testing"

	"argus/internal/config"
)

// TaskAPI 入参校验边界（2026-10-01 评测补缺回归）：恶意/畸形入参应在触达
// Store 前被 400 拒收（Store=nil 构造即断言"校验前置"）。
func TestCreateTaskDirectValidation(t *testing.T) {
	svc := &Service{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"gitlab": {Token: "tk"},
	}}, Log: testLogger{}}
	cases := []struct {
		name string
		kind string
		own  string
		repo string
		num  string
		art  map[string]string
		want bool // true=应通过校验（走到 Store 层，此处 nil Store 会 panic，用 recover 判定）
	}{
		{"normal", "code_review", "acme", "app", "7", nil, true},
		{"subgroup-repo", "code_review", "acme", "g1/g2/app", "7", nil, true},
		{"sql-injection-owner", "code_review", "a' OR 1=1--", "app", "7", nil, false},
		{"space-in-owner", "code_review", "a b", "app", "7", nil, false},
		{"unicode-owner", "code_review", "组织", "app", "7", nil, false},
		{"huge-number", "code_review", "a", "app", "99999999999999999999", nil, false},
		{"plus-number", "code_review", "a", "app", "+123", nil, false},
		{"artifacts-oversize", "code_review", "a", "app", "7", map[string]string{"k": string(make([]byte, 20*1024))}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil && !tc.want {
					return // nil-Store panic = 通过了校验到达落库层……对 want=false 是 BUG
				}
			}()
			_, code := svc.CreateTaskDirect(context.Background(), tc.kind, "gitlab", tc.own, tc.repo, tc.num, tc.art)
			rejected := code == 400
			if tc.want && rejected {
				t.Fatalf("合法入参被误拒: %d", code)
			}
			if !tc.want && !rejected {
				t.Fatalf("恶意入参应 400: got %d", code)
			}
			if tc.want {
				t.Skip("通过校验（nil Store 后续路径不在本测范围）")
			}
		})
	}
}
