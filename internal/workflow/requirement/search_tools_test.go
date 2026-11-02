package requirement

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oekit/logx"

	"argus/internal/plugin"
)

// v3.84.0：CodeSearchDir 非空时 R3/R4 工具表挂 search_code，空时不挂
// （既有行为不变）。
func TestR3ToolsCodeSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package app\n\nfunc handlerPaginate() { limitUnbounded() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	names := func(w *Workflow) []string {
		tools, _, err := w.r3Tools(context.Background(), plugin.Task{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tl := range tools {
			info, err := tl.Info(context.Background())
			if err != nil {
				continue
			}
			out = append(out, info.Name)
		}
		return out
	}

	with := names(&Workflow{
		CodeSearchDir:    dir,
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	})
	hasSearch := false
	for _, n := range with {
		if n == "search_code" {
			hasSearch = true
		}
	}
	if !hasSearch {
		t.Fatalf("CodeSearchDir 非空时应挂 search_code: %v", with)
	}

	for _, n := range names(&Workflow{
		Log:              logx.Nop(),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}) {
		if n == "search_code" {
			t.Fatalf("CodeSearchDir 为空时不应挂 search_code: %v", with)
		}
	}
}
