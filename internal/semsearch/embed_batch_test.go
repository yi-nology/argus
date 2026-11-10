package semsearch

import (
	"context"
	"strings"
	"testing"
)

// recEmbedder 记录每次调用的批量与字节数（字节预算断言用）。
type recEmbedder struct {
	calls    []int
	bytes    []int
	innerDim int
}

func (r *recEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	n := 0
	for _, t := range texts {
		n += len(t)
	}
	r.calls = append(r.calls, len(texts))
	r.bytes = append(r.bytes, n)
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = []float32{float32(len(r.calls))}
	}
	return vecs, nil
}

func (r *recEmbedder) Dim() int { return r.innerDim }

// TestByteBatchEmbedderBudget 每请求字节 ≤8KB 预算、段序/向量序对齐、超预算
// 大段独占一请求、nil 包装透传。
func TestByteBatchEmbedderBudget(t *testing.T) {
	if NewByteBatchEmbedder(nil) != nil {
		t.Fatal("nil 内件应透传 nil")
	}
	rec := &recEmbedder{innerDim: 1}
	emb := NewByteBatchEmbedder(rec)
	texts := []string{
		strings.Repeat("a", 3000), // 3KB
		strings.Repeat("b", 3000), // +3KB=6KB
		strings.Repeat("c", 3000), // +3KB=9KB 超 → 新批
		strings.Repeat("d", 9000), // 9KB 大段独占（clipText 保证产线 ≤6KB，此为行为钉子）
		strings.Repeat("e", 100),  // 与大段后新批
	}
	vecs, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("向量数 %d != 段数 %d", len(vecs), len(texts))
	}
	for i, b := range rec.bytes {
		if b > embedByteBudget && rec.calls[i] != 1 {
			t.Errorf("批 %d 超预算 %d 且非独占大段: %dB/%d 条", i, embedByteBudget, b, rec.calls[i])
		}
	}
	want := []int{2, 1, 1, 1} // 3+3KB 一批、3KB 一批、9KB 独占、100B 独占
	if len(rec.calls) != 4 || rec.calls[0] != 2 || rec.calls[1] != 1 || rec.calls[2] != 1 || rec.calls[3] != 1 {
		t.Errorf("批形不符: %v (want %v)", rec.calls, want)
	}
}
