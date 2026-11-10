package adapter_builtin

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

// v3.52.0：跨轮一致性段只在 ArtifactPrevFindings 存在时出现，并回指路径纪律。
func TestGuidelinesPrompt_PrevFindings(t *testing.T) {
	if out := guidelinesPrompt(plugin.Task{}); strings.Contains(out, "上轮已提出的意见") {
		t.Fatalf("无历史意见不应出现该段: %q", out)
	}
	task := plugin.Task{Artifacts: map[string]string{
		plugin.ArtifactPrevFindings: "- src/a.go:10 [MEDIUM] 意见一",
	}}
	out := guidelinesPrompt(task)
	if !strings.Contains(out, "上轮已提出的意见") || !strings.Contains(out, "意见一") {
		t.Fatalf("应含跨轮段与内容: %q", out)
	}
	if !strings.Contains(out, "路径纪律") {
		t.Fatalf("应回指路径纪律: %q", out)
	}
}
