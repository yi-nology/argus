// dispatcher_legacy_test.go —— 引擎侧历史测试（v3.173.0 自 plugin_test.go 随
// 引擎迁入：TestBreaker 熔断包装 + fakeStore + TestMerger 合并管线）。
package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oekit/concurrency/breaker"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
)

func TestBreaker(t *testing.T) {
	now := time.Now()
	// 旧写法 breaker.NewBreakers(0, 0) 里的 0 是「用缺省」（trip=3 / cooldown=5min），
	// 与上游语义**相反**：WithConsecutiveFailures(0) 是关闭计数触发、
	// WithCooldown(0) 是沿用 probeTimeout。照抄 0 会让本测试完全失去意义，
	// 所以必须显式写出真实参数。
	bs, ok := breaker.NewBreaker("argus.test.p",
		breaker.WithConsecutiveFailures(breakerTripThreshold),
		breaker.WithCooldown(breakerCooldown),
		breaker.WithBreakerClock(func() time.Time { return now }),
	).(breaker.GenBreaker)
	if !ok {
		t.Fatal("oekit/concurrency 未实现 GenBreaker 扩展接口")
	}

	allow := func() (uint64, error) { _, g, e := bs.AllowGen(context.Background()); return g, e }

	for i := 0; i < 3; i++ {
		gen, err := allow()
		if err != nil {
			t.Fatal("closed 应放行")
		}
		bs.FailureGen(context.Background(), gen)
	}
	if _, err := allow(); err == nil {
		t.Fatal("3 连败后应熔断")
	}
	now = now.Add(breakerCooldown + time.Second)
	probeGen, err := allow()
	if err != nil {
		t.Fatal("冷却后应半开放行一个探测")
	}
	if _, err := allow(); err == nil {
		t.Fatal("半开探测在途时不放行第二个")
	}
	bs.SuccessGen(probeGen)
	if _, err := allow(); err != nil {
		t.Fatal("探测成功后应闭合")
	}
}

// fakeStore Merger 测试桩。
type fakeStore struct {
	prints map[string]string // fp → status
}

func (f *fakeStore) GetPrintState(_ context.Context, _, _, _, _, _ string) (*PrintState, error) {
	return nil, nil
}

func (f *fakeStore) UpsertPrint(_ context.Context, _, _, _, _, fp string, _ Finding, _ string, _ time.Time) (bool, error) {
	st, ok := f.prints[fp]
	if ok && st == "suppressed" {
		return true, nil
	}
	f.prints[fp] = "open"
	return false, nil
}

func (f *fakeStore) UpsertPrints(_ context.Context, _, _, _, _, _ string,
	_ time.Time, items []domain.PrintUpsert) []domain.PrintUpsertResult {
	out := make([]domain.PrintUpsertResult, len(items))
	for i, it := range items {
		sup, _ := f.UpsertPrint(context.Background(), "", "", "", "", it.FP, it.Finding, "", time.Now())
		out[i] = domain.PrintUpsertResult{Suppressed: sup}
	}
	return out
}

func TestMerger(t *testing.T) {
	fs := &fakeStore{prints: map[string]string{
		Fingerprint("dup.go", "同一问题", 3): "suppressed", // 预置：历史误报
	}}
	m := NewMerger(fs, logx.Nop())
	outcomes := []Outcome{
		{
			Name: "correctness",
			Result: &Result{Findings: []Finding{
				{File: "a.go", Line: 10, Severity: "High", Comment: "空指针"},  // 大写 → 归一化
				{File: "a.go", Line: 12, Severity: "high", Comment: "空 指针"}, // 同指纹（±2 行容差）
				{File: "dup.go", Line: 3, Severity: "low", Comment: "同一问题"}, // 历史误报 → 抑制
			}},
		},
		{
			Name: "ocr",
			Result: &Result{Findings: []Finding{
				{File: "a.go", Line: 10, Severity: "medium", Comment: "空指针"}, // 跨插件同指纹 → severity 取高
				{File: "b.go", Line: 1, Severity: "weird", Comment: "越界词表"},  // 归 low
			}},
		},
		{ // 失败插件 → 降级注明
			Name: "broken", Err: context.DeadlineExceeded,
		},
	}
	merged, err := m.Merge(context.Background(), PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"},
		"task-1", outcomes, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Findings) != 2 {
		t.Fatalf("期望 2 条（同指纹合并+误报抑制），got %d: %+v", len(merged.Findings), merged.Findings)
	}
	first := merged.Findings[0]
	if first.Severity != SeverityHigh {
		t.Fatalf("冲突取高失败: %+v", first)
	}
	if first.By != "correctness,ocr" {
		t.Fatalf("by 合并失败: %q", first.By)
	}
	if merged.Findings[1].Severity != SeverityLow {
		t.Fatalf("越界词表应归 low: %+v", merged.Findings[1])
	}
	if merged.Suppressed != 1 {
		t.Fatalf("抑制计数错误: %d", merged.Suppressed)
	}
	// v3.8.0：Degraded 含 broken 未参与 + 词表外严重度注记，断言改为语义匹配
	foundBroken := false
	for _, d := range merged.Degraded {
		if strings.Contains(d, "broken 未参与") {
			foundBroken = true
		}
	}
	if !foundBroken {
		t.Fatalf("降级注明缺失: %+v", merged.Degraded)
	}
	// v3.8.0：词表外严重度必须有 Degraded 注记（此前只有日志零痕迹）
	foundSevNote := false
	for _, d := range merged.Degraded {
		if strings.Contains(d, "词表外严重度") {
			foundSevNote = true
		}
	}
	if !foundSevNote {
		t.Fatalf("词表外严重度应注入 Degraded: %+v", merged.Degraded)
	}
}

// v3.7.0：指纹 v2——行号入指纹（行不同即不同身份），规范化与稳定性不变。
