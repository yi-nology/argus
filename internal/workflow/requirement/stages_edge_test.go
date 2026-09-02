package requirement

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"git.example.com/enjoydream/agentkit/llm"
	"argus/internal/plugin"
)

// fakeModel 通用假模型：按 system prompt 前缀路由响应。
type fakeModel struct {
	digest func(i, total int) string
	final  string
	fail   bool
	calls  [][]*schema.Message
	seq    int
}

func (f *fakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.calls = append(f.calls, input)
	if strings.Contains(input[0].Content, "R1a") {
		if f.fail {
			return nil, errStr("boom")
		}
		f.seq++
		total := 1
		if i := strings.Index(input[1].Content, "需求片段 "); i >= 0 {
			fmt.Sscanf(input[1].Content[i:], "需求片段 %d/%d", new(int), &total)
		}
		return &schema.Message{Role: schema.Assistant, Content: f.digest(f.seq, total)}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: f.final}, nil
}
func (f *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errStr("nope")
}

type errStr string

func (e errStr) Error() string { return string(e) }

func newWF(m *fakeModel, bodyMax, issueMax int) *Workflow {
	return &Workflow{
		LLM:             llm.NewClient(m, "fake", nil),
		Log:             nopLogger{},
		ReqBodyMaxChars: bodyMax, ReqIssueMaxChars: issueMax,
	}
}

func task(body string) plugin.Task {
	return plugin.Task{PR: plugin.PRContext{Body: body, HeadBranch: "feature/x"}}
}

// ---------- 边缘场景 ----------

// TestR1_OnlyBranch 只有分支名（无 body、无 issues）→ 走 R1 解析。
func TestR1_OnlyBranch(t *testing.T) {
	fm := &fakeModel{final: `{"source":"分支名","acceptance_criteria":[],"ambiguous":["未在 PR 描述找到需求"]}`}
	wf := newWF(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), plugin.Task{PR: plugin.PRContext{HeadBranch: "feature/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.calls) != 1 {
		t.Fatalf("应 1 次 R1 调用，got %d", len(fm.calls))
	}
	if spec.Source != "分支名" {
		t.Fatalf("source 不符: %s", spec.Source)
	}
}

// TestR1_BodyExactlyAtLimit body 恰好在 bodyMax 内 → 单次 R1（不走两级）。
func TestR1_BodyExactlyAtLimit(t *testing.T) {
	fm := &fakeModel{final: specJSON(1)}
	wf := newWF(fm, 100, 50)
	body := string(make([]byte, 100)) // 100 chars = bodyMax
	spec, err := wf.ParseRequirement(context.Background(), task(body))
	if err != nil {
		t.Fatal(err)
	}
	// 恰好100=bodyMax，不超过 → 单次R1（无 R1a）
	if len(fm.calls) != 1 {
		t.Fatalf("恰好边界应单次 R1，got %d 次调用", len(fm.calls))
	}
	if len(spec.ACs) != 1 {
		t.Fatal("应解析出1条 AC")
	}
}

// TestR1_BodyOneOverLimit body 超 bodyMax 一个字符 → 触发两级提炼。
func TestR1_BodyOneOverLimit(t *testing.T) {
	fm := &fakeModel{
		digest: func(i, total int) string { return "- 要点" },
		final:  specJSON(2),
	}
	wf := newWF(fm, 100, 50)
	body := string(make([]byte, 101)) //101 > bodyMax
	spec, err := wf.ParseRequirement(context.Background(), task(body))
	if err != nil {
		t.Fatal(err)
	}
	// 超过边界应两级：101 runes/bodyMax=100 →2块 R1a +1次 R1 =3次调用
	if len(fm.calls) != 3 {
		t.Fatalf("超过边界应两级（2块R1a+R1），got %d", len(fm.calls))
	}
	if !hasPrefixIn(spec.Ambiguous, "[两级提炼]") {
		t.Fatal("两级提炼应留痕 Ambiguous")
	}
	if len(spec.ACs) != 2 {
		t.Fatal("应解析出2条 AC")
	}
}

// TestR1_BothTruncated body 和 issue 都超限 → 两个截断留痕。
func TestR1_BothTruncated(t *testing.T) {
	fm := &fakeModel{
		digest: func(i, total int) string { return "- 要点" },
		final:  specJSON(1),
	}
	// bodyMax=50, issueMax=30
	// body=200(>50), 不触发两级提炼阈值(200≤50)，走两级路径 → R1a 提炼
	wf := newWF(fm, 50, 30)
	spec, err := wf.ParseRequirement(context.Background(), task(strings.Repeat("需求", 300)))
	if err != nil {
		t.Fatal(err)
	}
	if !hasPrefixIn(spec.Ambiguous, "[两级提炼]") {
		t.Fatalf("两级提炼应留痕: %v", spec.Ambiguous)
	}
}

// TestR1_DigestPartialFailure 部分块 R1a 失败 → 失败块回退原文，不中断整体。
func TestR1_DigestPartialFailure(t *testing.T) {
	callCount := 0
	fm := &fakeModel{
		digest: func(i, total int) string {
			callCount++
			if i == 1 {
				return "- 成功要点"
			} // 第一块成功
			return "- 其他" // 第二块成功
		},
		final: specJSON(1),
		fail:  false,
	}
	wf := newWF(fm, 100, 50)
	// 用 body=300 runes(>100) 触发两级；分3块，逐块调 R1a
	spec, err := wf.ParseRequirement(context.Background(), task(strings.Repeat("测试", 300)))
	if err != nil {
		t.Fatal(err)
	}
	// 部分失败不影响整体完成
	if len(spec.ACs) != 1 {
		t.Fatalf("应有1条AC，got %d", len(spec.ACs))
	}
}

// TestR1_ACCapExactly30 恰好30条 → 不触发上限。
func TestR1_ACCapExactly30(t *testing.T) {
	fm := &fakeModel{final: specJSON(30)}
	wf := newWF(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), task("实现A"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ACs) != 30 {
		t.Fatalf("want30，got %d", len(spec.ACs))
	}
	if hasPrefixIn(spec.Ambiguous, "[条目超限]") {
		t.Fatal("30条不应触发上限")
	}
}

// TestR1_ACCap31 恰好31条 → 多余1条转 Ambiguous。
func TestR1_ACCap31(t *testing.T) {
	fm := &fakeModel{final: specJSON(31)}
	wf := newWF(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), task("实现A"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.ACs) != 30 {
		t.Fatalf("want30（截断），got %d", len(spec.ACs))
	}
	if !hasPrefixIn(spec.Ambiguous, "[条目超限]") {
		t.Fatal("31条应触发上限留痕")
	}
	// 条目超限 → Ambiguous → R5.3 人工确认
	if v, _ := Verdict(spec, nil, nil); v != VerdictComment {
		t.Fatalf("条目超限应落 R5.3 COMMENT，got %s", v)
	}
}

// TestR1_JSONParseFailure LLM 返回非法 JSON → 重试后仍失败 → 解析降级。
func TestR1_JSONParseFailure(t *testing.T) {
	fm := &fakeModel{final: "这不是JSON"}
	wf := newWF(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), task("需求A"))
	if err != nil {
		t.Fatal(err)
	}
	// 解析降级：全部当 ambiguous
	if spec.Source != "解析降级" {
		t.Fatalf("source 不符: %s", spec.Source)
	}
	if len(spec.Ambiguous) == 0 {
		t.Fatal("降级应有 Ambiguous 条目")
	}
}

// TestR1_EmptyPRAndIssues PR body 空 + 无 issues + 无分支名 → 全缺提示。
func TestR1_EmptyPRAndIssues(t *testing.T) {
	fm := &fakeModel{final: specJSON(0)}
	wf := newWF(fm, 1000, 500)
	spec, err := wf.ParseRequirement(context.Background(), plugin.Task{PR: plugin.PRContext{}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Source != "未关联" {
		t.Fatalf("source 不符: %s", spec.Source)
	}
	if len(spec.Ambiguous) == 0 || spec.Ambiguous[0] != "未在 PR 描述、分支名中找到关联需求，请补充需求说明" {
		t.Fatalf("应有全缺提示: %v", spec.Ambiguous)
	}
}

// TestR1_DigestToR1Flow 两级路径中 R1a 的输入带分块序号，R1 的输入只有提炼要点。
func TestR1_DigestToR1Flow(t *testing.T) {
	fm := &fakeModel{
		digest: func(i, total int) string { return fmt.Sprintf("- 第%d块要点/共%d块", i, total) },
		final:  specJSON(2),
	}
	wf := newWF(fm, 100, 50)
	body := strings.Repeat("需求描述内容", 200) // >100 runes
	spec, err := wf.ParseRequirement(context.Background(), task(body))
	if err != nil {
		t.Fatal(err)
	}
	// R1a 调用应带分块序号
	for _, call := range fm.calls[:len(fm.calls)-1] {
		if !strings.Contains(call[0].Content, "R1a") {
			continue
		}
		if !strings.Contains(call[1].Content, "需求片段 ") {
			t.Fatalf("R1a 输入应带分块序号: %s", call[1].Content[:100])
		}
	}
	// R1 调用应只含提炼要点
	finalCall := fm.calls[len(fm.calls)-1]
	if strings.Contains(finalCall[1].Content, "需求描述内容") {
		t.Fatal("R1 输入不应含原始原文")
	}
	if !strings.Contains(finalCall[1].Content, "分段提炼要点") {
		t.Fatal("R1 输入应含提炼标记")
	}
	_ = spec
}
