// store_impact_test.go —— MergeTaskArtifacts 单测（v3.183.0）：影响面快照
// 即时落库的合并语义（保留既有键、patch 覆盖同名键）。
package storage

import (
	"context"
	"testing"

	"argus/internal/plugin"
)

func TestMergeTaskArtifacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := NewTask("code_review", "gitea", "o", "r", "7")
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// 首写：快照键与既有 diff 键并存
	if err := s.MergeTaskArtifacts(ctx, task.ID, map[string]string{
		"diff":                    "RAW_DIFF",
		plugin.ArtifactImpactView: `{"version":1}`,
	}); err != nil {
		t.Fatalf("首写: %v", err)
	}
	row, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	m := DecodeJSONMap(row.ArtifactsJSON)
	if m["diff"] != "RAW_DIFF" || m[plugin.ArtifactImpactView] != `{"version":1}` {
		t.Fatalf("首写合并失真: %v", m)
	}
	// 二写：同名键覆盖、其余保留
	if err := s.MergeTaskArtifacts(ctx, task.ID, map[string]string{
		plugin.ArtifactImpactView: `{"version":1,"risk":{"level":"high"}}`,
	}); err != nil {
		t.Fatalf("二写: %v", err)
	}
	row, _ = s.GetTask(ctx, task.ID)
	m = DecodeJSONMap(row.ArtifactsJSON)
	if m["diff"] != "RAW_DIFF" {
		t.Fatalf("二写丢既有键: %v", m)
	}
	if m[plugin.ArtifactImpactView] != `{"version":1,"risk":{"level":"high"}}` {
		t.Fatalf("同名键未覆盖: %v", m)
	}
	// 不存在任务：报错不panic
	if err := s.MergeTaskArtifacts(ctx, "no-such-id", map[string]string{"k": "v"}); err == nil {
		t.Fatal("不存在任务应报错")
	}
}
