package service

import (
	"context"
	"testing"

	"github.com/yi-nology/argus/internal/config"
)

// TestCapabilities_FixPushMode 修复推送形态暴露（v7.0 阶段 2）：
// fix_push=环开关 bool（契约不变），fix_push_mode=branch|direct|""（新增面）。
func TestCapabilities_FixPushMode(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *config.Config
		wantOn   bool
		wantMode string
	}{
		{"nil cfg", nil, false, ""},
		{"branch", &config.Config{FixPush: true, FixPushMode: "branch"}, true, "branch"},
		{"direct", &config.Config{FixPush: true, FixPushMode: "direct"}, true, "direct"},
		{"off", &config.Config{}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Service{Cfg: c.cfg}
			caps := s.Capabilities(context.Background())
			if got := caps["fix_push"]; got != c.wantOn {
				t.Errorf("fix_push = %v, want %v", got, c.wantOn)
			}
			if got := caps["fix_push_mode"]; got != c.wantMode {
				t.Errorf("fix_push_mode = %v, want %q", got, c.wantMode)
			}
		})
	}
}
