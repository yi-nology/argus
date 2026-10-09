package codesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSymlinkReadEscapeGuard(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("ARGUS_GITLAB_TOKEN=glpat-STOLEN-SECRET\n"), 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "innocent.md")); err != nil {
		t.Skip(err)
	}
	res, err := Search(context.Background(), dir, "glpat-STOLEN", Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Render()
	if strings.Contains(out, "glpat-STOLEN") {
		t.Errorf("软链内容不得进检索结果: %s", out)
	} else {
		t.Log("safe: symlink content not leaked")
	}
}
