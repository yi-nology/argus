package requirement

// R3 失效降级路径单测（v3.27.0）：模型输出两次（原轮 + 解析失败重试轮）均不可
// 解析 → 全部 AC 置 unverifiable → R5.3 人工确认，结果不丢。该路径此前零覆盖，
// golden harness 亦不可达（mockLLM 命名 marker case 优先于 persona）——本测试
// 补上单测层，golden 侧由 garble_r3 开关承接（夹具 53）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/plugin"

	"github.com/oagentkit/llm"
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
	verdict, rule, _ := VerdictScoped(spec, report, nil, nil)
	if verdict != VerdictComment || rule != "R5.3" {
		t.Fatalf("降级报告应落 R5.3 comment，实得 %s/%s", verdict, rule)
	}
	if gm.calls == 0 {
		t.Fatal("模型应被实际调用")
	}
}

// rateLimitModel 恒返回 429 的桩：驱动 R3 运行中断降级路径（v5.5.3 死因透传）。
type rateLimitModel struct{ calls int }

func (m *rateLimitModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	return nil, errors.New("failed to create chat completion: error, status code: 429, status: 429 Too Many Requests, message: per-model RPM 超限")
}

func (m *rateLimitModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("status code: 429, Too Many Requests")
}

// TestCheckComplianceDegradedCarriesRunCause R3 运行中断（限流）：降级报告必须
// 写明真实死因与"重跑可救"，不得洗白成"模型输出不可解析"（MR186 实弹教训）。
func TestCheckComplianceDegradedCarriesRunCause(t *testing.T) {
	m := &rateLimitModel{}
	w := &Workflow{
		LLM: llm.NewClient(m, "fake", nil),
		Log: nopLogger{},
	}
	spec := &plugin.RequirementSpec{
		Source: "PR 描述",
		ACs:    []plugin.AcceptanceCriterion{{ID: "AC-1", Text: "验收点 1"}},
	}
	task := plugin.Task{PR: plugin.PRContext{HeadBranch: "feature/x"}}
	report, _, err := w.CheckCompliance(context.Background(), task, spec, "摘要")
	if err != nil {
		t.Fatalf("降级路径不应返回错误: %v", err)
	}
	if !strings.Contains(report.Summary, "上游限流 429") {
		t.Fatalf("Summary 应含真实死因: %q", report.Summary)
	}
	for _, c := range report.Checks {
		if !strings.HasPrefix(c.Detail, r3RuntimeFailPrefix) {
			t.Fatalf("Detail 应带运行中断前缀供 completeness 对症建议: %q", c.Detail)
		}
		if !strings.Contains(c.Detail, "重跑") {
			t.Fatalf("Detail 应提示重跑可救: %q", c.Detail)
		}
	}
	// completeness 对症：全量运行中断时不得说"拆分 PR"
	rep := JudgeCompleteness(nil, nil, nil, task, spec, report)
	msg := ""
	for _, item := range rep.Items {
		if item.Check == plugin.CheckUnverifiableRatio {
			if item.OK {
				t.Fatal("100% unverifiable 应 fail")
			}
			msg = item.Detail
		}
	}
	if msg == "" {
		t.Fatal("CheckUnverifiableRatio 应落项")
	}
	if !strings.Contains(msg, "重跑") || strings.Contains(msg, "建议拆分") {
		t.Fatalf("运行中断场景建议应指向重跑而非拆 PR: %q", msg)
	}
}

// TestCheckComplianceBudgetGateSkipsRetryCarriesCause 预算门在真实约束区间
// （Used≤2×Limit）拦下自救重试时，降级报告必须诚实写明"预算门拦下重试+调参路标"
// （v5.5.4：MR186 实弹——门拦重试省下零头、丢掉全部判定，还把死因埋进旧文案）。
func TestCheckComplianceBudgetGateSkipsRetryCarriesCause(t *testing.T) {
	gm := &garbageModel{}
	budget := llm.NewBudget(100)
	budget.Add(150) // Remaining=-50≤0 且 Used=150≤2×100：门有效区间，拦
	w := &Workflow{LLM: llm.NewClient(gm, "fake", nil), Log: nopLogger{}}
	spec := &plugin.RequirementSpec{Source: "PR 描述",
		ACs: []plugin.AcceptanceCriterion{{ID: "AC-1", Text: "验收点 1"}}}
	task := plugin.Task{Budget: budget}
	report, _, err := w.CheckCompliance(context.Background(), task, spec, "摘要")
	if err != nil {
		t.Fatalf("降级路径不应报错: %v", err)
	}
	if !strings.Contains(report.Summary, "预算门拦下重试") {
		t.Fatalf("Summary 应含预算门死因: %q", report.Summary)
	}
	if gm.calls != 1 {
		t.Fatalf("门拦下时只应有首轮调用（自救重试被拒），实得 %d", gm.calls)
	}
	for _, c := range report.Checks {
		if !strings.HasPrefix(c.Detail, r3RuntimeFailPrefix) || !strings.Contains(c.Detail, "task_token_budget") {
			t.Fatalf("Detail 应带运行中断前缀与调参路标: %q", c.Detail)
		}
	}
}
