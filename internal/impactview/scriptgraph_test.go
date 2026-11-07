// scriptgraph_test.go —— 非 Go 仓词法图（v5.6.0）：deploy-sql 形态夹具验证
// shell source 传导 / 函数调用位 / SQL 表血缘三条边。

package impactview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sgFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sgSnap(changed ...string) *Snapshot {
	s := &Snapshot{Version: SnapshotVersion, Changed: changed}
	for _, f := range changed {
		s.Nodes = append(s.Nodes, Node{ID: "file:" + f, Kind: KindFile, Label: shortLabel(f), Changed: true, Detail: f})
	}
	return s
}

func sgCountRel(s *Snapshot, rel string) int {
	n := 0
	for _, e := range s.Edges {
		if e.Rel == rel {
			n++
		}
	}
	return n
}

func TestScriptLayerShellSourceAndCalls(t *testing.T) {
	dir := t.TempDir()
	// lib/common.sh 定义函数 db_connect；bin/apply.sh source 它并调用；
	// 改 lib 后 apply 应被波及（source 边），且改动自身函数有调用边。
	sgFile(t, dir, "lib/common.sh", `#!/bin/bash
db_connect() {
  echo connect
}
unused_helper() {
  :
}
`)
	sgFile(t, dir, "bin/apply.sh", `#!/bin/bash
source "$(dirname "$0")/../lib/common.sh"
db_connect
`)
	// 改动方换成 lib/common.sh 自身
	s := sgSnap("lib/common.sh")
	affected, calls := buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if affected == 0 {
		t.Fatalf("改动 lib/common.sh 应波及装载方 bin/apply.sh")
	}
	if sgCountRel(s, RelSource) == 0 {
		t.Fatalf("应存在 source 边: %+v", s.Edges)
	}
	if calls == 0 { // lib 定义的 db_connect 被 apply 调用（caller 边同批产出）
		t.Fatalf("改动方 lib 的 db_connect 应命中调用位, 实得 %d", calls)
	}
	// 改 bin/apply.sh：无人装载/调用它 → 词法图不应造边（buildScriptLayer 允许 0）
	s2 := sgSnap("bin/apply.sh")
	a2, _ := buildScriptLayer(context.Background(), dir, s2, time.Now().Add(5*time.Second))
	_ = a2 // bin 定义无函数、无下游：允许 0
}

func TestScriptLayerFuncCallEdges(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "bin/apply.sh", `#!/bin/bash
render_env() {
  echo env
}
render_env
`)
	sgFile(t, dir, "bin/backup.sh", `#!/bin/bash
render_env   # 复用同一渲染逻辑（他文件调用位）
`)
	s := sgSnap("bin/apply.sh")
	_, calls := buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if calls == 0 {
		t.Fatalf("render_env 改动应命中调用方 backup.sh")
	}
	syms := 0
	for _, n := range s.Nodes {
		if n.Kind == KindSymbol && strings.HasPrefix(n.ID, "sym:bin/apply.sh:render_env") {
			syms++
		}
	}
	if syms == 0 {
		t.Fatalf("应生成改动符号枢纽节点")
	}
}

func TestScriptLayerSQLLineage(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "sql/01_schema.sql", `
CREATE TABLE iam_tenant (id int);
INSERT INTO iam_user SELECT * FROM iam_tenant;
`)
	sgFile(t, dir, "sql/02_report.sql", `SELECT count(*) FROM iam_tenant;
`)
	s := sgSnap("sql/01_schema.sql")
	affected, _ := buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if affected == 0 || sgCountRel(s, RelLineage) == 0 {
		t.Fatalf("01 建表应经血缘波及 02 读方: edges=%+v", s.Edges)
	}
	tableNodes := 0
	for _, n := range s.Nodes {
		if n.Kind == KindTable {
			tableNodes++
		}
	}
	if tableNodes == 0 {
		t.Fatalf("应生成 table 枢纽节点")
	}
}

func TestScriptLayerNoGoRepoNoteFlow(t *testing.T) {
	dir := t.TempDir()
	sgFile(t, dir, "a.sh", "#!/bin/bash\nsay_hi() { :; }\nsay_hi\n")
	sgFile(t, dir, "b.sh", "#!/bin/bash\n. ./a.sh\nsay_hi\n")
	s := sgSnap("a.sh")
	affected, _ := buildScriptLayer(context.Background(), dir, s, time.Now().Add(5*time.Second))
	if affected == 0 {
		t.Fatalf("b.sh 装载 a.sh 应被波及")
	}
	if !strings.Contains(strings.Join(s.Notes, "|"), "词法图补充") {
		t.Fatalf("产出边必须注明词法图: %v", s.Notes)
	}
}
