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
