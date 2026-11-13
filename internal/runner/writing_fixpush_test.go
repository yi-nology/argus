package runner

import "testing"

// TestFixBranchName 修复分支命名（v7.0 阶段 2 验收线）：argus/fix/<taskID>
// 自有前缀——与常见保护分支（main/master/release/*）无交集，branch 形态
// 任何推送目标都不可能落在保护分支上。
func TestFixBranchName(t *testing.T) {
	got := fixBranchName("t-abc123def456789011131500")
	want := "argus/fix/t-abc123def456789011131500"
	if got != want {
		t.Fatalf("fixBranchName = %q, want %q", got, want)
	}
	// 修复分支永不与保护分支词表相等（前缀保证）
	for _, protected := range []string{"main", "master", "release", "develop"} {
		if got == protected {
			t.Errorf("修复分支名 %q 撞保护分支 %q", got, protected)
		}
	}
}
