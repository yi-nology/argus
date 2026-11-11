// service_repomap_test.go —— RepoMap 工作目录隔离与凭据卫生回归（P0-03，#4 /
// P1-04，#9）：碰撞对映射唯一、token 不落 .git/config、跨仓地图内容互不可见、
// 提交更新后 freshness 失配触发刷新。克隆面用本地裸库（repomapCloneURLFn
// 测试钩子）真实走 git 子进程，不 mock。

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/config"
)

// repomapSeedRepo 建一个带一次提交的裸库 + 种子工作区（module 名做内容指纹）。
func repomapSeedRepo(t *testing.T, root, module string) (bare string) {
	t.Helper()
	bare = filepath.Join(root, module+".git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "init", "--bare", "-b", "main", bare)
	work := filepath.Join(root, "seed-"+module)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(work, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", fmt.Sprintf("module %s\n\ngo 1.27\n", module))
	write("main.go", fmt.Sprintf("package main\n\nimport %q\n\nfunc main() { _ = %s.Hello() }\n", module+"/pkg", module))
	if err := os.MkdirAll(filepath.Join(work, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("pkg", "pkg.go"), fmt.Sprintf("package pkg\n\nfunc Hello() string { return %q }\n", module))
	run(work, "init", "-b", "main")
	run(work, "config", "user.email", "test@test")
	run(work, "config", "user.name", "test")
	run(work, "add", ".")
	run(work, "commit", "-m", "seed")
	run(work, "push", bare, "main")
	return bare
}

// configForRepomap 本地裸库克隆的测试配置（平台 local，假凭据——验证不落盘）。
func configForRepomap(workRoot, bare string) *config.Config {
	return &config.Config{
		WorkRoot: workRoot,
		Platforms: map[string]config.PlatformConfig{
			"local": {Token: "secret-token", BaseURL: bare},
		},
	}
}

// repomapTestService 指向本地裸库克隆的测试 Service（平台 local，URL 钩子换
// 成直返裸库路径；token 注入假凭据验证不落盘）。
func repomapTestService(t *testing.T, workRoot, bare string) *Service {
	t.Helper()
	oldURL := repomapCloneURLFn
	repomapCloneURLFn = func(baseURL, owner, repo string) string { return bare }
	t.Cleanup(func() { repomapCloneURLFn = oldURL })
	return &Service{Cfg: configForRepomap(workRoot, bare)}
}

// TestRepoMapWorkDirCollision 碰撞对单测（#4 验收）：旧寻址 owner 内 "/" 拼接
// 转义使 g/s1+r 与 g+s1-r 撞同一目录；新寻址按三元组哈希派生必须互异。
func TestRepoMapWorkDirCollision(t *testing.T) {
	root := "/w"
	a := repoMapWorkDir(root, "gitlab", "g/s1", "r")
	b := repoMapWorkDir(root, "gitlab", "g", "s1-r")
	if a == b {
		t.Fatalf("碰撞对映射到同一目录（跨仓串仓）: %s", a)
	}
	if c := repoMapWorkDir(root, "gitlab", "g/s1", "r"); c != a {
		t.Fatalf("同身份映射应确定: %s != %s", c, a)
	}
	if strings.Contains(filepath.Base(a), "/") || strings.Contains(filepath.Base(a), "-") &&
		!strings.Contains(filepath.Base(a), "gitlab-") {
		t.Fatalf("目录名形态异常: %s", filepath.Base(a))
	}
}

// TestRepoMapCloneNoTokenOnDisk token 不落 .git/config 回归（#4 验收）：凭据走
// http.extraHeader（-c 作用域=单次调用不落盘），origin 为净 URL。
func TestRepoMapCloneNoTokenOnDisk(t *testing.T) {
	root := t.TempDir()
	bare := repomapSeedRepo(t, root, "tokdisk")
	s := repomapTestService(t, root, bare)
	dst, err := s.cloneForMap(context.Background(), "local", "o", "r")
	if err != nil {
		t.Fatalf("克隆失败: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dst, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-token", "oauth2:"} {
		if strings.Contains(string(cfg), leak) {
			t.Fatalf(".git/config 泄露凭据形态 %q:\n%s", leak, cfg)
		}
	}
	// 目录寻址走身份哈希
	if filepath.Base(dst) != filepath.Base(repoMapWorkDir(root, "local", "o", "r")) {
		t.Fatalf("克隆目录未按哈希寻址: %s", dst)
	}
}

// TestRepoMapCrossRepoIsolation 跨仓串仓回归（#4 验收）：两个内容互异的仓库
// 建图，各仓地图只见本仓包名——碰撞串仓形态在内容面收口验证。
func TestRepoMapCrossRepoIsolation(t *testing.T) {
	root := t.TempDir()
	bareA := repomapSeedRepo(t, root, "repomapa")
	bareB := repomapSeedRepo(t, root, "repomapb")
	ctx := context.Background()

	sA := repomapTestService(t, root, bareA)
	bA, err := sA.repoMap(ctx, "local", "o", "ra")
	if err != nil {
		t.Fatalf("仓 A 建图失败: %v", err)
	}
	sB := repomapTestService(t, root, bareB)
	bB, err := sB.repoMap(ctx, "local", "o", "rb")
	if err != nil {
		t.Fatalf("仓 B 建图失败: %v", err)
	}
	if string(bA) == string(bB) {
		t.Fatal("两仓地图快照完全相同（串仓）")
	}
	if !strings.Contains(string(bA), "pkg:pkg") || !strings.Contains(string(bB), "pkg:pkg") {
		t.Fatalf("快照缺包节点: A=%s B=%s", bA, bB)
	}
}

// TestRepoMapHeadMatchRefresh freshness 失配触发刷新（#9 验收）：提交新 commit
// 后远程 HEAD 漂移 → 探测按失配处理 → 缓存重建出含新文件的地图；探测失败
// （git 不可用形态）同样按失配 fail-safe 刷新，不再「任何失败按一致」。
func TestRepoMapHeadMatchRefresh(t *testing.T) {
	root := t.TempDir()
	bare := repomapSeedRepo(t, root, "refreshmap")
	s := repomapTestService(t, root, bare)
	ctx := context.Background()

	b1, err := s.repoMap(ctx, "local", "o", "rr")
	if err != nil {
		t.Fatalf("首建失败: %v", err)
	}

	// 远端推进一个新 commit（含新文件 refresh_marker.go）
	work := filepath.Join(root, "seed2")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "clone", "--depth", "1", bare, work)
	if err := os.WriteFile(filepath.Join(work, "refresh_marker.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "config", "user.email", "test@test")
	run(work, "config", "user.name", "test")
	run(work, "add", ".")
	run(work, "commit", "-m", "refresh")
	run(work, "push", bare, "main")

	// HEAD 失配必须被探测出来（修复前 ls-remote 缺 -C 恒失败按「一致」→ 永不刷新）
	matches := s.repoMapHeadMatches(ctx, repoMapWorkDir(root, "local", "o", "rr"), "local")
	if matches {
		t.Fatal("远端 HEAD 已推进，freshness 探测仍报一致（ls-remote -C 修复失效）")
	}
	b2, err := s.repoMap(ctx, "local", "o", "rr")
	if err != nil {
		t.Fatalf("刷新建图失败: %v", err)
	}
	if string(b1) == string(b2) {
		t.Fatal("提交更新后地图快照未变化（缓存刷新失效）")
	}
}

// TestRepoMapStaleCloneGC 孤儿目录治理（#4/#9 验收）：旧寻址残留目录（含
// token 落盘形态）立即清理；新寻址超保留期克隆清理；热仓目录与当前目录不动。
func TestRepoMapStaleCloneGC(t *testing.T) {
	root := t.TempDir()
	repomapRoot := filepath.Join(root, "repomap", "")
	if err := os.MkdirAll(repomapRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, old bool) string {
		p := filepath.Join(repomapRoot, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if old {
			past := time.Now().Add(-8 * 24 * time.Hour)
			_ = os.Chtimes(p, past, past)
		}
		return p
	}
	legacy := mk("local-legacy-oldrepo", false)     // 旧寻址拼接名
	orphan := mk("local-000000000000", true)        // 新寻址超保留期
	hot := mk("local-111111111111", false)          // 新寻址未过期
	keep := repoMapWorkDir(root, "local", "o", "r") // 当前请求的目录
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Service{Cfg: configForRepomap(root, "")}
	s.pruneRepomapClones(keep)
	for _, p := range []string{legacy, orphan} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("应清理 %s", p)
		}
	}
	for _, p := range []string{hot, keep} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不应清理 %s: %v", p, err)
		}
	}
}
