package requirement

// R3 失效降级路径单测（v3.27.0）：模型输出两次（原轮 + 解析失败重试轮）均不可
// 解析 → 全部 AC 置 unverifiable → R5.3 人工确认，结果不丢。该路径此前零覆盖，
// golden harness 亦不可达（mockLLM 命名 marker case 优先于 persona）——本测试
// 补上单测层，golden 侧由 garble_r3 开关承接（夹具 53）。

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/plugin"

	"git.example.com/enjoydream/agentkit/llm"
)

// garbageModel 恒返回不可解析文本的桩模型（R3 两轮全败的驱动器）。
type garbageModel struct{ calls int }

func (g *garbageModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	g.calls++
	return &schema.Message{Content: "not-json-garbage"}, nil
}

func (g *garbageModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

func TestCheckComplianceDegradedOnUnparseable(t *testing.T) {
	gm := &garbageModel{}
	w := &Workflow{
		LLM: llm.NewClient(gm, "fake", nil),
		Log: nopLogger{},
	}
	spec := &plugin.RequirementSpec{
		Source: "PR 描述",
		ACs: []plugin.AcceptanceCriterion{
			{ID: "AC-1", Text: "验收点 1"},
			{ID: "AC-2", Text: "验收点 2"},
		},
	}
	task := plugin.Task{PR: plugin.PRContext{HeadBranch: "feature/x"}}

	report, final, err := w.CheckCompliance(context.Background(), task, spec, "摘要")
	if err != nil {
		t.Fatalf("降级路径不应返回错误（结果不丢）: %v", err)
	}
	if final != "" {
		t.Fatalf("降级路径 final 应为空，实得 %q", final)
	}
	if report == nil {
		t.Fatal("降级报告不应为 nil")
	}
	if !strings.Contains(report.Summary, "R3 判定失败") || !strings.Contains(report.Summary, "已全部降级为需人工确认") {
		t.Fatalf("Summary 应标明降级: %q", report.Summary)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("两条 AC 均应保留: %d", len(report.Checks))
	}
	for _, c := range report.Checks {
		if c.Status != plugin.ACUnverifiable {
			t.Fatalf("AC %s 应降级为 unverifiable，实得 %s", c.ID, c.Status)
		}
		if !strings.Contains(c.Detail, "自动判定不可用") {
			t.Fatalf("AC %s Detail 应说明人工确认: %q", c.ID, c.Detail)
		}
	}
	// 降级报告驱动真值表必须落 R5.3 comment（unverifiable>0 → 人工确认），不许 APPROVE
	verdict, rule := Verdict(spec, report, nil)
	if verdict != VerdictComment || rule != "R5.3" {
		t.Fatalf("降级报告应落 R5.3 comment，实得 %s/%s", verdict, rule)
	}
	if gm.calls == 0 {
		t.Fatal("模型应被实际调用")
	}
}
