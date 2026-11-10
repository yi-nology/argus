package service

import (
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// TestDeriveStageDone_ProgressMarker stage_progress 主判据：标记之前的全 done、
// 标记本身进行中——涵盖 RunReviewOnly 跳过 R1/R3/R35 的场景（此前被当"没做到"，
// 轮询恒显示 R1，2026-10-09 实测 21 采样全是 R1）。
func TestDeriveStageDone_ProgressMarker(t *testing.T) {
	// code_review 运行到 R4：R2 checkpoint 在、R4 onStage 已发
	arts := map[string]string{
		plugin.ArtifactCkptR2Summary: "摘要",
		plugin.ArtifactStageProgress: plugin.LayerR4,
	}
	done := deriveStageDone(arts, map[string]string{}, "", "", storage.StatusRunning)
	for _, st := range []string{plugin.LayerR1, plugin.LayerR2, plugin.LayerR3, plugin.LayerR35} {
		if !done[st] {
			t.Errorf("进度=R4 时 %s 应 done（含跳过阶段）", st)
		}
	}
	if done[plugin.LayerR4] {
		t.Error("R4 为当前阶段，不应 done")
	}
	if done[plugin.LayerR5] {
		t.Error("R5 未到不应 done")
	}
	// R5 标记 = 全链完成（R5 进行中至终态翻转）
	done = deriveStageDone(map[string]string{plugin.ArtifactStageProgress: plugin.LayerR5}, nil, "", "", storage.StatusRunning)
	if !done[plugin.LayerR4] || done[plugin.LayerR5] {
		t.Error("进度=R5 时 R4 应 done、R5 不应 done")
	}
	if !done[plugin.LayerR1] {
		t.Error("进度=R5 时 R1 应 done")
	}
}

// TestDeriveStageDone_LegacyFallback 无标记老任务走产物规则 + 终态翻转。
func TestDeriveStageDone_LegacyFallback(t *testing.T) {
	arts := map[string]string{plugin.ArtifactCkptR1Spec: "{}", plugin.ArtifactCkptR2Summary: "s"}
	debug := map[string]string{plugin.DebugKeyR3Checks: "[]"}
	done := deriveStageDone(arts, debug, `{"f":[]}`, "approve", storage.StatusRunning)
	for _, st := range []string{plugin.LayerR1, plugin.LayerR2, plugin.LayerR3, plugin.LayerR4, plugin.LayerR5} {
		if !done[st] {
			t.Errorf("产物规则下 %s 应 done", st)
		}
	}
	if done[plugin.LayerR35] {
		t.Error("无 R3.5 产物且未终态，R35 不应 done")
	}
	done = deriveStageDone(nil, nil, "", "", storage.StatusDone)
	for _, st := range plugin.StageTimeline {
		if !done[st] {
			t.Errorf("终态 done 应全链 done，%s 漏", st)
		}
	}
}
