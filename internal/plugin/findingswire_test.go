package plugin

import (
	"strings"
	"testing"
)

func TestDecodeFindingsJSONBackCompat(t *testing.T) {
	// 旧形态输出（无 analyst 字段）零影响。
	w, err := DecodeFindingsJSON(`{"findings":[{"file":"a.go","line":3,"severity":"high","comment":"x"}],"notes":"n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Findings) != 1 || w.Findings[0].File != "a.go" {
		t.Fatalf("findings 解析漂移: %+v", w.Findings)
	}
	if w.Claims != nil || w.Design != nil || w.TestGaps != nil || w.OpenQuestions != nil {
		t.Fatal("旧输出应全部零值")
	}
}

func TestDecodeFindingsJSONAnalystFields(t *testing.T) {
	text := `{"findings":[],"notes":"","claims":[{"claim":"c1","evidence":"a.go:1","status":"undeclared"}],` +
		`"design":{"triggered":true,"object_graph":["g"],"cohesion":["ch"],"coupling":["cp"],"migration":"m"},` +
		`"test_gaps":["tg"],"open_questions":["oq"]}`
	w, err := DecodeFindingsJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Claims) != 1 || w.Claims[0].Status != ClaimUndeclared {
		t.Fatalf("claims 解析: %+v", w.Claims)
	}
	if w.Design == nil || !w.Design.Triggered || w.Design.Migration != "m" {
		t.Fatalf("design 解析: %+v", w.Design)
	}
	if len(w.TestGaps) != 1 || len(w.OpenQuestions) != 1 {
		t.Fatalf("gaps/questions 解析: %+v / %+v", w.TestGaps, w.OpenQuestions)
	}
	// domain.Finding.chain 字段透传（analyst 模式下 R4 产出）。
	w2, err := DecodeFindingsJSON(`{"findings":[{"file":"a.go","line":1,"severity":"high","comment":"c","chain":"消费侧 b.go:9 依赖旧语义"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Findings[0].Chain == "" {
		t.Fatal("chain 字段未透传")
	}
}

func TestNormalizeClaimStatus(t *testing.T) {
	for _, ok := range []string{"implemented", "PARTIAL", " missing ", ClaimUndeclared, ClaimInconsistent} {
		if _, valid := NormalizeClaimStatus(ok); !valid {
			t.Fatalf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"", "done", "yes", "implementedd"} {
		if s, valid := NormalizeClaimStatus(bad); valid || s != "" {
			t.Fatalf("%q 应拒", bad)
		}
	}
}

func TestSanitizeAnalyst(t *testing.T) {
	// 条数钳制 + 截断留痕。
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, strings.Repeat("x", 600))
	}
	w := &FindingsWire{TestGaps: many, OpenQuestions: many}
	sec := SanitizeAnalyst(w)
	if len(sec.TestGaps) != analystMaxItems {
		t.Fatalf("test_gaps 钳制后 %d 条, want %d", len(sec.TestGaps), analystMaxItems)
	}
	if !strings.Contains(sec.TestGaps[analystMaxItems-1], "（截断）") {
		t.Fatal("截断须留痕")
	}
	if len([]rune(sec.TestGaps[0])) > analystMaxItemRunes+10 {
		t.Fatalf("单条 runes 未钳: %d", len([]rune(sec.TestGaps[0])))
	}

	// 非法状态声明整条丢弃。
	w2 := &FindingsWire{Claims: []Claim{
		{Claim: "c", Evidence: "e", Status: "bogus"},
		{Claim: "c2", Evidence: "e2", Status: "implemented"},
	}}
	sec2 := SanitizeAnalyst(w2)
	if len(sec2.Claims) != 1 || sec2.Claims[0].Claim != "c2" {
		t.Fatalf("非法状态未丢弃: %+v", sec2.Claims)
	}

	// 全空输入 → 零值段（渲染 N=0 省略）。
	sec3 := SanitizeAnalyst(&FindingsWire{})
	if sec3.Claims != nil || sec3.Design != nil || sec3.TestGaps != nil || sec3.OpenQuestions != nil {
		t.Fatalf("空输入应零值: %+v", sec3)
	}

	// design 未触发也原样携带（渲染层按 Triggered 决定省略）。
	w4 := &FindingsWire{Design: &Design{Triggered: false}}
	if sec4 := SanitizeAnalyst(w4); sec4.Design == nil || sec4.Design.Triggered {
		t.Fatalf("design 未触发形态: %+v", sec4.Design)
	}
}
