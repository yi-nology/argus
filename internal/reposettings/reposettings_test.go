package reposettings

import "testing"

func TestParseValid(t *testing.T) {
	s, err := Parse([]byte(`{"monthly_token_budget": 5000000, "task_token_budget": 800000, "max_diff_chars": 200000}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.MonthlyTokenBudget == nil || *s.MonthlyTokenBudget != 5_000_000 {
		t.Fatalf("monthly_token_budget = %v", s.MonthlyTokenBudget)
	}
	if s.TaskTokenBudget == nil || *s.TaskTokenBudget != 800_000 {
		t.Fatalf("task_token_budget = %v", s.TaskTokenBudget)
	}
	if s.MaxDiffChars == nil || *s.MaxDiffChars != 200_000 {
		t.Fatalf("max_diff_chars = %v", s.MaxDiffChars)
	}
	if s.IsZero() {
		t.Fatal("非空配置被判 IsZero")
	}
}

func TestParsePartialAndEmpty(t *testing.T) {
	s, err := Parse([]byte(`{"max_diff_chars": 100}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.MaxDiffChars == nil || s.MonthlyTokenBudget != nil {
		t.Fatal("部分字段解析错误")
	}
	empty, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("Parse 空对象: %v", err)
	}
	if !empty.IsZero() {
		t.Fatal("空对象应 IsZero")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"负数":   `{"max_diff_chars": -1}`,
		"零":    `{"monthly_token_budget": 0}`,
		"超上限":  `{"task_token_budget": 2000000000}`,
		"未知字段": `{"maxDiffChars": 100}`,
		"类型错误": `{"max_diff_chars": "100k"}`,
		"非对象":  `[]`,
		"尾部垃圾": `{"max_diff_chars": 100} extra`,
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Fatalf("%s: 应拒绝 %s", name, in)
		}
	}
}

func TestOrSemantics(t *testing.T) {
	// nil receiver 安全：无配置 = 全默认。
	var nilS *Settings
	if got := nilS.MonthlyTokenBudgetOr(7); got != 7 {
		t.Fatalf("nil MonthlyTokenBudgetOr = %d", got)
	}
	if got := nilS.MaxDiffCharsOr(9); got != 9 {
		t.Fatalf("nil MaxDiffCharsOr = %d", got)
	}
	ov := 42
	s := &Settings{TaskTokenBudget: &ov}
	if got := s.TaskTokenBudgetOr(7); got != 42 {
		t.Fatalf("覆盖未生效: %d", got)
	}
	if got := s.MonthlyTokenBudgetOr(7); got != 7 {
		t.Fatalf("未设置字段被覆盖: %d", got)
	}
}

func TestNormalizeRoundtrip(t *testing.T) {
	ov := 123
	s, err := Parse([]byte(`{"task_token_budget": 123}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b, err := s.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	rt, err := Parse(b)
	if err != nil {
		t.Fatalf("roundtrip Parse: %v", err)
	}
	if rt.TaskTokenBudget == nil || *rt.TaskTokenBudget != ov {
		t.Fatalf("roundtrip 丢值: %v", rt.TaskTokenBudget)
	}
	if string(b) != `{"task_token_budget":123}` {
		t.Fatalf("规范形态或 omitempty 收敛错误: %s", b)
	}
}
