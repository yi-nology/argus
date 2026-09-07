package requirement

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/plugin"
)

// ---------- R3.5 报告反思测试（reflection.Refine 的 Argus 侧协议） ----------

// reflectFakeModel 按调用类型路由：draft（系统提示词含"终审编辑"）返回修订 findings
// JSON；critique（系统提示词含"苛刻"）按次数返回判定（首判不通过、次判通过可配）。
type reflectFakeModel struct {
	draftReplies []string // draft 轮次响应（超出取末个）
	verdicts     []string // critique 轮次响应（超出取末个）
	fail         bool     // Generate 直接报错
	draftCalls   int
	critCalls    int
}

func (f *reflectFakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if f.fail {
		return nil, errStr("model down")
	}
	if strings.Contains(input[0].Content, "终审编辑") {
		i := f.draftCalls
		f.draftCalls++
		if i >= len(f.draftReplies) {
			i = len(f.draftReplies) - 1
		}
		return &schema.Message{Role: schema.Assistant, Content: f.draftReplies[i]}, nil
	}
	// critique
	i := f.critCalls
	f.critCalls++
	if i >= len(f.verdicts) {
		i = len(f.verdicts) - 1
	}
	return &schema.Message{Role: schema.Assistant, Content: f.verdicts[i]}, nil
}

func (f *reflectFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errStr("not implemented")
}

func reflectTaskFixture() (plugin.Task, *plugin.MergedFindings) {
	task := plugin.Task{Diff: &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go"}, {Path: "b.go"}}}}
	merged := &plugin.MergedFindings{Findings: []plugin.Finding{
		{File: "a.go", Line: 10, Severity: "high", Comment: "空指针解引用"},
		{File: "b.go", Line: 3, Severity: "low", Comment: "变量命名不贴合语义"},
	}}
	return task, merged
}

// 收敛场景：初稿剔除风格意见 + 修正严重级别，首轮批判即通过。
func TestReflectFindings_ReviseAndDrop(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[{"id":0,"file":"a.go","line":10,"severity":"medium","comment":"空指针解引用：可能路径未判空"}]`},
		verdicts:     []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 2, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	info := w.reflectFindings(context.Background(), task, "摘要", merged)
	if info == nil {
		t.Fatal("应返回反思摘要")
	}
	if info.Converged != true || info.Rounds != 1 {
		t.Fatalf("应首轮收敛（rounds=1, converged=true），got %+v", info)
	}
	if len(merged.Findings) != 1 {
		t.Fatalf("应保留 1 条，got %d", len(merged.Findings))
	}
	f := merged.Findings[0]
	if f.File != "a.go" || f.Severity != "medium" || !strings.Contains(f.Comment, "未判空") {
		t.Fatalf("保留条应按修订值回写，got %+v", f)
	}
	if info.Removed != 1 || info.Adjusted != 1 || info.Kept != 1 {
		t.Fatalf("计数不符（removed=1/adjusted=1/kept=1），got %+v", info)
	}
}

// 防幻觉：修订列表里编造的 id 与重复 id 一律丢弃，不进报告。
func TestReflectFindings_HallucinatedIDRejected(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"high","comment":"空指针解引用"},
			{"id":9,"file":"evil.go","line":1,"severity":"high","comment":"凭空捏造的问题"},
			{"id":0,"file":"a.go","line":11,"severity":"low","comment":"重复 id"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	info := w.reflectFindings(context.Background(), task, "", merged)
	if len(merged.Findings) != 1 || merged.Findings[0].Comment != "空指针解引用" {
		t.Fatalf("应只保留 id=0 原始条目，got %+v", merged.Findings)
	}
	if info.Kept != 1 {
		t.Fatalf("kept 应为 1，got %+v", info)
	}
}

// 防越界：file 改到变更清单之外回退原值；清单内合法改写生效。
func TestReflectFindings_FileMoveValidated(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"b.go","line":10,"severity":"high","comment":"空指针解引用"},
			{"id":1,"file":"evil.go","line":3,"severity":"low","comment":"变量命名不贴合语义"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	w.reflectFindings(context.Background(), task, "", merged)
	if merged.Findings[0].File != "b.go" {
		t.Fatalf("清单内 file 改写应生效，got %s", merged.Findings[0].File)
	}
	if merged.Findings[1].File != "b.go" {
		t.Fatalf("清单外 file 改写应回退原值 b.go，got %s", merged.Findings[1].File)
	}
}

// 修订输出不可解析 → 保留原 findings（返回 nil，不产出反思摘要）。
func TestReflectFindings_ParseFailureKeepsOriginal(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{"这不是 JSON"},
		verdicts:     []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	info := w.reflectFindings(context.Background(), task, "", merged)
	if info != nil {
		t.Fatal("解析失败应返回 nil")
	}
	if len(merged.Findings) != 2 {
		t.Fatalf("应保留原 2 条，got %d", len(merged.Findings))
	}
}

// 模型故障 → 保留原 findings（审查不因反思故障而失败）。
func TestReflectFindings_ModelErrorKeepsOriginal(t *testing.T) {
	m := &reflectFakeModel{fail: true}
	w := &Workflow{ReflectRounds: 2, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	if info := w.reflectFindings(context.Background(), task, "", merged); info != nil {
		t.Fatal("模型故障应返回 nil")
	}
	if len(merged.Findings) != 2 {
		t.Fatalf("应保留原 2 条，got %d", len(merged.Findings))
	}
}

// 无 findings 时短路不调模型。
func TestReflectFindings_EmptyShortCircuit(t *testing.T) {
	m := &reflectFakeModel{draftReplies: []string{"[]"}, verdicts: []string{`{"pass":true,"issues":[]}`}}
	w := &Workflow{ReflectRounds: 2, ReflectModel: m, Log: nopLogger{}}
	if info := w.reflectFindings(context.Background(), plugin.Task{}, "", &plugin.MergedFindings{}); info != nil {
		t.Fatal("空 findings 应返回 nil")
	}
	if m.draftCalls != 0 {
		t.Fatalf("不应调模型，draft calls=%d", m.draftCalls)
	}
}

// 非法 severity 修订回退原值；合法词表归一化生效（critical → high）。
func TestReflectFindings_SeverityNormalized(t *testing.T) {
	m := &reflectFakeModel{
		draftReplies: []string{`[
			{"id":0,"file":"a.go","line":10,"severity":"critical","comment":"空指针解引用"},
			{"id":1,"file":"b.go","line":3,"severity":"whatever","comment":"变量命名不贴合语义"}
		]`},
		verdicts: []string{`{"pass":true,"issues":[]}`},
	}
	w := &Workflow{ReflectRounds: 1, ReflectModel: m, Log: nopLogger{}}
	task, merged := reflectTaskFixture()

	w.reflectFindings(context.Background(), task, "", merged)
	if merged.Findings[0].Severity != "high" {
		t.Fatalf("critical 应归一化为 high，got %s", merged.Findings[0].Severity)
	}
	if merged.Findings[1].Severity != "low" {
		t.Fatalf("非法 severity 应回退原值 low，got %s", merged.Findings[1].Severity)
	}
}
