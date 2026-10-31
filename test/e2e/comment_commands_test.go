package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/domain"
	"argus/internal/storage"
)

// setGitHubIssueCommentEvent 预置 GitHub issue_comment 事件（生产 GitHub 形态：
// SDK 只置 Type 不填 Actor/Repo，normalizeCommentEvent 从 RawPayload 兜底）。
func (s *stack) setGitHubIssueCommentEvent(body, actor string) {
	payload := fmt.Sprintf(`{"action":"created",
		"issue":{"number":7,"pull_request":{"url":"https://api.github.com/x/pr/7"}},
		"comment":{"body":%q},
		"sender":{"login":%q},
		"repository":{"full_name":"acme/app","name":"app","owner":{"login":"acme"}}}`, body, actor)
	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()
	s.fake.ev = &provider.NormalizedEvent{
		ID: "gh-note", Type: "issue_comment", Source: "github",
		RawPayload: json.RawMessage(payload),
	}
}

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

// v5.2.0 显式重审命令：@bot review 不经意图路由确定性建复审任务——
// ARGUS_COMMENT_ROUTER 关闭也生效（短指令零分类开销）。
func TestScenarioReviewCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, extraPlatforms: []string{"gitlab"}})

	s.setGitLabNoteEvent("@argus-bot review", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-rv-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("review 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	if !strings.Contains(bodies[0], "重新审查请求") {
		t.Fatalf("review 应回执重审受理: %q", bodies[0])
	}
	tasks, err := s.store.ListTasks(context.Background(), storage.StatusPending, 50)
	if err != nil || len(tasks) != 1 || tasks[0].Platform != "gitlab" || tasks[0].Number != "7" {
		t.Fatalf("review 应建 1 个复审任务: n=%d err=%v", len(tasks), err)
	}
	// v5.3.0 即时回执：触发指令的评论应被加 👀（commentID:100 来自 note 载荷）。
	if len(s.fake.reactions) == 0 || s.fake.reactions[0] != "100:eyes" {
		t.Fatalf("应给指令评论加 👀 回执: %v", s.fake.reactions)
	}
}

// v5.2.0 mention 任意位置（GitHub issue_comment 形态，生产 GitHub 不填 Actor/Repo，
// 走 normalizeCommentEvent 负载兜底）：句中 mention 同样触发显式重审命令。
func TestScenarioReviewCommandMentionAnywhere(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, extraPlatforms: []string{"github"}})

	s.setGitHubIssueCommentEvent("请 @argus-bot review 重点看登录模块", "carol")
	if code, resp := s.postWebhookOn(t, "github", "gh-rv-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("GitHub 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	if !strings.Contains(bodies[0], "重新审查请求") || !strings.Contains(bodies[0], "重点看登录模块") {
		t.Fatalf("句中 mention 应触发重审并带补充说明: %q", bodies[0])
	}
	tasks, err := s.store.ListTasks(context.Background(), storage.StatusPending, 50)
	if err != nil || len(tasks) != 1 || tasks[0].Platform != "github" {
		t.Fatalf("应建 1 个 github 复审任务: n=%d err=%v", len(tasks), err)
	}
}

// v5.2.0 单条复核命令：fp 短码定位入库意见 → LLM 裁决 → 回帖结论（不建任务）。
func TestScenarioRecheckCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, commentRouter: true})

	// 预置一条入库意见（prints 表），fp 短码 ab12cd34。
	if _, err := s.store.UpsertPrint(context.Background(), "gitlab", "acme", "app", "7",
		"ab12cd34ef567890",
		domain.Finding{File: "main.go", Line: 12, Severity: "high", Comment: "空指针风险", By: "builtin"},
		"seed-task", time.Now()); err != nil {
		t.Fatal(err)
	}

	s.setGitLabNoteEvent("@argus-bot recheck fp:ab12cd34 这条是不是误报", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-rc-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("recheck 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	body := bodies[0]
	if !strings.Contains(body, "意见复核") || !strings.Contains(body, "结论") {
		t.Fatalf("recheck 回帖缺复核结论: %q", body)
	}
	assertNoTasks(t, s)
}

// v5.3.0 help 指令：指令手册（目录单源渲染），免节流免门控。
func TestScenarioHelpCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, extraPlatforms: []string{"gitlab"}})

	s.setGitLabNoteEvent("@argus-bot help", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-help-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("help 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	body := bodies[0]
	for _, want := range []string{"指令速查", "review", "recheck", "status", "describe", "suppress"} {
		if !strings.Contains(body, want) {
			t.Fatalf("help 回帖缺 %q: %q", want, body)
		}
	}
	assertNoTasks(t, s)
}

// v5.3.0 status 指令：最近任务状态与结论（免节流）。
func TestScenarioStatusCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, extraPlatforms: []string{"gitlab"}})

	// 预置一个已完成任务（status 数据源：ListTasksByPR）。
	tk := storage.NewTask("requirement_compliance", "gitlab", "acme", "app", "7")
	tk.Status = storage.StatusDone
	tk.Verdict = "request_changes"
	tk.HeadSHA = "sha-1"
	if err := s.store.CreateTask(context.Background(), tk); err != nil {
		t.Fatal(err)
	}

	s.setGitLabNoteEvent("@argus-bot status", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-st-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("status 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	body := bodies[0]
	for _, want := range []string{"审查状态", "request_changes", "recheck"} {
		if !strings.Contains(body, want) {
			t.Fatalf("status 回帖缺 %q: %q", want, body)
		}
	}
	assertNoTasks(t, s)
}

// v5.3.0 烧预算指令门控：非协作者触发 review 被拒（🔒 提示），不建任务。
func TestScenarioDriveGateDenied(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, extraPlatforms: []string{"gitlab"}})
	s.fake.mu.Lock()
	s.fake.collabs = []string{"alice"} // mallory 不在协作者名单
	s.fake.mu.Unlock()

	s.setGitLabNoteEvent("@argus-bot review", "mallory", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-gate-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	if !strings.Contains(bodies[0], "写权限") {
		t.Fatalf("应回权限提示: %q", bodies[0])
	}
	assertNoTasks(t, s)
}

// v5.3.0 changelog 指令：CHANGELOG 条目草稿（轻量路径不建任务）。
func TestScenarioChangelogCommand(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, commentRouter: true})

	s.setGitLabNoteEvent("@argus-bot changelog", "alice", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-cl-1"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("changelog 评论应被接受: %d %+v", code, resp)
	}
	bodies := waitForReplies(t, s, 1)
	if !strings.Contains(bodies[0], "CHANGELOG 条目") {
		t.Fatalf("changelog 回帖缺条目草稿: %q", bodies[0])
	}
	assertNoTasks(t, s)
}
