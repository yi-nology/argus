package plugin

import "testing"

// v3.90.0：related 段解析与敌对输入钳制——合法声明透传；仓数/路径越界/
// repo 形状/路径穿越整份拒绝（LoadRepoConfig 降级为无配置+告警）。
func TestParseRepoConfigRelated(t *testing.T) {
	ok, err := ParseRepoConfig([]byte(`
related:
  - repo: acme/api
    ref: main
    paths: ["openapi.yaml", "proto/user.proto"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Related) != 1 || ok.Related[0].Repo != "acme/api" || len(ok.Related[0].Paths) != 2 {
		t.Fatalf("related 解析不符: %+v", ok.Related)
	}
	bad := []string{
		"related:\n  - repo: noslash\n    paths: [a]",
		"related:\n  - repo: a/b\n    paths: []",
		"related:\n  - repo: a/b\n    paths: ['../escape']",
		"related:\n  - repo: a/b\n    paths: ['/abs']",
	}
	for i, y := range bad {
		if _, err := ParseRepoConfig([]byte(y)); err == nil {
			t.Fatalf("敌对 related[%d] 应整份拒绝: %s", i, y)
		}
	}
}
