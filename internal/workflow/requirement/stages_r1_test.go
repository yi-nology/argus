package requirement

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"
	"git.example.com/enjoydream/agentkit/llm"
)

// ---------- 测试基建：假模型 + 空 logger（复用 worker_test 的 nopLogger 形态） ----------

// fakeChatModel 按 R1a / R1 阶段路由响应的假模型，记录全部调用供断言。
type fakeChatModel struct {
	calls   [][]*schema.Message
	digest  func(i int, total int) string // R1a 阶段响应（块 i/total）
	final   string                        // R1 最终解析响应
	FailR1A bool                          // 所有 R1a 调用返回错误
	callSeq int
}

func (f *fakeChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.calls = append(f.calls, input)
	if strings.Contains(input[0].Content, "R1a") {
		if f.FailR1A {
			return nil, fmt.Errorf("digest boom")
		}
		total := 1
		if i := strings.Index(input[1].Content, "需求片段 "); i >= 0 {
			fmt.Sscanf(input[1].Content[i:], "需求片段 %d/%d", new(int), &total)
		}
		return &schema.Message{Role: schema.Assistant, Content: f.digest(f.callSeq+1, total)}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: f.final}, nil
}

func (f *fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not implemented")
}

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)                      {}
func (nopLogger) Info(string, ...any)                       {}
func (nopLogger) Warn(string, ...any)                       {}
func (nopLogger) Error(string, ...any)                      {}
func (nopLogger) Debugf(string, ...any)                     {}
func (nopLogger) Infof(string, ...any)                      {}
func (nopLogger) Warnf(string, ...any)                      {}
func (nopLogger) Errorf(string, ...any)                     {}
func (nopLogger) CtxDebug(context.Context, string, ...any)  {}
func (nopLogger) CtxInfo(context.Context, string, ...any)   {}
func (nopLogger) CtxWarn(context.Context, string, ...any)   {}
func (nopLogger) CtxError(context.Context, string, ...any)  {}
func (nopLogger) CtxDebugf(context.Context, string, ...any) {}
func (nopLogger) CtxInfof(context.Context, string, ...any)  {}
func (nopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (nopLogger) CtxErrorf(context.Context, string, ...any) {}
func (nopLogger) WithCtx(context.Context) logx.Logger       { return nopLogger{} }
func (nopLogger) WithService(string) logx.Logger            { return nopLogger{} }
func (nopLogger) WithAttrs(...logx.Attr) logx.Logger        { return nopLogger{} }
func (nopLogger) WithGroup(string) logx.Logger              { return nopLogger{} }
func (nopLogger) SetLevel(logx.Level)                       {}

func specJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"source":"PR 描述","acceptance_criteria":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"AC-%d","text":"验收点 %d"}`, i, i)
	}
	b.WriteString(`],"ambiguous":[]}`)
	return b.String()
}

func newTestWorkflow(m *fakeChatModel, bodyMax, issueMax int) *Workflow {
	return &Workflow{
		LLM:              llm.NewClient(m, "fake", nil),
		Log:              nopLogger{},
		ReqBodyMaxChars:  bodyMax,
		ReqIssueMaxChars: issueMax,
	}
}

func r1Task(body string) plugin.Task {
	return plugin.Task{PR: plugin.PRContext{Body: body, HeadBranch: "feature/x"}}
}

// ---------- 用例 ----------

// TestParseRequirement_SmallBody 单段短需求：单次 R1 直进，无任何截断/提炼留痕。
func TestParseRequirement_SmallBody(t *testing.T) {
	fm := &fakeChatModel{final: specJSON(2)}
	wf := newTestWorkflow(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), r1Task(strings.Repeat("需求A。", 50)))
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.calls) != 1 {
		t.Fatalf("want 1 次 LLM 调用，got %d", len(fm.calls))
	}
	if len(spec.ACs) != 2 || len(spec.Ambiguous) != 0 {
		t.Fatalf("want 2 AC/0 ambiguous，got %d/%d", len(spec.ACs), len(spec.Ambiguous))
	}
}

// TestParseRequirement_TwoStage 超长需求：先 R1a 分块提炼（含分块序号），再 R1 解析提炼结果。
func TestParseRequirement_TwoStage(t *testing.T) {
	fm := &fakeChatModel{
		digest: func(i, total int) string { return fmt.Sprintf("- 要点%d/%d", i, total) },
		final:  specJSON(3),
	}
	wf := newTestWorkflow(fm, 1000, 500)
	body := strings.Repeat("需求", 1500) // 3000 runes > bodyMax=1000 → 两级
	spec, err := wf.ParseRequirement(context.Background(), r1Task(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.calls) != 4 {
		t.Fatalf("want 4 次 LLM 调用（3 块 R1a + R1），got %d", len(fm.calls))
	}
	digestCall, finalCall := fm.calls[0], fm.calls[3]
	if !strings.Contains(digestCall[0].Content, "R1a") {
		t.Fatalf("第 1 次调用应是 R1a 提炼，got system: %.40s", digestCall[0].Content)
	}
	if !strings.Contains(digestCall[1].Content, "需求片段 1/3") {
		t.Fatalf("3000 runes/bodyMax 1000 应分 3 块，digest 输入: %.80s", digestCall[1].Content)
	}
	if !strings.Contains(finalCall[1].Content, "分段提炼要点") || strings.Contains(finalCall[1].Content, "需求需求") {
		t.Fatalf("R1 输入应只含提炼要点而非原文，got: %.120s", finalCall[1].Content)
	}
	if len(spec.ACs) != 3 {
		t.Fatalf("want 3 AC，got %d", len(spec.ACs))
	}
	if !hasPrefixIn(spec.Ambiguous, "[两级提炼]") {
		t.Fatalf("两级提炼必须留痕 Ambiguous，got %v", spec.Ambiguous)
	}
	// Ambiguous 非空 → R5.3 人工确认
	if v, _ := Verdict(spec, nil, nil); v != VerdictComment {
		t.Fatalf("两级提炼后应落 R5.3 COMMENT，got %s", v)
	}
}

// TestParseRequirement_DigestChunkCap 单段超硬上限：分块数封顶并留痕。
func TestParseRequirement_DigestChunkCap(t *testing.T) {
	fm := &fakeChatModel{digest: func(i, total int) string { return "- 要点" }, final: specJSON(1)}
	wf := newTestWorkflow(fm, 500, 500)
	// digestMax=4000 runes，块=500 → 8 块封顶；原文 9000 runes 触发硬上限截断
	spec, err := wf.ParseRequirement(context.Background(), r1Task(strings.Repeat("长", 9000)))
	if err != nil {
		t.Fatal(err)
	}
	// 8 次 R1a + 1 次 R1（截断发生在 R1a 之前，不影响调用数）
	if len(fm.calls) != maxDigestChunks+1 {
		t.Fatalf("want %d 次 LLM 调用，got %d", maxDigestChunks+1, len(fm.calls))
	}
	if !hasPrefixIn(spec.Ambiguous, "[超长截断]") {
		t.Fatalf("硬上限截断必须留痕，got %v", spec.Ambiguous)
	}
}

// TestParseRequirement_DigestFailure 提炼失败：回退截断原文继续 R1，且留痕落人工确认。
func TestParseRequirement_DigestFailure(t *testing.T) {
	fm := &fakeChatModel{FailR1A: true, digest: func(i, total int) string { return "- x" }, final: specJSON(1)}
	wf := newTestWorkflow(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), r1Task(strings.Repeat("需求", 800)))
	if err != nil {
		t.Fatal(err)
	}
	if !hasPrefixIn(spec.Ambiguous, "[超长截断]") || !strings.Contains(strings.Join(spec.Ambiguous, "\n"), "提炼失败") {
		t.Fatalf("提炼失败必须留痕，got %v", spec.Ambiguous)
	}
	if len(spec.ACs) != 1 {
		t.Fatalf("回退路径仍应完成 R1 解析，got %d AC", len(spec.ACs))
	}
}

// TestParseRequirement_ACCap AC 条数上限：35 条 → 保留 30，多余转 Ambiguous。
func TestParseRequirement_ACCap(t *testing.T) {
	fm := &fakeChatModel{final: specJSON(maxACs + 5)}
	wf := newTestWorkflow(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), r1Task("实现A"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ACs) != maxACs {
		t.Fatalf("want %d AC，got %d", maxACs, len(spec.ACs))
	}
	if !hasPrefixIn(spec.Ambiguous, "[条目超限]") {
		t.Fatalf("条目超限必须留痕，got %v", spec.Ambiguous)
	}
	if v, _ := Verdict(spec, nil, nil); v != VerdictComment {
		t.Fatalf("条目超限应落 R5.3 COMMENT，got %s", v)
	}
}

// TestParseRequirement_ACTextTrunc 单条 AC 文本超长截断。
func TestParseRequirement_ACTextTrunc(t *testing.T) {
	long := `{"source":"x","acceptance_criteria":[{"id":"AC-1","text":"` + strings.Repeat("详", 800) + `"}],"ambiguous":[]}`
	fm := &fakeChatModel{final: long}
	wf := newTestWorkflow(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), r1Task("实现A"))
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(spec.ACs[0].Text)) > maxACTextRunes+10 || !strings.HasSuffix(spec.ACs[0].Text, "…（截断）") {
		t.Fatalf("AC 文本应被截断，len=%d tail=%s", len([]rune(spec.ACs[0].Text)), spec.ACs[0].Text[maxACTextRunes-10:])
	}
}

func hasPrefixIn(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
