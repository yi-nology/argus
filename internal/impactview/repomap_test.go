package impactview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rmWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func edgeOf(s *Snapshot, rel string) int {
	n := 0
	for _, e := range s.Edges {
		if e.Rel == rel {
			n++
		}
	}
	return n
}

func TestRepoMapGoImportDirs(t *testing.T) {
	dir := t.TempDir()
	rmWrite(t, dir, "go.mod", "module example.com/rm\n\ngo 1.22\n")
	rmWrite(t, dir, "pkga/a.go", `package pkga

func F() {}
`)
	rmWrite(t, dir, "pkgb/b.go", `package pkgb

import "example.com/rm/pkga"

func G() { pkga.F() }
`)
	s := BuildRepoMap(dir, 5*time.Second)
	if edgeOf(s, RelImport) == 0 {
		t.Fatalf("pkgb→pkga import 边缺失: %+v", s.Edges)
	}
	if _, ok := dirNode(s, "pkga"); !ok {
		t.Fatalf("pkga 目录节点缺失: %+v", s.Nodes)
	}
	if s.Risk.Score != 0 {
		t.Fatalf("仓库全图不评分, 实得 %d", s.Risk.Score)
	}
}

func TestRepoMapShellSourceCrossDir(t *testing.T) {
	dir := t.TempDir()
	rmWrite(t, dir, "lib/common.sh", "db_connect() { :; }\n")
	rmWrite(t, dir, "bin/apply.sh", "source \"$(dirname \"$0\")/../lib/common.sh\"\n")
	s := BuildRepoMap(dir, 5*time.Second)
	if edgeOf(s, RelSource) == 0 {
		t.Fatalf("bin→lib source 聚合边缺失: %+v", s.Edges)
	}
}

func TestRepoMapSQLLineageAggregates(t *testing.T) {
	dir := t.TempDir()
	rmWrite(t, dir, "sql/01.sql", "CREATE TABLE iam_tenant (id int);\n")
	rmWrite(t, dir, "bin/dump.sql", "psql -c 'SELECT * FROM iam_tenant'\n")
	// 读方放独立目录：血缘聚合到目录对（同目录自环在目录图被过滤）
	rmWrite(t, dir, "report/read.sql", "SELECT count(*) FROM iam_tenant;\n")
	s := BuildRepoMap(dir, 5*time.Second)
	if edgeOf(s, RelLineage) == 0 {
		t.Fatalf("写/读目录血缘聚合边缺失: %+v", s.Edges)
	}
}

func TestRepoMapEmptyRepoNote(t *testing.T) {
	dir := t.TempDir()
	rmWrite(t, dir, "README.md", "# doc only\n")
	s := BuildRepoMap(dir, 5*time.Second)
	if len(s.Nodes) != 0 || !strings.Contains(strings.Join(s.Notes, "|"), "地图为空") {
		t.Fatalf("纯文档仓应空图+注明: nodes=%d notes=%v", len(s.Nodes), s.Notes)
	}
}
