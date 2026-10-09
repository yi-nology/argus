package config

import "testing"

// TestParseAPITokens_RepoScope token:scope@repo-pattern 解析（v3.135.0）：
// 带 @ 的条目携带仓库约束；无 @ 保持原语义。
func TestParseAPITokens_RepoScope(t *testing.T) {
	toks := ParseAPITokens("abc:ci@gitlab:obs/*,def:read,ghi:admin@github:acme/app")
	if len(toks) != 3 {
		t.Fatalf("解析条数 = %d, want 3: %+v", len(toks), toks)
	}
	if toks[0].Token != "abc" || toks[0].Scope != "ci" || toks[0].Repos != "gitlab:obs/*" {
		t.Errorf("带 pattern 条目解析错: %+v", toks[0])
	}
	if toks[1].Repos != "" {
		t.Errorf("无 pattern 条目 Repos 应空: %+v", toks[1])
	}
	if toks[2].Repos != "github:acme/app" {
		t.Errorf("精确 pattern 解析错: %+v", toks[2])
	}
	// @ 后为空 = 坏条目跳过
	if got := ParseAPITokens("abc:ci@"); len(got) != 0 {
		t.Errorf("空 pattern 应跳过: %+v", got)
	}
}

// TestParseAPITokens_Expiry 过期时间解析（v3.136.0）：#YYYY-MM-DD（当日截止）/
// RFC3339/坏格式条目跳过。
func TestParseAPITokens_Expiry(t *testing.T) {
	toks := ParseAPITokens("a:ci#2099-01-01,b:read@github:o/r#2099-06-01T12:00:00Z,c:admin#notadate")
	if len(toks) != 2 {
		t.Fatalf("坏格式应跳过: %+v", toks)
	}
	if toks[0].Expires.IsZero() || toks[0].Expires.Year() != 2099 {
		t.Errorf("日期解析错: %+v", toks[0].Expires)
	}
	if toks[1].Repos != "github:o/r" || toks[1].Expires.IsZero() {
		t.Errorf("@+# 组合解析错: %+v", toks[1])
	}
}
