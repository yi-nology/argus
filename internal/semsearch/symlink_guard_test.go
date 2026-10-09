package semsearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type echoEmbedder struct{}

func (echoEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (echoEmbedder) Dim() int { return 3 }

func TestSymlinkReadEscapeGuard(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.env")
	os.WriteFile(secret, []byte("ARGUS_GITLAB_TOKEN=glpat-STOLEN-SECRET\n"), 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "notes.md")); err != nil {
		t.Skip(err)
	}
	ix := NewIndex(echoEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 直查 chunk 文本里有没有泄漏
	hits, _ := ix.Search(context.Background(), "token", 5, 0)
	for _, h := range hits {
		if strings.Contains(h.Text, "glpat-STOLEN") {
			t.Errorf("软链内容不得进索引: %s", h.Text)
		}
	}
	// scanDir 层面直接验
	files, _ := scanDir(dir)
	for _, f := range files {
		if strings.Contains(string(f.content), "glpat-STOLEN") {
			t.Errorf("软链内容不得进扫描结果: %s", f.rel)
		}
	}
	t.Log("done")
}
