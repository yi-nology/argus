package adapter_cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/runtime/procx"
)

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

// plugins.yaml env 声明经 RunProcess 白名单透传的语义由 agentkit/acpx 上游测试
// 锁定（TestEnvAllowlist/TestRunProcess）；此处端到端钉一遍声明确实传到了底座。
func TestInvokeEnvWhitelistEndToEnd(t *testing.T) {
	t.Setenv("ARGUS_GITEA_TOKEN", "secret-token")
	t.Setenv("OPENAI_API_KEY", "sk-secret")

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ocr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$OPENAI_API_KEY\"; echo \"$ARGUS_GITEA_TOKEN\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, _, err := procx.Run(context.Background(), procx.RunRequest{
		Argv:    []string{script},
		Env:     []string{"OPENAI_API_KEY"}, // plugins.yaml env 声明形态
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "sk-secret") {
		t.Fatalf("声明白名单的变量应透传: %q", stdout)
	}
	if strings.Contains(stdout, "secret-token") {
		t.Fatalf("未声明的密钥变量不得继承: %q", stdout)
	}
}
