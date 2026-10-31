package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/storage"
)

// TestScenario20_GitLabNoteComment （v3.5.3）GitLab note 评论 @argus 触发链路：
// 生产 GitLab 的评论经 SDK 归一化为 cr.note 事件——此前 isCommentEvent 不认，@argus
// 被白名单静默丢弃。本场景验证完整链路 + 反复触发的三道防护（节流/bot 自审/系统注释）。
// 走评论意图路由的重审关键词快路径（确定性建任务，不经 LLM）。
// v3.5.12：评论处理异步化（webhook 立即返回，副作用在后台 goroutine 落地）——
// 断言改为轮询式；"仍为 1"类断言先短暂等待再查，确认后台 goroutine 确实没建。
func TestScenario20_GitLabNoteComment(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0, commentRouter: true})
	ctx := context.Background()

	countPending := func() int {
		tasks, err := s.store.ListTasks(ctx, storage.StatusPending, 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(tasks)
	}
	replyBodies := func() []string {
		_, _, _, bodies := s.fake.snapshot()
		return bodies
	}
	waitFor := func(cond func() bool, msg string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal(msg)
	}
	// settle 等后台静默：相隔 100ms 两次采样计数不变才继续——「无变化」类断言
	// 只在静默后有统计学意义（固定睡一觉会被慢背景 goroutine 偶发击穿）。
	settle := func() {
		count := func() int { return len(replyBodies()) + countPending() }
		prev := count()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			cur := count()
			if cur == prev {
				return
			}
			prev = cur
		}
	}

	// ① MR note「@argus-bot 请重新审查」→ 回执评论 + 建全量复审任务（platform=gitlab）
	s.setGitLabNoteEvent("@argus-bot 请重新审查", "alice", false)
	code, resp := s.postWebhookOn(t, "gitlab", "gl-d1")
	if code != 200 || resp.Status != "accepted" || resp.Reason != "qa comment" {
		t.Fatalf("GitLab note 评论应被评论路径接受: %d %+v", code, resp)
	}
	var task *storage.Task
	waitFor(func() bool { return countPending() == 1 }, "应新建 1 个复审任务")
	waitFor(func() bool { return len(replyBodies()) == 1 }, "应回执任务号评论")
	tasks, err := s.store.ListTasks(ctx, storage.StatusPending, 50)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("应新建 1 个复审任务: n=%d err=%v", len(tasks), err)
	}
	task = &tasks[0]
	if task.Platform != "gitlab" || task.Number != "7" || !strings.Contains(task.ArtifactsJSON, `"trigger":"comment"`) {
		t.Fatalf("任务归属错误: %+v", task)
	}
	if !strings.Contains(replyBodies()[0], task.ID) {
		t.Fatalf("应回执任务号评论: bodies=%v", replyBodies())
	}

	// ② 反复触发防护一：30s per-PR 节流——同 PR 第二条重审评论不建任务；
	// v5.3.0 起指令撞冷却有感知（回帖冷却提示），不再静默丢弃。
	s.setGitLabNoteEvent("@argus-bot 再审一遍", "bob", false)
	if code, resp := s.postWebhookOn(t, "gitlab", "gl-d2"); code != 200 || resp.Status != "accepted" {
		t.Fatalf("节流评论 webhook 仍应 200 accepted: %d %+v", code, resp)
	}
	settle()
	if n := countPending(); n != 1 {
		t.Fatalf("30s 内第二条评论应被节流，任务数应仍为 1，got %d", n)
	}
	bodies := replyBodies()
	if len(bodies) != 2 {
		t.Fatalf("节流指令应回冷却提示（v5.3.0），got %d 条回帖", len(bodies))
	}
	if !strings.Contains(bodies[1], "冷却") {
		t.Fatalf("第二条回帖应为冷却提示: %q", bodies[1])
	}

	// ③ 反复触发防护二：bot 自评防循环——bot 账号自己的评论（含引用的 @argus 原文）被忽略
	s.setGitLabNoteEvent("> @argus-bot 请重新审查\n\n已收到重新审查请求", "argus-bot", false)
	s.postWebhookOn(t, "gitlab", "gl-d3")
	settle()
	if n := countPending(); n != 1 {
		t.Fatalf("bot 自评不应建任务，got %d", n)
	}

	// ④ 反复触发防护三：GitLab 系统注释（改指派等自动生成）无用户意图，忽略
	s.setGitLabNoteEvent("assigned to @alice", "gitlab-avatar", true)
	s.postWebhookOn(t, "gitlab", "gl-d4")
	settle()
	if n := countPending(); n != 1 {
		t.Fatalf("系统注释不应建任务，got %d", n)
	}
	if n := len(replyBodies()); n != 2 {
		t.Fatalf("被忽略的评论不应产生回帖（含冷却提示共 2 条），got %d", n)
	}
}

// setGitLabNoteEvent 预置一条 GitLab note webhook 事件（SDK 归一化后的 cr.note 形态：
// RawPayload 为 GitLab 原始负载，Actor/Repo/CR 由 SDK 从 payload 填充）。
func (s *stack) setGitLabNoteEvent(noteBody, actor string, system bool) {
	payload := fmt.Sprintf(`{"object_kind":"note",
		"user":{"id":2,"username":%q,"name":%q},
		"project":{"id":1,"path_with_namespace":"acme/app"},
		"object_attributes":{"id":100,"note":%q,"noteable_type":"MergeRequest","system":%t},
		"merge_request":{"iid":7,"title":"login"}}`, actor, actor, noteBody, system)
	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()
	s.fake.ev = &provider.NormalizedEvent{
		ID: "gl-note", Type: "cr.note", Action: "note", Source: "gitlab",
		Actor:      &provider.CRUser{ID: 2, Username: actor, Name: actor},
		Repo:       &provider.EventRepo{Owner: "acme", Name: "app", ID: 1},
		CR:         &provider.ChangeRequest{ID: 7, Number: "7"},
		RawPayload: json.RawMessage(payload),
	}
}
