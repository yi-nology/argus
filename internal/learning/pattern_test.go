package learning

import (
	"strings"
	"testing"
)

// 门面冒烟：确定性数学/键派生已下沉 agentkit reportutil（行为测试在 SDK 侧），
// 此处钉门面接线与 FirstBy 记账键语义。
func TestPatternKeyFacade(t *testing.T) {
	base := "未检查 error 返回，可能导致后续逻辑使用空值"
	withNoise := base + " `handler.go:12` internal/foo/bar.go abcdef012345 42"
	if PatternKey("security", base) != PatternKey("security", withNoise) {
		t.Fatal("噪声不应改变 pattern_key")
	}
	if PatternKey("security", base) == PatternKey("style", base) {
		t.Fatal("不同 plugin 不得共享键")
	}
	s := PatternSample("  未处理的错误返回：`db.Ping()` 失败 99 ")
	if s == "" || strings.Contains(s, "`") || len([]rune(s)) > PatternTextMax {
		t.Fatalf("sample 门面不符: %q", s)
	}
}

func TestConfidenceFacade(t *testing.T) {
	if c := Confidence(0, 0, 0, 0); c < 0.49 || c > 0.51 {
		t.Fatalf("先验应为 0.5, got %v", c)
	}
	if Confidence(0, 2, 1, 0) >= 0.35 {
		t.Fatal("regret2+suppress1 应低于 soft floor")
	}
	if PatternSamples(1, 2, 1) != 4 {
		t.Fatal("samples 门槛不符")
	}
	// 成对循环不洗白（v3.8.0 在案回归，随下沉在 SDK 侧详测）
	if Confidence(1, 0, 1, 1) >= Confidence(1, 0, 0, 0) {
		t.Fatal("suppress+restore 成对循环应单调不升")
	}
}

func TestFirstBy(t *testing.T) {
	cases := map[string]string{
		"sec,aux":   "sec",
		"sec":       "sec",
		"a,b,c":     "a",
		"":          "",
		"trailing,": "trailing",
	}
	for in, want := range cases {
		if got := FirstBy(in); got != want {
			t.Errorf("FirstBy(%q)=%q want %q", in, got, want)
		}
	}
}
