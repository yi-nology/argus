package config

import "testing"

// TestFixPushMode ARGUS_FIX_PUSH 值语义（v7.0 Verified Fix Loop 阶段 2）：
// "1"/"branch" → 修复分支+PR 新形态；"direct" → v3.74.1 直推旧形态；
// 其余（含空/"0"/垃圾值）→ 环关闭（与旧 `== "1"` 行为兼容）。
func TestFixPushMode(t *testing.T) {
	cases := []struct {
		in       string
		wantOn   bool
		wantMode string
	}{
		{"1", true, "branch"},      // 旧开关值升级为新形态（评估要求第一版不直推）
		{"branch", true, "branch"}, // 显式新形态
		{"direct", true, "direct"}, // 旧直推显式保留
		{"", false, ""},            // 缺省关
		{"0", false, ""},           // 显式关
		{"true", false, ""},        // 旧实现只认 "1"，不放宽
		{"garbage", false, ""},
	}
	for _, c := range cases {
		if got := fixPushEnabled(c.in); got != c.wantOn {
			t.Errorf("fixPushEnabled(%q) = %v, want %v", c.in, got, c.wantOn)
		}
		if got := fixPushMode(c.in); got != c.wantMode {
			t.Errorf("fixPushMode(%q) = %q, want %q", c.in, got, c.wantMode)
		}
	}
}
