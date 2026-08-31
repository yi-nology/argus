package adapter_cli

import (
	"testing"
	"time"

	"argus/internal/plugin"
)

func pluginManifest() plugin.AgentManifest {
	return plugin.AgentManifest{Name: "ocr", Kind: plugin.KindCLI, Timeout: 5 * time.Second}
}

// 干净 PR 的合法"零意见"输出（[] 或 {"comments":[]}）不再是错误——
// 此前连续 3 个干净 PR 会把 cli 插件熔断摘除 5 分钟。
func TestParseFindingsEmptyIsLegit(t *testing.T) {
	for _, in := range []string{"[]", `{"comments":[]}`, `{"data":{"comments":[]}}`} {
		findings, err := ParseFindings(in)
		if err != nil {
			t.Fatalf("%s 应为合法零结果，got err=%v", in, err)
		}
		if len(findings) != 0 {
			t.Fatalf("%s 应解析出 0 条，got %d", in, len(findings))
		}
	}
	// 结构不符/空输出仍然是错误
	if _, err := ParseFindings(""); err == nil {
		t.Fatal("空输出应报错")
	}
	if _, err := ParseFindings("not json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// childEnv 只给白名单变量，绝不继承密钥类环境。
func TestChildEnvMinimal(t *testing.T) {
	t.Setenv("ARGUS_GITEA_TOKEN", "secret-token")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("PATH", "/usr/bin")
	p := New(pluginManifest(), []string{"/bin/true"}, "full", []string{"OPENAI_API_KEY"}, nil, nil)
	env := p.childEnv()
	joined := ""
	for _, kv := range env {
		joined += kv + "\n"
	}
	if !containsEnv(env, "PATH=/usr/bin") {
		t.Fatal("PATH 应保留")
	}
	if !containsEnv(env, "OPENAI_API_KEY=sk-secret") {
		t.Fatal("显式白名单变量应透传")
	}
	if containsEnv(env, "ARGUS_GITEA_TOKEN=secret-token") {
		t.Fatal("未声明白名单的密钥变量不得继承")
	}
	_ = joined
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
