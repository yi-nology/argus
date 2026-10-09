package requirement

import (
	"testing"

	"argus/internal/plugin"
)

// TestOnStagePersistsProgress onStage 除 SSE 上报外还须把 stage_progress 落库
// （v3.132.0）：get_report 阶段推导的主判据来源。
func TestOnStagePersistsProgress(t *testing.T) {
	var got []map[string]string
	w := &Workflow{CheckpointSink: func(arts map[string]string) {
		got = append(got, arts)
	}}
	w.onStage(plugin.LayerR4)
	if len(got) != 1 {
		t.Fatalf("onStage 应恰落一次库，got %d", len(got))
	}
	if got[0][plugin.ArtifactStageProgress] != plugin.LayerR4 {
		t.Errorf("落库内容不符: %v", got[0])
	}
	// nil sink 安全
	w2 := &Workflow{}
	w2.onStage(plugin.LayerR1) // 不 panic 即过
}
