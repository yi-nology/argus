package config

import (
	"testing"
)

// TestDeriveDefaults_ContextTokenZero ContextToken=0 时回退128k，全部派生值不炸。
func TestDeriveDefaults_ContextTokenZero(t *testing.T) {
	c := &Config{LLM: LLMConfig{ContextToken: 0, MaxTokens: 0}, TaskTokenBudget: 0}
	if got := c.MaxDiffCharsResolved(); got < 10_000 {
		t.Fatalf("diff 下限保护失效: %d", got)
	}
	if got := c.MaxTokensResolved(); got < 4096 {
		t.Fatalf("输出下限保护失效: %d", got)
	}
	if got := c.EffectiveTaskTokenBudget(); got <= 0 {
		t.Fatalf("预算回退失效: %d", got)
	}
	if got := c.MaxCallInputChars(); got < 8_000 {
		t.Fatalf("输入兜底下限失效: %d", got)
	}
	if got := c.ReqBodyMaxChars(); got < 24_000 {
		t.Fatalf("需求 body 下限失效: %d", got)
	}
	if got := c.ReqIssueMaxChars(); got < 8_000 {
		t.Fatalf("需求 issue 下限失效: %d", got)
	}
	if got := c.GetFileMaxRunes(); got < 20_000 {
		t.Fatalf("get_file 下限失效: %d", got)
	}
	if got := c.GetIssueMaxRunes(); got < 8_000 {
		t.Fatalf("get_issue 下限失效: %d", got)
	}
	if got := c.GetDiffMaxRunes(); got < 10_000 {
		t.Fatalf("get_diff 下限失效: %d", got)
	}
}

// TestDerive1M_All 1M 窗口下派生值合理（不超窗口、不小到没用）。
func TestDerive1M_All(t *testing.T) {
	c := &Config{LLM: LLMConfig{ContextToken: 1_000_000, MaxTokens: 0}, TaskTokenBudget: 0}
	// diff:1.4M chars ≤1.6M（窗口×4×2 chars/token）
	if got := c.MaxDiffCharsResolved(); got != 1_400_000 {
		t.Fatalf("diff: want 1400000, got %d", got)
	}
	// 预算:600k
	if got := c.EffectiveTaskTokenBudget(); got != 600_000 {
		t.Fatalf("budget: want 600000, got %d", got)
	}
	// 输出:50k
	if got := c.MaxTokensResolved(); got != 50_000 {
		t.Fatalf("max_tokens: want 50000, got %d", got)
	}
	// 输入兜底:(1M-50k)×2×0.9=1.71M
	if got := c.MaxCallInputChars(); got != 1_710_000 {
		t.Fatalf("input_chars: want 1710000, got %d", got)
	}
}

// TestDeriveSmallWindow 小窗口（4k）下限保护：不炸、不输出0。
func TestDeriveSmallWindow(t *testing.T) {
	c := &Config{LLM: LLMConfig{ContextToken: 4000}, TaskTokenBudget: 0}
	if got := c.MaxDiffCharsResolved(); got != 10_000 { //4000*35/100*4=5600 < min 10000
		t.Fatalf("diff: want 5600, got %d", got)
	}
	if got := c.MaxTokensResolved(); got != 4096 { // min 4096
		t.Fatalf("max_tokens: want 4096, got %d", got)
	}
	if got := c.EffectiveTaskTokenBudget(); got != 2400 { //4000*60/100
		t.Fatalf("budget: want 2400, got %d", got)
	}
}

// TestDeriveExplicitOverride 显式 env 优先于派生。
func TestDeriveExplicitOverride(t *testing.T) {
	c := &Config{LLM: LLMConfig{ContextToken: 1_000_000, MaxTokens: 4096}, TaskTokenBudget: 100_000}
	if got := c.MaxTokensResolved(); got != 4096 {
		t.Fatalf("显式 MaxTokens 应覆盖: %d", got)
	}
	if got := c.EffectiveTaskTokenBudget(); got != 100_000 {
		t.Fatalf("显式 TaskTokenBudget 应覆盖: %d", got)
	}
}

// TestSingleFileLimit_Scales 单文件上限随总量缩放。
func TestSingleFileLimit_Scales(t *testing.T) {
	// singleFileLimit 在 context 包，这里测 config 派生值一致性
	c0 := &Config{LLM: LLMConfig{ContextToken: 128_000}}
	c1 := &Config{LLM: LLMConfig{ContextToken: 1_000_000}}
	// 小窗口 diff 上限应 < 大窗口
	if c0.MaxDiffCharsResolved() >= c1.MaxDiffCharsResolved() {
		t.Fatalf("小窗口 diff 应 < 大窗口: %d vs %d", c0.MaxDiffCharsResolved(), c1.MaxDiffCharsResolved())
	}
	// 验证公式：128k*35/100*4 = 179200
	if got := c0.MaxDiffCharsResolved(); got != 179_200 {
		t.Fatalf("128k diff: want179200, got %d", got)
	}
	// 1M*35/100*4=1400000
	if got := c1.MaxDiffCharsResolved(); got != 1_400_000 {
		t.Fatalf("1M diff: want1400000, got %d", got)
	}
}
