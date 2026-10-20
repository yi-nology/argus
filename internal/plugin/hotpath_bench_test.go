package plugin

// 热路径基准（v3.25.1）：指纹与 best-of-N 聚簇是 Merger/R4 的每意见必经点
// （Fingerprint 由评语派生、跨轮身份依赖它；Aggregate 供 reviewN 聚簇）。

import (
	"fmt"
	"testing"

	reportutil "github.com/oagentkit/textx/reportutil"
)

// BenchmarkFingerprint 指纹派生（SHA256 + 评语规范化）——每条意见每轮 ≥1 次。
func BenchmarkFingerprint(b *testing.B) {
	comment := "并发写共享 map 未加锁：func set(k string, v int) { cache[k] = v } 无互斥保护，建议 sync.RWMutex"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Fingerprint("internal/plugin/merger.go", comment, 42)
	}
}

// BenchmarkAggregateSamples3x50 best-of-N 形态：3 采样 × 各 50 条意见聚合
// （签名双通道 + eq 行号相容判定）。
func BenchmarkAggregateSamples3x50(b *testing.B) {
	findings := make([]Finding, 0, 150)
	for s := 0; s < 3; s++ {
		for i := 0; i < 50; i++ {
			findings = append(findings, Finding{
				File:     fmt.Sprintf("pkg/file%02d.go", i%20),
				Line:     10 + i,
				Severity: SeverityMedium,
				Comment:  fmt.Sprintf("第 %d 类问题：错误被吞且未包装上下文（采样 %d）", i%10, s),
			})
		}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = reportutil.Aggregate(findings,
			func(f Finding) []string {
				return []string{
					"fp:" + Fingerprint(f.File, f.Comment, f.Line),
					"nc:" + f.File + "|" + reportutil.NormalizeComment(f.Comment),
				}
			},
			func(a, b Finding) bool { return a.Line == b.Line },
		)
	}
}
