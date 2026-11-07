package impactview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitInit 幂等建仓（test 专用：本地身份免全局配置）。
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir,
			"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
}

func writeAndCommit(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t",
		"add", rel)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-qm", msg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
}

func fdStatusOf(fds []FuncDiff, name string) string {
	for _, fd := range fds {
		if fd.Name == name {
			return fd.Status
		}
	}
	return ""
}

func TestDiffScriptFunctionsShell(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	v1 := "#!/bin/bash\nold_fn() {\n  echo v1\n}\nkeep_fn() {\n  echo keep\n}\n"
	writeAndCommit(t, dir, "bin/a.sh", v1, "v1")
	v2 := "#!/bin/bash\nkeep_fn() {\n  echo keep-v2\n}\nnew_fn() {\n  echo new\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "bin/a.sh"), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-aqm", "v2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit v2: %v: %s", err, out)
	}
	// touched 区间覆盖 keep_fn/new_fn 定义行（2 与 5），old_fn 已删除无从触达
	ranges := map[string][][2]int{"bin/a.sh": {{2, 2}, {5, 5}}}
	fds, notes := diffScriptFunctions(context.Background(), dir, "HEAD~1",
		[]string{"bin/a.sh"}, ranges)
	if len(notes) != 0 {
		t.Fatalf("不应有 note: %v", notes)
	}
	if got := fdStatusOf(fds, "new_fn"); got != FuncAdded {
		t.Fatalf("new_fn 应 added, 实得 %s (%+v)", got, fds)
	}
	if got := fdStatusOf(fds, "keep_fn"); got != FuncModified {
		t.Fatalf("keep_fn 应 modified, 实得 %s", got)
	}
	if got := fdStatusOf(fds, "old_fn"); got != FuncRemoved {
		t.Fatalf("old_fn 应 removed, 实得 %s", got)
	}
	for _, fd := range fds {
		if fd.Status == FuncModified && fd.Name == "keep_fn" {
			if !fd.SigSame {
				t.Fatal("签名同为 keep_fn() 且未变, SigSame 应为 true")
			}
			if fd.Before.Lines == fd.After.Lines && fd.Before.BodyHash == fd.After.BodyHash {
				t.Fatal("体行数/指纹应反映变化")
			}
		}
	}
}

func TestDiffScriptFunctionsPyUntouchedNotReported(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	writeAndCommit(t, dir, "m.py", "def a():\n    pass\n\ndef b():\n    pass\n", "v1")
	// 只改 def a 体；def b 定义行未被 hunk 触达
	if err := os.WriteFile(filepath.Join(dir, "m.py"),
		[]byte("def a():\n    return 1\n\ndef b():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-aqm", "v2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	ranges := map[string][][2]int{"m.py": {{1, 2}}}
	fds, _ := diffScriptFunctions(context.Background(), dir, "HEAD~1", []string{"m.py"}, ranges)
	if got := fdStatusOf(fds, "a"); got != FuncModified {
		t.Fatalf("a 应 modified, 实得 %s (%+v)", got, fds)
	}
	if got := fdStatusOf(fds, "b"); got != "" {
		t.Fatalf("未触达的 b 不应上报, 实得 %s", got)
	}
}
