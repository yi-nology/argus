package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/plugin"
	"argus/internal/storage"
)

type fakeStore struct {
	repos   []storage.RepoRef
	pattern map[string][]storage.PatternSignal // key owner/repo
	low     map[string][]plugin.PatternState
	merged  [][3]any // keepID, dupID, repo
}

func (f *fakeStore) ListPatternRepos(context.Context) ([]storage.RepoRef, error) {
	return f.repos, nil
}

func (f *fakeStore) ListRepoPatterns(_ context.Context, _, owner, repo string) ([]storage.PatternSignal, error) {
	return f.pattern[owner+"/"+repo], nil
}

func (f *fakeStore) MergePatternInto(_ context.Context, _, owner, repo string, keepID, dupID uint, _ time.Time) error {
	f.merged = append(f.merged, [3]any{keepID, dupID, owner + "/" + repo})
	return nil
}

func (f *fakeStore) ListLowConfidencePatterns(_ context.Context, _, owner, repo string,
	_ int, _ float64, _ int) ([]plugin.PatternState, error) {
	return f.low[owner+"/"+repo], nil
}

// 去重：同 plugin + 同 prefix 的两行合并到样本更多的一行。
func TestDedupRepoMergesSimilarPatterns(t *testing.T) {
	// 前缀必须 > DedupPrefixRunes，截断后两行才相同
	prefix := strings.Repeat("未检查错误返回", 20) // 120 runes > 80
	a := storage.PatternSignal{
		ID: 1, Plugin: "security", Pattern: "aaa",
		PatternText: prefix + " 版本a",
		Posted:      2, Regret: 1,
	}
	b := storage.PatternSignal{
		ID: 2, Plugin: "security", Pattern: "bbb",
		PatternText: prefix + " 版本b",
		Posted:      0, Regret: 3, Suppress: 1,
	}
	// 不同 plugin 不合并
	c := storage.PatternSignal{ID: 3, Plugin: "style", Pattern: "ccc", PatternText: prefix + " 版本a"}

	st := &fakeStore{
		repos:   []storage.RepoRef{{Platform: "gitea", Owner: "acme", Repo: "app"}},
		pattern: map[string][]storage.PatternSignal{"acme/app": {a, b, c}},
	}
	d := &Dream{Store: st, Learning: plugin.DefaultLearningConfig()}
	n, _, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应合并 1 对，got %d merges=%v", n, st.merged)
	}
	// keep 应为 b（samples=4 > a 的 3）
	if st.merged[0][0] != uint(2) || st.merged[0][1] != uint(1) {
		t.Fatalf("keep 应为 ID=2（样本更多），got %v", st.merged[0])
	}
}

// 导出：低置信模式写成 markdown；无低置信时跳过。
func TestExportRepoWritesLocalMarkdown(t *testing.T) {
	dir := t.TempDir()
	st := &fakeStore{
		repos: []storage.RepoRef{{Platform: "gitea", Owner: "acme", Repo: "app"}},
		low: map[string][]plugin.PatternState{
			"acme/app": {{
				Plugin: "style", Pattern: "p1", PatternText: "建议提取魔法数字",
				Confidence: 0.2, Samples: 5, Regret: 2, Suppress: 1,
			}},
		},
	}
	d := &Dream{Store: st, KnowledgeDir: dir, Learning: plugin.LearningConfig{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35,
	}}
	_, exported, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exported != 1 {
		t.Fatalf("exported=%d", exported)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dream-acme-app.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "建议提取魔法数字") || !strings.Contains(string(raw), "style") {
		t.Fatalf("markdown 内容不完整: %s", raw)
	}

	// 负控：无低置信 → 不写文件
	st.low = map[string][]plugin.PatternState{}
	os.Remove(filepath.Join(dir, "dream-acme-app.md"))
	_, exported, err = d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exported != 0 {
		t.Fatal("无低置信不应导出")
	}
	if _, err := os.Stat(filepath.Join(dir, "dream-acme-app.md")); err == nil {
		t.Fatal("不应残留文件")
	}
}

func TestBetterKeepStable(t *testing.T) {
	a := storage.PatternSignal{ID: 1, Posted: 2}
	b := storage.PatternSignal{ID: 2, Posted: 2}
	if !betterKeep(a, b) == false && betterKeep(b, a) == false {
		// same samples → smaller ID wins → a better than b
	}
	if betterKeep(b, a) {
		t.Fatal("样本相同时应保留更小 ID")
	}
}
