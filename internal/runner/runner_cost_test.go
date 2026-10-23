package runner

import (
	"math"
	"testing"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/port"
)

// TestCacheAdjustedInput 缓存命中输入单价摊薄（v3.180.0 成本口径修正）。
// cached 是命中前缀缓存的 prompt token（cacheRatio=0.1 便宜得多）；
// 摊薄后 InputPerM = 全价×(uncached + cached×ratio)/prompt，token 计数保持真实。
func TestCacheAdjustedInput(t *testing.T) {
	const full = 100.0
	cases := []struct {
		name       string
		prompt     int
		cached     int
		cacheRatio float64
		want       float64
	}{
		{"无缓存命中=全价", 1000, 0, 0.1, full},
		{"prompt 为零=全价", 0, 0, 0.1, full},
		{"全命中 ratio 0.1 → 0.1×全价", 1000, 1000, 0.1, full * 0.1},
		{"半命中 ratio 0.1 → 0.55×全价", 1000, 500, 0.1, full * 0.55},
		{"ratio 1.0 不折扣=全价", 1000, 1000, 1.0, full},
		{"cached 超 prompt 钳回 → 全命中", 1000, 2000, 0.1, full * 0.1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheAdjustedInput(full, tc.prompt, tc.cached, tc.cacheRatio)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cacheAdjustedInput(%v,%d,%d,%v): want %v, got %v",
					full, tc.prompt, tc.cached, tc.cacheRatio, tc.want, got)
			}
		})
	}
}

// TestRecordPricedCachedExposesHitRatio 接上游 llm v0.17.0 的 RecordPricedCached：
// cached 落进 CostRecord.CachedTokens、CacheHitRatio() 可派生。缓存折扣仍由
// cacheAdjustedInput 在 Price 层摊薄（上游 Price 哲学：折扣由调用方表达）。
func TestRecordPricedCachedExposesHitRatio(t *testing.T) {
	ct := llm.NewCostTracker()
	const inputPer1K = 1.0
	// 一次调用：1000 prompt、400 命中缓存、0 completion。
	ratio := 0.1
	adj := cacheAdjustedInput(inputPer1K*1000, 1000, 400, ratio)
	price := port.Price{InputPerM: adj, OutputPerM: 0}
	ct.RecordPricedCached("m", "", 1000, 400, 0, price)

	recs := ct.Records()
	if recs[0].CachedTokens != 400 {
		t.Fatalf("cached 应落 CostRecord: got %d want 400", recs[0].CachedTokens)
	}
	// 成本反映缓存折扣：命中部分按 ratio 摊薄 → 等效 prompt = 600+400×0.1=640
	wantCost := 640.0 / 1e6 * (inputPer1K * 1000)
	if math.Abs(recs[0].CostUSD-wantCost) > 1e-9 {
		t.Fatalf("成本应含缓存折扣: got %v want %v", recs[0].CostUSD, wantCost)
	}
	// CacheHitRatio = Σcached/Σprompt = 400/1000
	s := ct.Summary()["m"]
	if math.Abs(s.CacheHitRatio()-0.4) > 1e-9 {
		t.Fatalf("命中率: got %v want 0.4", s.CacheHitRatio())
	}
}
