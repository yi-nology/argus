package plugin

import (
	"context"
	"testing"
	"time"

	"git.example.com/enjoydream/agentkit/breaker"
)

func TestNormalizeSeverity(t *testing.T) {
	cases := map[string]string{
		"High": "high", "HIGH": "high", "critical": "high",
		"Medium": "medium", "warn": "medium",
		"low": "low", " info ": "low",
	}
	for in, want := range cases {
		got, ok := NormalizeSeverity(in)
		if got != want || !ok {
			t.Fatalf("%q → %q(ok=%v)，want %q", in, got, ok, want)
		}
	}
	got, ok := NormalizeSeverity("whatever")
	if got != SeverityLow || ok {
		t.Fatalf("越界值应归 low 且 ok=false，got %q ok=%v", got, ok)
	}
	if HigherSeverity("low", "high") != "high" || HigherSeverity("medium", "low") != "medium" {
		t.Fatal("冲突取高失败")
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Fingerprint("a.go", "这里 存在  SQL 注入！", 10)
	b := Fingerprint("a.go", "这里存在SQL注入", 10) // 空白/标点差异 → 同指纹
	c := Fingerprint("a.go", "完全不同的意见", 10)
	d := Fingerprint("b.go", "这里存在SQL注入", 10) // 文件不同 → 指纹不同
	if a != b {
		t.Fatal("规范化后应同指纹")
	}
	if a == c || a == d {
		t.Fatal("不同内容/文件不应同指纹")
	}
	// 行号不入指纹（跨轮次 ±2 容差语义）
	if Fingerprint("a.go", "x", 0) != Fingerprint("a.go", "x", 0) {
		t.Fatal("指纹不稳定")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"web/**", "web/src/a.vue", true},
		{"web/**", "web/a.vue", true},
		{"web/**", "src/web/a.vue", false},
		{"*.vue", "web/a.vue", true},
		{"*.go", "cmd/main.go", true},
		{"cmd/*.go", "cmd/main.go", true},
		{"cmd/*.go", "cmd/x/main.go", false},
		{"**/*.go", "a/b/c.go", true},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pat, c.path); got != c.want {
			t.Fatalf("GlobMatch(%q,%q)=%v want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestTriggerMatch(t *testing.T) {
	empty := Trigger{}
	if !empty.Match([]string{"x.go"}, nil, "") {
		t.Fatal("空 trigger 应恒命中")
	}
	tr := Trigger{Paths: []string{"web/**"}}
	if !tr.Match([]string{"web/a.js"}, nil, "") {
		t.Fatal("路径命中失败")
	}
	if tr.Match([]string{"go/a.go"}, nil, "") {
		t.Fatal("不应命中")
	}
}

// TestTriggerMatchEvents v3.0.1 修复回归：纯 Events 触发器此前恒 false 且调用方传空事件。
func TestTriggerMatchEvents(t *testing.T) {
	openOnly := Trigger{Events: []string{"cr.opened"}}
	if !openOnly.Match(nil, nil, "cr.opened") {
		t.Fatal("纯 events 触发器：白名单内事件应命中")
	}
	if openOnly.Match(nil, nil, "cr.synchronize") {
		t.Fatal("白名单外事件不应命中")
	}
	// CLI 手动任务（eventType=""）：事件条件无从过滤，视为不限制
	if !openOnly.Match(nil, nil, "") {
		t.Fatal("空事件类型（CLI 任务）应视为不限制")
	}
	// Events + Paths 混合：类型间 AND（events 白名单先过滤，paths 再过滤）
	mixed := Trigger{Events: []string{"cr.synchronize"}, Paths: []string{"docs/**"}}
	if mixed.Match([]string{"docs/a.md"}, nil, "cr.opened") {
		t.Fatal("混合条件：事件不在白名单应不触发（AND）")
	}
	if mixed.Match([]string{"go/a.go"}, nil, "cr.synchronize") {
		t.Fatal("混合条件：路径不命中应不触发")
	}
	if !mixed.Match([]string{"docs/a.md"}, nil, "cr.synchronize") {
		t.Fatal("混合条件：事件与路径都命中应触发")
	}
	if !mixed.Match([]string{"docs/a.md"}, nil, "") {
		t.Fatal("混合条件：空事件类型（CLI 任务）事件条件视为通过")
	}
	// Labels 命中
	byLabel := Trigger{Labels: []string{"ui"}}
	if !byLabel.Match(nil, []string{"ui"}, "") {
		t.Fatal("标签命中失败")
	}
}

func TestBreaker(t *testing.T) {
	bs := breaker.NewBreakers(0, 0)
	now := time.Now()
	bs.Now = func() time.Time { return now }

	name := "p"
	for i := 0; i < 3; i++ {
		if !bs.Allow(name) {
			t.Fatal("closed 应放行")
		}
		bs.Failure(name)
	}
	if bs.Allow(name) {
		t.Fatal("3 连败后应熔断")
	}
	now = now.Add(breaker.DefaultCooldown + time.Second)
	if !bs.Allow(name) {
		t.Fatal("冷却后应半开放行一个探测")
	}
	if bs.Allow(name) {
		t.Fatal("半开探测在途时不放行第二个")
	}
	bs.Success(name)
	if !bs.Allow(name) {
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

func TestMerger(t *testing.T) {
	fs := &fakeStore{prints: map[string]string{
		Fingerprint("dup.go", "同一问题", 3): "suppressed", // 预置：历史误报
	}}
	m := NewMerger(fs, testNopLogger{})
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
	if len(merged.Degraded) != 1 || merged.Degraded[0] == "" {
		t.Fatalf("降级注明缺失: %+v", merged.Degraded)
	}
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// v3.7.0：指纹 v2——行号入指纹（行不同即不同身份），规范化与稳定性不变。
func TestFingerprintV2_LineSensitive(t *testing.T) {
	a := Fingerprint("a.go", "这里存在SQL注入", 10)
	b := Fingerprint("a.go", "这里存在SQL注入", 200)
	if a == b {
		t.Fatal("行号不同应有不同指纹")
	}
	if a != Fingerprint("a.go", "这里 存在 SQL 注入！", 10) {
		t.Fatal("规范化后同评语同行应同指纹")
	}
	if a != Fingerprint("a.go", "这里存在SQL注入", 0) == false && a == Fingerprint("a.go", "这里存在SQL注入", 0) {
		t.Fatal("line=0 与 line=10 应不同")
	}
	if len(a) != 32 {
		t.Fatalf("指纹长度应与 v1 一致（32 hex），got %d", len(a))
	}
}
