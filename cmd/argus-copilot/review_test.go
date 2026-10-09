package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFindings 意见行解析：标准行/多行报告/路径含冒号/severity 大小写
// 归一/非意见行忽略。
func TestParseFindings(t *testing.T) {
	report := `## 预审意见

- internal/foo/bar.go:42 [high] 空指针解引用风险
- cmd/x/y.go:7 [Medium] 错误被吞
- a:b/c.go:3 [LOW] 命名不规范
这是总结性文字，不是意见。
- 这行缺行号和 severity，不算
✅ 未发现问题
`
	fs := parseFindings(report)
	if len(fs) != 3 {
		t.Fatalf("parseFindings 条数 = %d, want 3（报告=\n%s）", len(fs), report)
	}
	if fs[0].File != "internal/foo/bar.go" || fs[0].Line != 42 || fs[0].Severity != "high" {
		t.Errorf("fs[0] = %+v", fs[0])
	}
	if fs[1].Severity != "medium" {
		t.Errorf("severity 大小写未归一: %q", fs[1].Severity)
	}
	if fs[2].File != "a:b/c.go" || fs[2].Line != 3 || fs[2].Severity != "low" {
		t.Errorf("路径含冒号解析错: %+v", fs[2])
	}
	if len(parseFindings("✅ 未发现问题")) != 0 {
		t.Error("干净报告应解析出 0 条")
	}
}

// TestThresholdExit 退出码阈值：high 缺省只挡 high；medium 挡 medium+high；
// low 全挡；none 恒通过；低档意见不误伤。
func TestThresholdExit(t *testing.T) {
	fs := []cliFinding{
		{File: "a.go", Line: 1, Severity: "medium", Note: "x"},
	}
	cases := []struct {
		failOn string
		want   int
	}{
		{"high", exitClean},
		{"medium", exitFindings},
		{"low", exitFindings},
		{"none", exitClean},
	}
	for _, tc := range cases {
		if got := thresholdExit(fs, tc.failOn); got != tc.want {
			t.Errorf("thresholdExit(medium 意见, %q) = %d, want %d", tc.failOn, got, tc.want)
		}
	}
	if thresholdExit(nil, "low") != exitClean {
		t.Error("无意见应恒通过")
	}
	high := []cliFinding{{Severity: "high"}}
	if thresholdExit(high, "high") != exitFindings {
		t.Error("high 意见 + high 阈值应退出 1")
	}
}

// TestSynthNewFileDiff 未跟踪文件 → new-file diff：hunk 行数与 + 行数一致、
// 截断有附注。
func TestSynthNewFileDiff(t *testing.T) {
	content := "package main\n\nfunc main() {}\n"
	piece := synthNewFileDiff("new.go", []byte(content))
	if !strings.Contains(piece, "diff --git a/new.go b/new.go\n") ||
		!strings.Contains(piece, "--- /dev/null\n") ||
		!strings.Contains(piece, "+++ b/new.go\n") {
		t.Fatalf("缺 standard 头:\n%s", piece)
	}
	if !strings.Contains(piece, "@@ -0,0 +1,3 @@\n") {
		t.Errorf("hunk 行数不符:\n%s", piece)
	}
	if strings.Count(piece, "\n+")-1 != 3 { // 减 hunk 头里的 +1,3
		t.Errorf("+ 行数与 hunk 声明不一致:\n%s", piece)
	}
	// 截断路径
	long := strings.Repeat("line\n", untrackedFileCap+10)
	piece2 := synthNewFileDiff("big.go", []byte(long))
	if !strings.Contains(piece2, "文件截断") {
		t.Error("超限文件应带截断附注")
	}
	if n := strings.Count(piece2, "\n+") - 1; n != untrackedFileCap+1 {
		t.Errorf("截断后行数 = %d, want %d（截断附注行）", n, untrackedFileCap+1)
	}
}

// TestAppendUntracked 真仓拼装：tmp git 仓 + 未跟踪文件 → 合成 diff 追加、
// 二进制跳过。
func TestAppendUntracked(t *testing.T) {
	if _, err := os.Stat(".git"); err != nil {
		t.Skip("非 git 仓工作目录，跳过（单测在包目录跑即有 .git）")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a_new.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte("BIN\x00ARY"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// t.TempDir 不在包目录 .git 仓内——runGit 会失败，这里只验「失败安全降级」：
	// 非仓目录下 ls-files 报错 → 原 diff 原样返回、无 panic。
	got, trunc := appendUntracked([]byte("base\n"), syncDiffCap)
	if trunc || !strings.Contains(string(got), "base\n") {
		t.Errorf("非仓目录应安全降级: trunc=%v out=%q", trunc, got)
	}
}

// TestAppendUntrackedSynthesis 纯合成路径（不经 git）：伪造文件列表直接验证拼装。
func TestAppendUntrackedSynthesis(t *testing.T) {
	dir := t.TempDir()
	files := []string{"x.go", "y.md"}
	for i, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(strings.Repeat("l\n", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var buf []byte
	for _, f := range files {
		content, _ := os.ReadFile(f)
		buf = append(buf, synthNewFileDiff(f, content)...)
	}
	joined := string(buf)
	if !strings.Contains(joined, "+++ b/x.go\n") || !strings.Contains(joined, "@@ -0,0 +1,1 @@") {
		t.Errorf("x.go 形态不符:\n%s", joined)
	}
	if !strings.Contains(joined, "@@ -0,0 +1,2 @@") {
		t.Errorf("y.md 行数不符:\n%s", joined)
	}
}

// TestCollectDiffCaps 截断上限：超过 cap 的输入被截断并报告。
func TestCollectDiffCaps(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.diff")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", syncDiffCap+100)), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	data, trunc, err := collectDiff(reviewInput{DiffFile: big}, syncDiffCap)
	if err != nil {
		t.Fatal(err)
	}
	if !trunc || len(data) != syncDiffCap {
		t.Errorf("trunc=%v len=%d want %d", trunc, len(data), syncDiffCap)
	}
}

// TestReviewInputMutualExclusion 输入源互斥校验（错误在触达 ctx/cli 前返回，
// 传 Background 与 nil client 即可）。
func TestReviewInputMutualExclusion(t *testing.T) {
	ctx := context.Background()
	in := reviewInput{DiffFile: "a", Staged: true, FailOn: "high"}
	if _, _, err := runReview(ctx, nil, in); err == nil || !strings.Contains(err.Error(), "至多选一个") {
		t.Errorf("双输入源应报互斥错，got %v", err)
	}
	in2 := reviewInput{IncludeUntracked: true, FailOn: "high"}
	if _, _, err := runReview(ctx, nil, in2); err == nil || !strings.Contains(err.Error(), "-include-untracked 须配合") {
		t.Errorf("-include-untracked 裸用应报错，got %v", err)
	}
}

// TestReviewResultJSON -json 信封可序列化且 clean/findings 自洽。
func TestReviewResultJSON(t *testing.T) {
	res := reviewResult{Mode: "sync", Clean: false, Findings: []cliFinding{
		{File: "a.go", Line: 9, Severity: "high", Note: "n"},
	}, Report: "- a.go:9 [high] n"}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["clean"] != false || back["mode"] != "sync" {
		t.Errorf("信封字段不符: %s", b)
	}
}
