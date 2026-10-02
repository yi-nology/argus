package qa

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// v3.91.0：search_code 工具绑定目录构造——可检索真实内容；dir 为空时不挂。
func TestSearchCodeToolFor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package app\n\nfunc limitUnbounded() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Log: testNopLogger{}}

	st := e.searchCodeToolFor(dir)
	if st == nil {
		t.Fatal("dir 非空应返回工具")
	}
	info, err := st.Info(context.Background())
	if err != nil || info.Name != "search_code" {
		t.Fatalf("工具名不符: %v %v", info, err)
	}

	if e.searchCodeToolFor("") != nil {
		t.Fatal("空 dir 不应返回工具")
	}
}
