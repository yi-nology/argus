package sastscan

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFilterRegularFiles 软链剔除（v3.134.1）：外部扫描工具跟随软链读宿主文件、
// 以 finding 形式外带——清单里只许常规文件。
func TestFilterRegularFiles(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "app.go")
	if err := os.WriteFile(regular, []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self/environ", filepath.Join(dir, "leak.go")); err != nil {
		t.Skip(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.go"), 0o755); err != nil { // 目录伪装成 .go
		t.Fatal(err)
	}
	got := filterRegularFiles(dir, []string{"app.go", "leak.go", "sub.go", "missing.go"})
	if len(got) != 1 || got[0] != "app.go" {
		t.Errorf("filterRegularFiles = %v, want [app.go]", got)
	}
}
