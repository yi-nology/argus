package reporttmpl

import (
	"strings"
	"testing"
)

func TestKeysSortedVocab(t *testing.T) {
	want := []string{Analyst, Minimal, Standard}
	got := Keys()
	if len(got) != len(want) {
		t.Fatalf("Keys() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys()[%d] = %q, want %q（词表顺序即文档枚举顺序，勿乱）", i, got[i], want[i])
		}
	}
	// 与 registry 一致性：Keys 覆盖且仅覆盖 registry 键。
	if len(registry) != len(got) {
		t.Fatalf("registry %d 键 vs Keys %d 个——新增模板须同步两者", len(registry), len(got))
	}
	for _, k := range got {
		if _, ok := registry[k]; !ok {
			t.Fatalf("Keys() 含 registry 外键 %q", k)
		}
	}
}

func TestLookupAndIsValid(t *testing.T) {
	for _, k := range Keys() {
		if !IsValid(k) {
			t.Fatalf("IsValid(%q) = false", k)
		}
		tpl, ok := Lookup(k)
		if !ok || tpl.Key != k {
			t.Fatalf("Lookup(%q) = %+v, %v", k, tpl, ok)
		}
	}
	for _, bad := range []string{"", "custom", "STANDARD", "analyst ", "minimal\n"} {
		if IsValid(bad) {
			t.Fatalf("IsValid(%q) = true，非法键须拒", bad)
		}
		if _, ok := Lookup(bad); ok {
			t.Fatalf("Lookup(%q) 命中，非法键须拒", bad)
		}
	}
}

func TestSynthesisPromptOnlyForAnalyst(t *testing.T) {
	if p := AnalystSynthesisPrompt(Analyst); p == "" {
		t.Fatal("analyst 综合段提示词为空")
	} else if len([]rune(p)) > 3000 {
		t.Fatalf("analyst 综合段提示词 %d runes，超 3000 上限（提示词体积纪律）", len([]rune(p)))
	}
	for _, k := range []string{Standard, Minimal, ""} {
		if p := AnalystSynthesisPrompt(k); p != "" {
			t.Fatalf("%q 模板不应有综合段提示词，got %d bytes", k, len(p))
		}
	}
	// schema 与纪律关键锚点钉住（防手滑删段）。
	p := AnalystSynthesisPrompt(Analyst)
	for _, anchor := range []string{"claims", "design", "test_gaps", "open_questions",
		"undeclared", "inconsistent", "triggered", "如果只做一件事"} {
		if !strings.Contains(p, anchor) {
			t.Fatalf("综合段提示词缺锚点 %q", anchor)
		}
	}
}

func TestChainInstructionShape(t *testing.T) {
	if !strings.Contains(ChainInstruction, "\"chain\"") {
		t.Fatal("chain 指令须含字段名锚点")
	}
	// 指令必须是条件性追加（自带前缀换行，便于直接拼在纪律段后）。
	if !strings.HasPrefix(ChainInstruction, "\n") {
		t.Fatal("ChainInstruction 须以换行开头（拼接纪律）")
	}
}

func TestResolvePriority(t *testing.T) {
	cases := []struct {
		name          string
		repo, service string
		want          string
	}{
		{"repo 优先", Analyst, Minimal, Analyst},
		{"repo 空取 service", "", Minimal, Minimal},
		{"全空回退 standard", "", "", Standard},
		{"repo 非法回退 service", "bogus", Analyst, Analyst},
		{"全非法回退 standard", "bogus", "nope", Standard},
		{"repo minimal service analyst 各归位", Minimal, Analyst, Minimal},
	}
	for _, c := range cases {
		if got := Resolve(c.repo, c.service); got != c.want {
			t.Fatalf("%s: Resolve(%q,%q) = %q, want %q", c.name, c.repo, c.service, got, c.want)
		}
	}
}
