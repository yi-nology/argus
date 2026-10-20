package service

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/rag"
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
