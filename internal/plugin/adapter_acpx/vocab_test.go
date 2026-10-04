package adapter_acpx

import "testing"

// TestAcpxAgentVocabulary 钉 argus 与 agentkit/acpx 的词表对齐：构造走 SDK
// Registry 单源后全量名可用；bins 探测词表与 SDK 真实 CLI 一致——minimax 曾
// 手抄为不存在的 minimax-code（agentkit v0.13.0 起真实 CLI 是 mcode），删
// vendor 后 replace 转活才暴露。
func TestAcpxAgentVocabulary(t *testing.T) {
	for _, name := range []string{
		"claude", "zcode", "codex", "opencode", "minimax",
		"kimi", "gemini", "qwen", "mimo", "pi", "dsh",
	} {
		if _, ok := newAgent(name); !ok {
			t.Errorf("SDK Registry 缺省集应含 %q", name)
		}
	}
	if _, ok := newAgent("no_such_agent"); ok {
		t.Error("未注册名不应返回 agent")
	}
	if bins["minimax"] != "mcode" {
		t.Errorf("minimax CLI 应为 mcode（minimax-code 自发布起不存在），got %q", bins["minimax"])
	}
	for name, bin := range bins {
		if bin == "" {
			t.Errorf("bins[%q] 为空", name)
		}
	}
}
