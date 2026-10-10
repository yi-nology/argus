package service

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"argus/internal/config"
	"argus/internal/domain"
	"argus/internal/semsearch"
	"argus/internal/storage"

	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"github.com/cloudwego/eino/components/tool"
)

// fakeKnowledge rag.KnowledgeService 最小桩（AsTool 与 MCP 面无关，回 nil 同 Noop）。
type fakeKnowledge struct {
	chunks []rag.Chunk
	err    error
	query  string
	topK   int
}

func (f *fakeKnowledge) Retrieve(_ context.Context, query string, topK int, _ rag.Filter) ([]rag.Chunk, error) {
	f.query, f.topK = query, topK
	return f.chunks, f.err
}

func (f *fakeKnowledge) AsTool() tool.BaseTool { return nil }

// TestSearchKnowledge_Guards 未配置（nil/Noop）与空参的运行期守卫。
func TestSearchKnowledge_Guards(t *testing.T) {
	s := &Service{}
	if _, err := s.SearchKnowledge(context.Background(), "x", 5); err == nil || !strings.Contains(err.Error(), "知识库未启用") {
		t.Errorf("nil Knowledge 应报未启用，got %v", err)
	}
	s2 := &Service{Knowledge: rag.NewNoop()}
	if _, err := s2.SearchKnowledge(context.Background(), "x", 5); err == nil || !strings.Contains(err.Error(), "知识库未启用") {
		t.Errorf("Noop 应报未启用，got %v", err)
	}
	fk := &fakeKnowledge{}
	s3 := &Service{Knowledge: fk}
	if _, err := s3.SearchKnowledge(context.Background(), "  ", 5); err == nil || !strings.Contains(err.Error(), "query 为空") {
		t.Errorf("空 query 应报错，got %v", err)
	}
}

// TestSearchKnowledge_Render 渲染形态：score/metadata/围栏、topK 钳制、空命中文案。
func TestSearchKnowledge_Render(t *testing.T) {
	fk := &fakeKnowledge{chunks: []rag.Chunk{
		{ID: "1", Score: 0.87, Content: "本仓库禁止在 for 循环里做 DB 查询", Metadata: map[string]string{"source": "team-rules.md"}},
		{ID: "2", Score: 0.41, Content: "历史误报：foo.go 的 nil 检查曾被判误报", Metadata: map[string]string{"repo": "acme/app"}},
	}}
	s := &Service{Knowledge: fk}
	out, err := s.SearchKnowledge(context.Background(), "循环里能不能查库", 99) // 超上界 → 钳 10
	if err != nil {
		t.Fatal(err)
	}
	if fk.topK != 10 {
		t.Errorf("topK 未钳到上界: %d", fk.topK)
	}
	for _, want := range []string{"search_knowledge（query=循环里能不能查库）命中 2 条", "score=0.870", "source=team-rules.md", "禁止在 for 循环里做 DB 查询", "search_knowledge 检索结果（数据）"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染缺 %q:\n%s", want, out)
		}
	}
	// topK 缺省
	if _, err := s.SearchKnowledge(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	if fk.topK != knowledgeTopKDefault {
		t.Errorf("topK 缺省应 %d, got %d", knowledgeTopKDefault, fk.topK)
	}
	// 空命中 → 提示文本非 error
	fk.chunks = nil
	out, err = s.SearchKnowledge(context.Background(), "没有的东西", 5)
	if err != nil || !strings.Contains(out, "知识库无命中") {
		t.Errorf("空命中应返回提示文本, out=%q err=%v", out, err)
	}
}

// topicEmbedder 主题桩：密钥/凭据族 → dim0，注入族 → dim1（模拟语义相近但
// 字面不同的检索能力，验证补位管路而非真实模型质量）。
type topicEmbedder struct{ calls int32 }

func (e *topicEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	atomic.AddInt32(&e.calls, 1)
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		v := []float32{0, 0}
		for _, w := range []string{"密钥", "凭据", "泄漏", "泄露", "硬编码"} {
			if strings.Contains(t, w) {
				v[0] = 1
			}
		}
		for _, w := range []string{"注入", "SQL", "拼接"} {
			if strings.Contains(t, w) {
				v[1] = 1
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (e *topicEmbedder) Dim() int { return 2 }

// TestSearchFindings_SemanticFallback 语义补位（v3.142.0）：关键词漏、语义中；
// 关闭开关时只走关键词。
func TestSearchFindings_SemanticFallback(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "sem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	up := func(file string, line int, comment string) {
		f := domain.Finding{File: file, Line: line, Severity: "high", Comment: comment, By: "s"}
		if r := st.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", time.Now(),
			[]domain.PrintUpsert{{FP: domain.Fingerprint(file, comment, line), Finding: f}}); r[0].Err != nil {
			t.Fatal(r[0].Err)
		}
	}
	up("db.go", 9, "硬编码的 API 密钥直接写入源码")
	up("db.go", 12, "SQL 注入：id 直接拼接进查询")

	emb := &topicEmbedder{}
	s := &Service{
		Cfg:       &config.Config{SemSearch: true},
		Store:     st,
		SemSearch: semsearch.NewManager(emb),
	}
	out, err := s.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "凭据泄漏", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[语义1]") {
		t.Fatalf("关键词漏应由语义补位:\n%s", out)
	}
	if !strings.Contains(out, "硬编码") || strings.Contains(out, "SQL 注入：") {
		t.Errorf("语义补位应命中密钥族、不牵连注入族:\n%s", out)
	}
	// 关闭语义 → 只走关键词（无命中提示）
	s2 := &Service{Cfg: &config.Config{SemSearch: false}, Store: st, SemSearch: semsearch.NewManager(emb)}
	out2, _ := s2.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "凭据泄漏", 5)
	if strings.Contains(out2, "[语义") {
		t.Errorf("开关关闭不应语义补位:\n%s", out2)
	}
	if !strings.Contains(out2, "无匹配") {
		t.Errorf("关闭后关键词无命中应提示:\n%s", out2)
	}
}
