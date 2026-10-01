package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"argus/internal/learning"
	"argus/internal/memory"
	"argus/internal/storage"
)

type fakeStore struct {
	repos   []storage.RepoRef
	pattern map[string][]storage.PatternSignal // key owner/repo
	low     map[string][]learning.PatternState
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
	_ int, _ float64, _ int) ([]learning.PatternState, error) {
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
	d := &Dream{Store: st, Learning: learning.DefaultConfig()}
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
		low: map[string][]learning.PatternState{
			"acme/app": {{
				Plugin: "style", Pattern: "p1", PatternText: "建议提取魔法数字",
				Confidence: 0.2, Samples: 5, Regret: 2, Suppress: 1,
			}},
		},
	}
	d := &Dream{Store: st, KnowledgeDir: dir, Learning: learning.Config{
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

	// 负控：无低置信 → 不再残留旧误报结论（v3.8.0 改为写澄清文案覆盖）
	st.low = map[string][]learning.PatternState{}
	os.Remove(filepath.Join(dir, "dream-acme-app.md"))
	_, exported, err = d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exported != 0 {
		t.Fatal("无低置信不应导出")
	}
	raw2, err := os.ReadFile(filepath.Join(dir, "dream-acme-app.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw2), "建议提取魔法数字") || !strings.Contains(string(raw2), "没有样本充足且低置信") {
		t.Fatalf("无低置信时应覆盖为澄清文案: %s", raw2)
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

// 记忆面双写（v3.35.0）：低置信模式逐事实落 MemoryDir（一文件一事实），
// 澄清路径同步退场——与知识导出同源同生命周期。
func TestExportRepoWritesMemoryFacts(t *testing.T) {
	kdir := t.TempDir()
	mdir := t.TempDir()
	st := &fakeStore{
		repos: []storage.RepoRef{{Platform: "gitea", Owner: "acme", Repo: "app"}},
		low: map[string][]learning.PatternState{
			"acme/app": {
				{Plugin: "style", Pattern: "p1", PatternText: "建议提取魔法数字\n第二行应压平",
					Confidence: 0.2, Samples: 5, Regret: 2, Suppress: 1},
				{Plugin: "robust", Pattern: "p2", PatternText: "错误未上抛",
					Confidence: 0.3, Samples: 4, Regret: 1, Suppress: 0},
			},
		},
	}
	d := &Dream{Store: st, KnowledgeDir: kdir, MemoryDir: mdir, Learning: learning.Config{
		Enabled: true, MinSamples: 3, SoftFloor: 0.35,
	}}
	if _, exported, err := d.RunOnce(context.Background()); err != nil || exported != 1 {
		t.Fatalf("exported=%d err=%v", exported, err)
	}
	ref := memory.Ref{Platform: "gitea", Owner: "acme", Repo: "app"}
	idx := memory.LoadIndex(mdir, ref, 0)
	if !strings.Contains(idx, "魔法数字") || !strings.Contains(idx, "错误未上抛") {
		t.Fatalf("记忆索引应含两条事实，got %q", idx)
	}
	if strings.Contains(idx, "魔法数字\n第二行") {
		t.Fatal("description 内换行应压平（单行 frontmatter）")
	}
	// 澄清路径：记忆同步清空（不留场）
	st.low = map[string][]learning.PatternState{}
	if _, _, err := d.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := memory.LoadIndex(mdir, ref, 0); got != "" {
		t.Fatalf("无低置信后记忆应退场，got %q", got)
	}
}

func TestExportOrgMemoryCrossRepoAggregation(t *testing.T) {
	// 同一 pattern 文本在两个仓出现 → 组织桶应聚合出一条跨仓模式
	shared := "未检查 err 就继续使用返回值"
	st := &fakeStore{
		repos: []storage.RepoRef{
			{Platform: "gitea", Owner: "acme", Repo: "app"},
			{Platform: "gitea", Owner: "acme", Repo: "web"},
		},
		low: map[string][]learning.PatternState{
			"acme/app": {{Pattern: shared, Plugin: "security", Confidence: 0.4, Samples: 5}},
			"acme/web": {{Pattern: shared, Plugin: "security", Confidence: 0.3, Samples: 3}},
		},
	}
	dir := t.TempDir()
	d := &Dream{Store: st, Learning: learning.DefaultConfig(), MemoryDir: dir}
	_, _, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	orgDir := filepath.Join(dir, "_org")
	entries, rerr := os.ReadDir(orgDir)
	if rerr != nil || len(entries) == 0 {
		t.Fatalf("组织桶应有聚合文件: %v", rerr)
	}
	raw, _ := os.ReadFile(filepath.Join(orgDir, entries[0].Name()))
	if !strings.Contains(string(raw), "2 仓共现") || !strings.Contains(string(raw), "acme/app") {
		t.Fatalf("聚合内容不符: %s", raw)
	}
	// 单仓独有模式不应入组织桶
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(orgDir, e.Name()))
		if strings.Contains(string(b), "1 仓共现") {
			t.Fatalf("单仓模式不应入组织桶: %s", b)
		}
	}
}
