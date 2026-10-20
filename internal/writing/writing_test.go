package writing

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino/components/model"

	"github.com/oagentkit/llm/llm"
)

// stubModel 固定响应桩（GenerateJSON 走 Client 层 JSON 修复解析）。
type stubModel struct{ resp string }

func (m stubModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return &schema.Message{Content: m.resp}, nil
}

func (m stubModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, context.Canceled
}

func newService(resp string) *Service {
	return &Service{LLM: llm.NewClient(stubModel{resp: resp}, "stub", nil)}
}

func TestDescribe(t *testing.T) {
	svc := newService(`{"title":"修复登录失败锁定","body":"## 变更动机\n\n连续登录无限制"}`)
	d, err := svc.Describe(context.Background(), Input{
		Platform: "gitlab", Owner: "o", Repo: "r", Number: "1",
		Title: "fix", DiffText: "diff --git a/x.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "修复登录失败锁定" || d.Body == "" {
		t.Fatalf("产物不符: %+v", d)
	}
}

func TestDescribeEmptyProduct(t *testing.T) {
	svc := newService(`{}`)
	if _, err := svc.Describe(context.Background(), Input{DiffText: "x"}); err == nil {
		t.Fatal("空产物应报错")
	}
}

func TestDescribeNoLLM(t *testing.T) {
	if _, err := (&Service{}).Describe(context.Background(), Input{}); err == nil {
		t.Fatal("无生成器应报错")
	}
}

func TestChangelog(t *testing.T) {
	svc := newService("- **Fixes:** 修复登录失败不锁定的问题")
	entry, err := svc.Changelog(context.Background(), Input{DiffText: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if entry == "" {
		t.Fatal("条目不应为空")
	}
}

func TestMergeDescription(t *testing.T) {
	section := RenderDescription(Description{Title: "", Body: "新正文"}, "t-1")

	// 空描述 → 直接放管理段
	if got := MergeDescription("", section); got != section {
		t.Fatalf("空描述应等于管理段: %q", got)
	}
	// 作者内容 → 保留 + 追加
	got := MergeDescription("作者写的说明", section)
	if !strings.Contains(got, "作者写的说明") || !strings.Contains(got, "新正文") {
		t.Fatalf("追加语义破坏: %q", got)
	}
	// 已有管理段 → 整段替换，旧正文不残留
	old := "作者写的说明\n\n---\n\n" + RenderDescription(Description{Title: "", Body: "旧正文"}, "t-0")
	got = MergeDescription(old, section)
	if strings.Contains(got, "旧正文") {
		t.Fatalf("旧管理段应被整段替换: %q", got)
	}
	if !strings.Contains(got, "作者写的说明") || !strings.Contains(got, "新正文") {
		t.Fatalf("替换应保留作者段+新段: %q", got)
	}
}

// TestRenderMergeRoundTrip Render 的真实输出（begin 带任务号）过 Merge 必须命中
// 替换分支——v3.75.3 修复的契约漂移回归（此前 Merge 找精确串永不命中，反复追加）。
func TestRenderMergeRoundTrip(t *testing.T) {
	first := RenderDescription(Description{Title: "t1", Body: "第一版"}, "task-a")
	withAuthor := "作者原始描述"
	once := MergeDescription(withAuthor, first)
	if !strings.Contains(once, "作者原始描述") || !strings.Contains(once, "第一版") {
		t.Fatalf("首写应追加保留作者段: %q", once)
	}
	second := RenderDescription(Description{Title: "t2", Body: "第二版"}, "task-b")
	twice := MergeDescription(once, second)
	if strings.Contains(twice, "第一版") {
		t.Fatalf("二次 describe 应替换旧管理段: %q", twice)
	}
	if !strings.Contains(twice, "第二版") || !strings.Contains(twice, "作者原始描述") {
		t.Fatalf("二次应保留新段+作者段: %q", twice)
	}
	// 管理段之后的作者内容不被吞（v3.75.3 修复的尾段丢失回归）
	afterAuthor := MergeDescription(once+"\n\n作者后补说明", second)
	if !strings.Contains(afterAuthor, "作者后补说明") {
		t.Fatalf("管理段之后的作者内容应保留: %q", afterAuthor)
	}
}

func TestAnalyzeCIFailure(t *testing.T) {
	svc := newService(`{"conclusion":"代码问题","root_cause":"空指针","fix":"补判空"}`)
	a, err := svc.AnalyzeCIFailure(context.Background(), CIInput{
		Platform: "gitlab", Owner: "o", Repo: "r", Number: "1",
		Jobs: []CIJob{{Name: "test", Stage: "test", Trace: "panic: nil deref"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Conclusion != "代码问题" || a.RootCause != "空指针" {
		t.Fatalf("归因不符: %+v", a)
	}
	body := RenderCIAnalysis(a, "t-1")
	if !strings.Contains(body, "CI 失败归因") || !strings.Contains(body, "t-1") {
		t.Fatalf("评论渲染不符: %s", body)
	}
}

func TestAnalyzeCIFailureNoJobs(t *testing.T) {
	svc := newService(`{}`)
	if _, err := svc.AnalyzeCIFailure(context.Background(), CIInput{}); err == nil {
		t.Fatal("无失败 job 应报错")
	}
}
