package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ref() Ref { return Ref{Platform: "gitlab", Owner: "grp/sub", Repo: "demo"} }

func TestSaveLoadIndexRoundtrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	if err := Save(dir, ref(), "pattern-1", "pattern", "[r1] 空指针解引用（置信度 0.30）", "正文一", at); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Save(dir, ref(), "pattern-2", "pattern", "[r2] 错误未包裹（置信度 0.25）", "正文二", at); err != nil {
		t.Fatalf("Save: %v", err)
	}
	idx := LoadIndex(dir, ref(), 0)
	want1 := "- [r1] 空指针解引用（置信度 0.30）"
	want2 := "- [r2] 错误未包裹（置信度 0.25）"
	if !strings.Contains(idx, want1) || !strings.Contains(idx, want2) {
		t.Fatalf("索引应含两条事实，got %q", idx)
	}
	// 目录名消毒：嵌套组 "/" 不落盘为子目录
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.ContainsAny(entries[0].Name(), "/") {
		t.Fatalf("仓库目录应单层且消毒，got %v", entries)
	}
}

func TestSaveOverwriteIdempotent(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	_ = Save(dir, ref(), "p1", "pattern", "描述一", "b", at)
	_ = Save(dir, ref(), "p1", "pattern", "描述二", "b2", at)
	idx := LoadIndex(dir, ref(), 0)
	if strings.Contains(idx, "描述一") || !strings.Contains(idx, "描述二") {
		t.Fatalf("同名 slug 应覆盖，got %q", idx)
	}
}

func TestLoadIndexBudgetTruncates(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	for i := 0; i < 5; i++ {
		desc := strings.Repeat("长", 600)
		if err := Save(dir, ref(), string(rune('a'+i)), "pattern", desc, "b", at); err != nil {
			t.Fatal(err)
		}
	}
	idx := LoadIndex(dir, ref(), 1000)
	if len([]rune(idx)) > 1000+50 || !strings.Contains(idx, "其余记忆已省略") {
		t.Fatalf("超预算应截断并注明，rune=%d", len([]rune(idx)))
	}
}

func TestClearRepo(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	_ = Save(dir, ref(), "p1", "pattern", "d", "b", at)
	if LoadIndex(dir, ref(), 0) == "" {
		t.Fatal("前置：应有记忆")
	}
	if err := ClearRepo(dir, ref()); err != nil {
		t.Fatalf("ClearRepo: %v", err)
	}
	if LoadIndex(dir, ref(), 0) != "" {
		t.Fatal("清空后索引应为空")
	}
	// 不存在的目录：幂等
	if err := ClearRepo(dir, Ref{Platform: "x", Owner: "y", Repo: "z"}); err != nil {
		t.Fatalf("清空不存在目录应幂等: %v", err)
	}
}

func TestLoadIndexEmptyDir(t *testing.T) {
	if got := LoadIndex(t.TempDir(), ref(), 0); got != "" {
		t.Fatalf("空目录索引应为空串，got %q", got)
	}
	if got := LoadIndex(filepath.Join(t.TempDir(), "missing"), ref(), 0); got != "" {
		t.Fatalf("不存在目录索引应为空串，got %q", got)
	}
}

func TestLoadIndexWithOrg(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// 组织桶 + 仓桶各一条
	if err := Save(dir, OrgRef, "nil-deref", "pattern", "组织级：Go 项目空指针高发于 handler 层", "正文", now); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, Ref{Platform: "gitlab", Owner: "o", Repo: "r"}, "sql-n-plus-one", "pattern", "本仓：列表接口 N+1 已修过两次", "正文", now); err != nil {
		t.Fatal(err)
	}
	idx := LoadIndexWithOrg(dir, Ref{Platform: "gitlab", Owner: "o", Repo: "r"}, 0)
	if !strings.Contains(idx, "跨仓常见模式") || !strings.Contains(idx, "空指针") {
		t.Fatalf("组织段缺失: %q", idx)
	}
	if !strings.Contains(idx, "本仓记忆") || !strings.Contains(idx, "N+1") {
		t.Fatalf("仓段缺失: %q", idx)
	}
	// 组织桶跨仓共享：任何仓都应读到（组织桶语义即如此），换独立目录模拟
	// 无组织桶形态 → 纯仓段，无空节头
	dir2 := t.TempDir()
	if err := Save(dir2, Ref{Platform: "gitlab", Owner: "x", Repo: "y"}, "only-repo", "pattern", "另一仓的私有记忆", "正文", now); err != nil {
		t.Fatal(err)
	}
	idx2 := LoadIndexWithOrg(dir2, Ref{Platform: "gitlab", Owner: "x", Repo: "y"}, 0)
	if strings.Contains(idx2, "跨仓常见模式") {
		t.Fatalf("无组织桶不应出现组织节头: %q", idx2)
	}
}
