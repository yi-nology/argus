package requirement

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/codegraph"
	"argus/internal/plugin"
)

// v3.15.0 P0-1：CodeGraph 非 nil 时 R3/R4 工具表挂 get_symbol/get_references，
// nil 时不挂（既有行为不变）。
func TestR3ToolsCodegraph(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package app\n\nfunc Handler() error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := codegraph.NewIndex(dir, 10)
	if err != nil {
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

	w := &Workflow{
		CodeGraph:        idx,
		Log:              logx.NewSlogLogger("argus-test"),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	}
	with := names(w)
	hasSym, hasRefs := false, false
	for _, n := range with {
		switch n {
		case "get_symbol":
			hasSym = true
		case "get_references":
			hasRefs = true
		}
	}
	if !hasSym || !hasRefs {
		t.Fatalf("工具表缺 codegraph 工具: %v", with)
	}

	// nil CodeGraph：不挂（既有行为）
	without := names(&Workflow{
		Log:              logx.NewSlogLogger("argus-test"),
		GetFileMaxChars:  1000,
		GetIssueMaxChars: 1000,
		GetDiffMaxChars:  1000,
	})
	for _, n := range without {
		if n == "get_symbol" || n == "get_references" {
			t.Fatalf("nil CodeGraph 不应挂 %s: %v", n, without)
		}
	}
}
