package impactview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestScriptLayerRustModUse(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "src/lib.rs", "mod util;\npub mod ops;\n")
	sgFile(t, dir, "src/util.rs", "pub fn fmt() {}\n")
	sgFile(t, dir, "src/ops/mod.rs", "pub fn run() {}\n")
	// use crate::util + super::（子模块引用兄弟模块）
	sgFile(t, dir, "src/ops/job.rs", "use crate::util;\nuse super::run;\nfn go() { util::fmt(); run(); }\n")
	s := sgSnap("src/util.rs")
	affected, calls := buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if sgCountRel(s, RelSource) < 1 {
		t.Fatalf("util.rs 应被 job.rs 经 crate::use 命中: edges=%+v", s.Edges)
	}
	if calls == 0 {
		t.Fatalf("rust fn fmt 应命中调用位")
	}
	_ = affected
	// mod 声明：lib.rs 改动 → 波及 util.rs/ops/mod.rs（装载方向=lib 装载模块?）
	// 语义：job.rs use ops → ops 变 → job 受影响；lib.rs mod ops 声明本身改动
	// 影响 ops 面的可见性——sourcedBy 反向边（装载 ops 的文件=lib.rs）已覆盖。
	s2 := sgSnap("src/ops/mod.rs")
	buildScriptLayer(context.Background(), dir, s2, time.Now().Add(5*time.Second))
	if sgCountRel(s2, RelSource) < 1 {
		t.Fatalf("ops/mod.rs 应被 lib.rs(mod) 或 job.rs(use) 命中: %+v", s2.Edges)
	}
}

func TestScriptLayerJavaImport(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "src/main/java/com/foo/Bar.java", "package com.foo;\npublic class Bar {}\n")
	sgFile(t, dir, "src/main/java/com/app/App.java", "import com.foo.Bar;\npublic class App {}\n")
	sgFile(t, dir, "src/main/java/com/app/Wild.java", "import com.foo.*;\npublic class Wild {}\n")
	s := sgSnap("src/main/java/com/foo/Bar.java")
	buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if sgCountRel(s, RelSource) < 1 {
		t.Fatalf("Bar 应被 App 的 import 命中: %+v", s.Edges)
	}
	for _, e := range s.Edges {
		if e.Rel == RelSource && e.To == "file:src/main/java/com/app/Wild.java" {
			t.Fatal("通配 import 不应入图")
		}
	}
}

func TestScriptLayerCInclude(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "inc/util.h", "#ifndef U\n#define U\n#endif\n")
	sgFile(t, dir, "src/main.c", "#include <stdio.h>\n#include \"../inc/util.h\"\nint main() { return 0; }\n")
	s := sgSnap("inc/util.h")
	buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if sgCountRel(s, RelSource) < 1 {
		t.Fatalf("util.h 应被 main.c include 命中: %+v", s.Edges)
	}
	for _, e := range s.Edges {
		if e.To == "file:stdio.h" {
			t.Fatal("系统头文件不应入图")
		}
	}
}

func TestDiffScriptFunctionsRust(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeAndCommit(t, dir, "src/lib.rs", "pub fn old() {}\npub fn keep() {}\n", "v1")
	if err := os.WriteFile(filepath.Join(dir, "src/lib.rs"),
		[]byte("pub fn keep() {\n    println!(\"v2\");\n}\npub fn brand_new() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-aqm", "v2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit v2: %v: %s", err, out)
	}
	ranges := map[string][][2]int{"src/lib.rs": {{1, 3}, {4, 5}}}
	fds, notes := diffScriptFunctions(context.Background(), dir, "HEAD~1", []string{"src/lib.rs"}, ranges)
	if len(notes) != 0 {
		t.Fatalf("不应有 note: %v", notes)
	}
	if got := fdStatusOf(fds, "old"); got != FuncRemoved {
		t.Fatalf("old 应 removed, 实得 %s (%+v)", got, fds)
	}
	if got := fdStatusOf(fds, "keep"); got != FuncModified {
		t.Fatalf("keep 应 modified, 实得 %s", got)
	}
	if got := fdStatusOf(fds, "brand_new"); got != FuncAdded {
		t.Fatalf("brand_new 应 added, 实得 %s", got)
	}
}
