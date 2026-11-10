// embed_batch.go —— 字节预算批量包装（v6.1.2）：内部 Embedder 的 Embed 按字节
// 预算分批调用（单请求体 ≤8KB）。起因：产线网关（APISIX）对 /v1/embeddings
// 路由有请求体大小分流——实测 9KB 过、12KB 起 404「model does not exist」（
// 超限请求被分流到兜底路由，错误文案误导；直调小请求全绿、argus 10×2000
// rune≈21KB 恒死）。预算 8KB 留余量；每段 ≤2000 rune（≤6KB UTF-8，clipText
// 保证）恒可独占一请求，不截断不丢段。
package semsearch

import (
	"context"
	"fmt"
)

// embedByteBudget 单请求体字节预算（实测阈值 9KB 过/12KB 挂，取 8KB 留余量；
// 网关侧若日后放开，此包装仅退化为低效批量，仍正确）。
const embedByteBudget = 8 * 1024

// ByteBatchEmbedder 字节预算批量 Embedder（实现 Embedder，装配侧包装
// rag.OpenAIEmbedder 使用——bootstrap 与 runner 两装配口同 wrap）。
type ByteBatchEmbedder struct {
	inner Embedder
}

// NewByteBatchEmbedder 包装内部 Embedder（nil 安全：nil 直接透传原样返回）。
func NewByteBatchEmbedder(inner Embedder) Embedder {
	if inner == nil {
		return nil
	}
	return &ByteBatchEmbedder{inner: inner}
}

// Embed 按字节预算分批：累计字节数超预算前截断子批（每段至少 1 条，超预算
// 大段独占一请求），段序与向量序严格对齐。
func (b *ByteBatchEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); {
		n := 1
		size := len(texts[start])
		for start+n < len(texts) && size+len(texts[start+n]) <= embedByteBudget {
			size += len(texts[start+n])
			n++
		}
		vecs, err := b.inner.Embed(ctx, texts[start:start+n])
		if err != nil {
			return nil, err
		}
		if len(vecs) != n {
			return nil, fmt.Errorf("byte batch 返回数不符: got %d, want %d", len(vecs), n)
		}
		out = append(out, vecs...)
		start += n
	}
	return out, nil
}

// Dim 透传内部维度。
func (b *ByteBatchEmbedder) Dim() int { return b.inner.Dim() }
