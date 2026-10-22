package runner

import (
	"math"
	"testing"
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
