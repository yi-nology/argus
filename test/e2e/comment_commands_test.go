package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"argus/internal/storage"
)

// v3.15.0 P1-4/P1-5 评论命令链路：@argus describe / @argus testgen——
// 显式命令免意图路由（router 开启时也不走分类），走 Q&A 同款轻量路径
// （不建 task、单次 LLM、回帖评论）。两命令拆两个栈跑：评论命令与 Q&A 共享
// per-PR 30s 节流，同 PR 连发第二条会被节流。

func TestScenarioDescribeCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, commentRouter: true})

	s.setGitLabNoteEvent("@argus-bot describe", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-desc-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("describe 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	if body := bodies[0]; !strings.Contains(body, "PR 变更说明") || !strings.Contains(body, "feature") {
		t.Fatalf("describe 回帖内容错误: %q", body)
	}
	assertNoTasks(t, s)
}

func TestScenarioTestgenCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, commentRouter: true})

	// testgen 带目标路径参数：回帖测试骨架 + 固定「未经编译验证」尾注
	s.setGitLabNoteEvent("@argus-bot testgen", "bob", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-tg-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("testgen 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	body := bodies[0]
	if !strings.Contains(body, "user_test.go") {
		t.Fatalf("testgen 回帖缺测试骨架: %q", body)
	}
	if !strings.Contains(body, "未经编译验证") {
		t.Fatalf("testgen 回帖缺未验证声明: %q", body)
	}
	assertNoTasks(t, s)
}

// waitForReplies 轮询等待 n 条评论回帖（评论链路异步化，与 comment_note_test 同法）。
func waitForReplies(t *testing.T, s *stack, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var bodies []string
	for time.Now().Before(deadline) {
		_, _, _, bodies = s.fake.snapshot()
		if len(bodies) >= n {
			return bodies
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %d 条回帖超时，实际 %d", n, len(bodies))
	return nil
}

// assertNoTasks 评论命令是轻量路径：全程不建审查任务。
func assertNoTasks(t *testing.T, s *stack) {
	t.Helper()
	tasks, err := s.store.ListTasks(context.Background(), storage.StatusPending, 50)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("评论命令不应建任务: n=%d err=%v", len(tasks), err)
	}
}
