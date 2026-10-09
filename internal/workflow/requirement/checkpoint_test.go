package requirement

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/plugin"

	"git.example.com/enjoydream/agentkit/llm"
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
	w.checkpointSave(map[string]string{plugin.ArtifactCkptR1Spec: "x"}) // 不 panic

	var captured map[string]string
	w2 := &Workflow{Log: nopLogger{}, CheckpointSink: func(a map[string]string) { captured = a }}
	w2.checkpointSave(map[string]string{plugin.ArtifactCkptR1Spec: "x"})
	if captured == nil || captured[plugin.ArtifactCkptR1Spec] != "x" {
		t.Fatalf("sink 未收到产物: %+v", captured)
	}
}

// summaryWithCheckpoint 桩模型：计数 R2 LLM 调用，恒返回足长摘要（≥ r2MinValidRunes）。
type summaryCountingModel struct{ calls int }

func (m *summaryCountingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	return &schema.Message{Content: "本次变更重构了 dispatcher 的 multins 判定逻辑，统一了 fanout 与抓包使能的口径，并补充了回归测试覆盖。"}, nil
}

func (m *summaryCountingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// v3.49.0：R2 checkpoint 统一入口（Run 与 RunReviewOnly 共用）——未命中调 LLM 并
// 落库；命中零 LLM 零落库；重复调用复用不重跑。
func TestSummaryWithCheckpoint(t *testing.T) {
	var saved map[string]string
	gm := &summaryCountingModel{}
	w := &Workflow{
		LLM:            llm.NewClient(gm, "fake", nil),
		Log:            nopLogger{},
		CheckpointSink: func(arts map[string]string) { saved = arts },
	}
	task := plugin.Task{Diff: &plugin.DiffContext{Text: "diff --git a/x b/x"}}

	s, reused := w.summaryWithCheckpoint(context.Background(), task)
	if reused || s == "" || gm.calls != 1 {
		t.Fatalf("未命中应调一次 LLM 并返回摘要: reused=%v calls=%d s=%q", reused, gm.calls, s)
	}
	if saved == nil || saved[plugin.ArtifactCkptR2Summary] != s {
		t.Fatalf("成功后应落库 checkpoint: %+v", saved)
	}

	// 命中：模拟任务重试时 runner 从 DB 重载 StageArtifacts 进 Artifacts——
	// 命中后零 LLM、零落库、返回 checkpoint 内容
	task.Artifacts = map[string]string{plugin.ArtifactCkptR2Summary: s}
	saved = nil
	s2, reused2 := w.summaryWithCheckpoint(context.Background(), task)
	if !reused2 || s2 != s || gm.calls != 1 || saved != nil {
		t.Fatalf("命中应复用且不调 LLM: reused=%v s2=%q calls=%d saved=%v", reused2, s2, gm.calls, saved)
	}
}

// summaryErroringModel 恒返回错误的桩模型（R2 失败降级路径驱动器）。
type summaryErroringModel struct{ calls int }

func (m *summaryErroringModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	return nil, errors.New("boom")
}

func (m *summaryErroringModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

// LLM 失败：降级空摘要、不落 checkpoint（重试时 R2 可重来——checkpoint 缺失
// 只影响重试成本）。stage_progress 进度标记除外（onStage 进入即落，v3.132.0）。
func TestSummaryWithCheckpoint_DegradeOnLLMError(t *testing.T) {
	var saved map[string]string
	gm := &summaryErroringModel{}
	w := &Workflow{
		LLM: llm.NewClient(gm, "fake", nil),
		Log: nopLogger{},
		CheckpointSink: func(arts map[string]string) {
			for k, v := range arts {
				if k != plugin.ArtifactStageProgress {
					saved = map[string]string{k: v}
				}
			}
		},
	}
	s, reused := w.summaryWithCheckpoint(context.Background(),
		plugin.Task{Diff: &plugin.DiffContext{Text: "diff --git a/x b/x"}})
	if reused || s != "" || gm.calls < 1 || saved != nil {
		// calls 放宽为 >=1：llm.Client 自带重试，重试次数非本助手语义
		t.Fatalf("LLM 失败应降级且不落库: reused=%v s=%q calls=%d saved=%v", reused, s, gm.calls, saved)
	}
}
