package ocrshim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitOnPath 把一个伪造 git 放上 PATH：记录完整 argv 到 <probe>、
// 删除 <dir>/.git/shallow。
func fakeGitOnPath(t *testing.T) (probe string) {
	t.Helper()
	bin := t.TempDir()
	probe = filepath.Join(bin, "called")
	script := "#!/bin/sh\necho \"$@\" >> " + probe + "\n" +
		`if [ "$1" = "-C" ]; then /bin/rm -f "$2/.git/shallow"; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return probe
}

// 有 .git/shallow → 执行 git fetch --unshallow origin + 显式全量 refspec
// （OCR range 审查的 merge-base 在浅历史下必挂，实测复现；unshallow 后恢复）。
// refspec 必须显式：clone --depth 1 隐含 single-branch，裸 fetch 只补默认分支的
// 远端引用，PR base 非默认分支时 origin/<base> 不存在（与 ocrdeleg 同款，真件复现）。
func TestUnshallowNeeded(t *testing.T) {
	probe := fakeGitOnPath(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unshallowIfNeeded(context.Background(), dir, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "shallow")); !os.IsNotExist(err) {
		t.Fatal("shallow 标记应已被 fake git 移除")
	}
	argv, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal("git 应被调用")
	}
	if !strings.Contains(string(argv), "+refs/heads/*:refs/remotes/origin/*") {
		t.Fatalf("fetch 必须带显式全量 refspec: %s", argv)
	}
}

// 无 shallow 标记（完整克隆）→ 不额外 fetch。
func TestUnshallowSkippedWhenComplete(t *testing.T) {
	probe := fakeGitOnPath(t)
	dir := t.TempDir()
	if err := unshallowIfNeeded(context.Background(), dir, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("完整克隆不应触发 git fetch")
	}
}

// git 失败 → 错误上抛（带 stderr 尾巴，可诊断）；stderr 里带凭证的 clone URL
// 必须被脱敏——此错误经 MCP 原样回传 argus，可落入日志与 PR 评论（v3.9.0 审查修复）。
func TestUnshallowGitFailureSurfaces(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'fatal: unable to access https://oauth2:sup3rsecret@host/o/r.git/: some error' >&2\nexit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := unshallowIfNeeded(context.Background(), dir, 10*time.Second)
	if err == nil {
		t.Fatal("git 失败应上抛")
	}
	if !strings.Contains(err.Error(), "some error") {
		t.Fatalf("错误应含 stderr 尾巴: %v", err)
	}
	if strings.Contains(err.Error(), "sup3rsecret") {
		t.Fatalf("带凭证 URL 必须被脱敏: %v", err)
	}
}
