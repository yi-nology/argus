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

// TestIsFixBranch 防循环护栏（v7.0 阶段 2）：源分支属 argus/fix/* 命名空间
// = 本环自产修复 PR，push 环必须跳过（否则无限开修复 PR）；普通分支不受影响。
// fixBranchName 产物必须被护栏识别（两函数共用 fixBranchPrefix 单源的回归钉）。
func TestIsFixBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   bool
	}{
		{"argus/fix/t-abc123def456789011131500", true}, // 本环产物（fixBranchName 形态）
		{"argus/fix/anything", true},                   // 命名空间即拦，不校验 task 形态
		{"feature/foo", false},                         // 普通特性分支
		{"argus/fix", false},                           // 前缀缺斜杠不误伤
		{"argus/fixtures/x", false},                    // 相似前缀不误伤（fix ≠ fixtures）
		{"main", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isFixBranch(c.branch); got != c.want {
			t.Errorf("isFixBranch(%q) = %v, want %v", c.branch, got, c.want)
		}
	}
	// 单源钉：fixBranchName 产物恒被护栏识别
	if !isFixBranch(fixBranchName("t-deadbeef00112233")) {
		t.Error("fixBranchName 产物未被 isFixBranch 识别——两函数前缀漂移")
	}
}
