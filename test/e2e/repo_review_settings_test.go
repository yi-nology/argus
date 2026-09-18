package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/yi-nology/git-platform-sdk/provider"
)

// v3.15.0 P0-2 仓库级配置面全链路：.reviewagents.yaml review 段
// （min_severity / exclude_paths / max_comments）在真实管线中的消费。
func TestScenarioRepoReviewMinSeverity(t *testing.T) {
	s := newStack(t, stackOpts{
		r1: `{"source":"issue #12","acceptance_criteria":[{"id":"AC-1","text":"完成变更所述功能"}],"ambiguous":[]}`,
		r3: `{"checks":[{"id":"AC-1","status":"implemented","evidence":"internal/api/user.go:3"}],"scope_creep":[],"summary":"完成"}`,
		// correctness 仅产出 low 意见 → min_severity=high 应滤掉 → R5.5 approve
		correctness: `{"findings":[{"file":"internal/api/user.go","line":3,"severity":"low","comment":"变量命名可更清晰","by":"correctness"}],"notes":""}`,
		diffFiles:   twoFilesWithGenerated(),
		repoFiles: map[string]string{
			".reviewagents.yaml": "review:\n  exclude_paths: [\"**/generated/**\"]\n  min_severity: high\n",
		},
	})
	taskID := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, taskID, "done")
	if task.Verdict != "approve" {
		t.Fatalf("low 意见被 min_severity=high 过滤后应 R5.5 approve，got %s\n报告:\n%s", task.Verdict, task.Report)
	}
	if !strings.Contains(task.Report, "review.min_severity=high：1 条低于阈值的意见未纳入报告与裁决") {
		t.Fatalf("报告缺 min_severity 降级注明:\n%s", task.Report)
	}
	if !strings.Contains(task.Report, "review 路径过滤：1 个变更文件被排除") {
		t.Fatalf("报告缺路径排除注明:\n%s", task.Report)
	}
	_, _, reviews, _ := s.fake.snapshot()
	if len(reviews) != 1 || len(reviews[0].Comments) != 0 {
		t.Fatalf("被过滤的意见不应产生行内评论: %+v", reviews)
	}
}

func TestScenarioRepoReviewMaxComments(t *testing.T) {
	s := newStack(t, stackOpts{
		r1: `{"source":"issue #12","acceptance_criteria":[{"id":"AC-1","text":"完成变更所述功能"}],"ambiguous":[]}`,
		r3: `{"checks":[{"id":"AC-1","status":"implemented","evidence":"internal/api/user.go:3"}],"scope_creep":[],"summary":"完成"}`,
		// 2 条 medium 行内意见 + min_severity=medium（均保留）→ max_comments=1 钳行内
		correctness: `{"findings":[` +
			`{"file":"internal/api/user.go","line":3,"severity":"medium","comment":"错误被吞","by":"correctness"},` +
			`{"file":"internal/api/user.go","line":7,"severity":"medium","comment":"缺超时","by":"correctness"}],"notes":""}`,
		diffFiles: twoFilesWithGenerated(),
		repoFiles: map[string]string{
			".reviewagents.yaml": "review:\n  exclude_paths: [\"**/generated/**\"]\n  min_severity: medium\n  max_comments: 1\n",
		},
	})
	taskID := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, taskID, "done")
	// medium 意见保留 → R5.4 comment
	if task.Verdict != "comment" {
		t.Fatalf("medium 意见应驱动 R5.4 comment，got %s", task.Verdict)
	}
	_, _, reviews, bodies := s.fake.snapshot()
	if len(reviews) != 1 || len(reviews[0].Comments) != 1 {
		t.Fatalf("max_comments=1 应只保留 1 条行内评论: %+v", reviews)
	}
	summary := strings.Join(bodies, "\n")
	if !strings.Contains(summary, "review.max_comments=1：1 条行内意见超出上限") {
		t.Fatalf("总结评论缺钳制注明:\n%s", summary)
	}
	// 总结评论的 findings 段仍完整列出 2 条（透明可查）
	if !strings.Contains(summary, "缺超时") {
		t.Fatalf("总结评论应保留全部意见:\n%s", summary)
	}
}

// twoFilesWithGenerated 一个正常源文件 + 一个命中 **/generated/** 的文件。
func twoFilesWithGenerated() []provider.ChangedFile {
	return []provider.ChangedFile{
		{NewPath: "internal/api/user.go", Diff: "diff --git a/internal/api/user.go b/internal/api/user.go\n--- a/internal/api/user.go\n+++ b/internal/api/user.go\n@@ -1,3 +1,5 @@\n package api\n+\n+func Handle() error { return nil }\n", Additions: 2},
		{NewPath: "api/generated/types.go", Diff: "diff --git a/api/generated/types.go b/api/generated/types.go\n--- a/api/generated/types.go\n+++ b/api/generated/types.go\n@@ -1,2 +1,4 @@\n package generated\n+\n+type X struct{}\n", Additions: 2},
	}
}
