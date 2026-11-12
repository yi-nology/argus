package commentcmd

// ExtractTemplateFlag 词法单测（v6.11.0）：两种形态/缺值/重复/词表不越界
//（词表归 service 层 reporttmpl.IsValid，本包只做形状）。

import "testing"

func TestExtractTemplateFlag(t *testing.T) {
	cases := []struct {
		name          string
		rest          string
		wantKey       string
		wantRemainder string
		wantFound     bool
		wantBad       bool
	}{
		{"空格形态", "--template analyst 重点关注并发", "analyst", "重点关注并发", true, false},
		{"等号形态", "--template=minimal", "minimal", "", true, false},
		{"旗标在尾部", "重点关注并发 --template analyst", "analyst", "重点关注并发", true, false},
		{"无旗标原样穿透", "重点关注并发", "", "重点关注并发", false, false},
		{"缺值 bad", "--template", "", "", true, true},
		{"缺值后接另一旗标 bad", "--template --verbose", "", "", true, true},
		{"重复旗标 bad", "--template analyst --template minimal", "", "", true, true},
		{"等号空值 bad", "--template=", "", "", true, true},
	}
	for _, c := range cases {
		key, rem, found, bad := ExtractTemplateFlag(c.rest)
		if key != c.wantKey || rem != c.wantRemainder || found != c.wantFound || bad != c.wantBad {
			t.Fatalf("%s: got (%q,%q,%v,%v), want (%q,%q,%v,%v)",
				c.name, key, rem, found, bad, c.wantKey, c.wantRemainder, c.wantFound, c.wantBad)
		}
	}
}

// 词表不越界守卫生成测试：本包不得 import reporttmpl（保持零依赖）——
// 词表校验在 service 消费端，形状歧义（bad）才在本层截停。
func TestExtractTemplateFlagNoVocabValidation(t *testing.T) {
	key, rem, found, bad := ExtractTemplateFlag("--template bogus_value")
	if !found || bad || key != "bogus_value" || rem != "" {
		t.Fatalf("非词表值应形状层放行（校验归 service）: (%q,%q,%v,%v)", key, rem, found, bad)
	}
}
