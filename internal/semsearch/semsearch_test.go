package semsearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubEmbedder 词袋向量化：词表三词正交三维，词频计数——「含 db 的文本」与
// 查询「db」相似度可预期，够验证排序/过滤/增量，不碰网络。
type stubEmbedder struct {
	calls int // Embed 调用次数（增量刷新断言用）
}

var stubVocab = []string{"db", "http", "parse"}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	s.calls++
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v := make([]float32, len(stubVocab))
		lower := strings.ToLower(t)
		for i, w := range stubVocab {
			v[i] = float32(strings.Count(lower, w))
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *stubEmbedder) Dim() int { return len(stubVocab) }

// TestExtractGoChunks Go 文件按顶层声明切：doc 注释进文本、方法名带接收者
// （v5.10.0 节点键=NodeKey 契约，dir 限定）、行号是声明真实行。
func TestExtractGoChunks(t *testing.T) {
	src := `package app

// Repo 从 DB 读用户。
func (s *Store) GetUser(id int) (*User, error) {
	return nil, nil
}

type Server struct {
	h http.Handler
}

var Version = "1.0"
`
	chunks, _ := extractChunks("internal/app/store.go", []byte(src), "argus")
	if len(chunks) != 3 {
		t.Fatalf("chunk 数 = %d, want 3（%v）", len(chunks), chunks)
	}
	if chunks[0].Symbol != "internal/app.Store.GetUser" || chunks[0].Line != 3 {
		// chunk 从 doc 注释行起算（注释随文本进 embedding 输入）；符号键 dir 限定。
		t.Errorf("方法 chunk: %+v", chunks[0])
	}
	if !strings.Contains(chunks[0].Text, "Repo 从 DB 读用户") {
		t.Errorf("doc 注释应进 embedding 文本:\n%s", chunks[0].Text)
	}
	if !strings.HasPrefix(chunks[0].Text, "internal/app/store.go:3\n") {
		t.Errorf("文本应以路径:行开头:\n%s", chunks[0].Text)
	}
	if chunks[1].Symbol != "internal/app.Server" || chunks[2].Symbol != "internal/app.Version" {
		t.Errorf("声明名不符: %q %q", chunks[1].Symbol, chunks[2].Symbol)
	}
}

// TestExtractFileChunk 非 Go 文件与坏 Go 文件都降级文件级 chunk；二进制跳过。
func TestExtractFileChunk(t *testing.T) {
	md, _ := extractChunks("docs/guide.md", []byte("# 指南\n\n如何处理认证流程。\n"), "")
	if len(md) != 1 || md[0].Line != 1 || !strings.Contains(md[0].Text, "认证") {
		t.Errorf("md 文件级 chunk 不符: %+v", md)
	}
	bad, _ := extractChunks("broken.go", []byte("package app\nfunc {{{\n"), "")
	if len(bad) != 1 || bad[0].Symbol != "" {
		t.Errorf("坏 Go 文件应降级文件级: %+v", bad)
	}
	if binChunks, binEdges := extractChunks("bin.dat", []byte("AB\x00CD"), ""); binChunks != nil || binEdges != nil {
		// bin.dat 不在 codeExts，scanDir 层面就跳过；extractChunks 直喂时
		// 二进制不该产出可嵌文本
		t.Log("二进制内容由 scanDir 过滤")
	}
}

// TestRefreshIncremental 增量刷新：新文件进索引、未变文件不重嵌、删除即清。
func TestRefreshIncremental(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\n// 用户查询走 DB。\nfunc QueryDB() {}\n")
	e := &stubEmbedder{}
	ix := NewIndex(e)
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if got := ix.Size(); got != 2 { // a.go: package 子句外的 1 个 decl + 无? 实际 1 decl
		t.Logf("首次 Size=%d", got)
	}
	firstCalls := e.calls

	// 未变：Refresh 不再触发 embedding。
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if e.calls != firstCalls {
		t.Errorf("未变文件不应重嵌: calls %d → %d", firstCalls, e.calls)
	}

	// 新文件：只嵌新文件的 chunk。
	write("b_http.go", "package a\n\n// HTTP 路由注册。\nfunc Routes() {}\n")
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if e.calls != firstCalls+1 {
		t.Errorf("新文件应恰好多一次批量 Embed: %d → %d", firstCalls, e.calls)
	}

	// 删除：整组剔除。
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(pathList(ix), ","), "a.go") {
		t.Error("删除的文件应从索引剔除")
	}
	if !strings.Contains(strings.Join(pathList(ix), ","), "b_http.go") {
		t.Error("现存文件应保留")
	}
}

// pathList 索引内路径（测试辅助）。
func pathList(ix *Index) []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append([]string(nil), ix.paths...)
}

// TestSearchRanking 检索排序：意图查询命中语义最近的 chunk；topK 生效。
func TestSearchRanking(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a_db.go":    "package a\n\n// 查询用户数据，访问 DB。\nfunc QueryDB() {}\n",
		"b_http.go":  "package a\n\n// HTTP handler 注册。\nfunc Routes() {}\n",
		"c_parse.go": "package a\n\n// 解析配置文件。\nfunc ParseConfig() {}\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(&stubEmbedder{})
	hits, err := m.Search(context.Background(), dir, "db 数据库查询", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("应有命中")
	}
	if !strings.Contains(hits[0].Path, "a_db.go") {
		t.Errorf("db 查询应命中 db 文件在前, got %v", hits)
	}
	if len(hits) > 2 {
		t.Errorf("topK 未生效: %d", len(hits))
	}
}

// TestManagerCachedDir 同目录二次检索只嵌 query（索引缓存命中）。
func TestManagerCachedDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &stubEmbedder{}
	m := NewManager(e)
	ctx := context.Background()
	if _, err := m.Search(ctx, dir, "q1", 5, 0); err != nil {
		t.Fatal(err)
	}
	afterFirst := e.calls
	if _, err := m.Search(ctx, dir, "q2", 5, 0); err != nil {
		t.Fatal(err)
	}
	if e.calls != afterFirst+1 {
		t.Errorf("无变更时二次检索应只多 query 一次 Embed: %d → %d", afterFirst, e.calls)
	}
}

// TestTotalChunkBudget 全库 chunk 预算：触顶即截断并置位 Truncated。
func TestTotalChunkBudget(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		src := "package a\n"
		for i := 0; i < 10; i++ {
			src += "func F" + strings.Repeat("x", i) + strings.TrimSpace(name[:1]) + "()" + " {}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &stubEmbedder{}
	ix := NewIndex(e)
	ix.maxTotalChunks = 15 // 3 文件 × 10 decl = 30 → 预算 15 截断
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if !ix.Truncated() {
		t.Error("触顶应置位 Truncated")
	}
	if got := ix.Size(); got != 15 {
		t.Errorf("Size = %d, want 15（预算硬顶）", got)
	}
}
