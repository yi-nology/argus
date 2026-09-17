package requirement

import (
	"testing"

	"argus/internal/plugin"
)

// v3.12.0 G2 断点续审：checkpoint 助手纯函数测试（spec 回流/坏 JSON 容错/摘要复用/sink 落库）。

func TestCheckpointSpec(t *testing.T) {
	w := &Workflow{Log: nopLogger{}}
	if got := w.checkpointSpec(plugin.Task{}); got != nil {
		t.Fatalf("无 checkpoint 应返回 nil: %+v", got)
	}
	task := plugin.Task{Artifacts: map[string]string{plugin.ArtifactCkptR1Spec: `{"source":"issue #1","acceptance_criteria":[{"id":"AC-1","text":"登录"}]}`}}
	got := w.checkpointSpec(task)
	if got == nil || len(got.ACs) != 1 || got.ACs[0].ID != "AC-1" {
		t.Fatalf("checkpoint spec 回流失败: %+v", got)
	}
	bad := plugin.Task{Artifacts: map[string]string{plugin.ArtifactCkptR1Spec: "not-json"}}
	if got := w.checkpointSpec(bad); got != nil {
		t.Fatal("坏 JSON 应容错为 nil（全量重跑）")
	}
}

func TestCheckpointSummary(t *testing.T) {
	w := &Workflow{Log: nopLogger{}}
	if s, reused := w.checkpointSummary(plugin.Task{}); reused || s != "" {
		t.Fatalf("无 checkpoint 应 (\"\", false): %q %v", s, reused)
	}
	s, reused := w.checkpointSummary(plugin.Task{Artifacts: map[string]string{
		plugin.ArtifactCkptR2Summary: "新增登录"}})
	if !reused || s != "新增登录" {
		t.Fatalf("摘要复用失败: %q %v", s, reused)
	}
}

func TestCheckpointSave_NilSinkNoop(t *testing.T) {
	w := &Workflow{Log: nopLogger{}}
	w.checkpointSave(plugin.Task{}, map[string]string{plugin.ArtifactCkptR1Spec: "x"}) // 不 panic

	var captured map[string]string
	w2 := &Workflow{Log: nopLogger{}, CheckpointSink: func(a map[string]string) { captured = a }}
	w2.checkpointSave(plugin.Task{}, map[string]string{plugin.ArtifactCkptR1Spec: "x"})
	if captured == nil || captured[plugin.ArtifactCkptR1Spec] != "x" {
		t.Fatalf("sink 未收到产物: %+v", captured)
	}
}
