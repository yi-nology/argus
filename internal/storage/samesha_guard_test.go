package storage

import (
	"context"
	"testing"
	"time"
)

// 深挖 P0 配套：在飞同 SHA 唯一防线（SQLite 部分唯一索引 + 冲突幂等回查）。
func TestSameSHAGuard(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	active, err := s.EnsureSameSHAGuard()
	if err != nil || !active {
		t.Fatalf("空库应成功建立防线: active=%v err=%v", active, err)
	}

	sha := "abc123def456"
	mk := func(id, eventID, status string) *Task {
		tk := &Task{
			ID: id, Kind: "requirement_compliance", Platform: "gitlab",
			Owner: "acme", Repo: "app", Number: "7", HeadSHA: sha,
			Status: status, CreatedAt: time.Now(),
		}
		if eventID != "" {
			tk.EventID = &eventID
		}
		return tk
	}

	dup, id, err := s.CreateTaskForWebhook(ctx, mk("task-a", "evt-a", StatusPending))
	if err != nil || dup != "" || id != "" {
		t.Fatalf("首个任务应正常建: dup=%q id=%q err=%v", dup, id, err)
	}
	// 单实例纪律：事务内 in-flight 检查拦同 SHA 第二笔
	dup, id, err = s.CreateTaskForWebhook(ctx, mk("task-b", "evt-b", StatusPending))
	if err != nil || dup != "in_flight" || id != "task-a" {
		t.Fatalf("在飞去重应拦: dup=%q id=%q err=%v", dup, id, err)
	}
	// 跨实例纪律：绕过事务检查直插第二条在飞 → 撞部分唯一索引。
	// 方言分支（CI MySQL 矩阵实锤）：部分唯一索引是 SQLite 专有，MySQL 分支
	// EnsureSameSHAGuard 恒 (true, nil) 占位（samesha_guard.go:34 设计在案），
	// 无索引可撞——MySQL 下断言占位语义，跨实例约束不在其契约面。
	if s.isSQLite() {
		err = s.db.Create(mk("task-c", "evt-c", StatusRunning)).Error
		if !isDuplicateKeyErr(err) {
			t.Fatalf("同 SHA 第二条在飞应撞唯一索引: %v", err)
		}
	} else {
		if active, err := s.EnsureSameSHAGuard(); err != nil || !active {
			t.Fatalf("MySQL 分支应恒占位 (true, nil): active=%v err=%v", active, err)
		}
	}
	// 经公开入口再建（4b 事务检查兜住）→ in_flight 语义不变
	dup, id, err = s.CreateTaskForWebhook(ctx, mk("task-d", "evt-d", StatusPending))
	if err != nil || dup != "in_flight" || id != "task-a" {
		t.Fatalf("在飞去重语义不变: dup=%q id=%q err=%v", dup, id, err)
	}
	// done 不受约束：TaskAPI 手工同 SHA 重审合法
	if err := s.db.Create(mk("task-e", "", StatusDone)).Error; err != nil {
		t.Fatalf("done 任务不应受在飞索引约束: %v", err)
	}
	// 防线幂等
	if active, err := s.EnsureSameSHAGuard(); err != nil || !active {
		t.Fatalf("防线应幂等: active=%v err=%v", active, err)
	}
}

// 不同 kind 的任务同 PR 同 SHA 可并存（v3.74.2）：审查 15min 在飞期间
// describe/changelog 应能建任务；last_head_sha（审查语义）不挡归因任务。
func TestDifferentKindsCoexist(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	sha := "sha-coexist-1"
	mk := func(id, kind, status string) *Task {
		return &Task{
			ID: id, Kind: kind, Platform: "gitlab",
			Owner: "acme", Repo: "app", Number: "9", HeadSHA: sha,
			Status: status, CreatedAt: time.Now(),
		}
	}
	if dup, _, err := s.CreateTaskForWebhook(ctx, mk("task-cr", "code_review", StatusPending)); err != nil || dup != "" {
		t.Fatalf("审查任务应建: dup=%q err=%v", dup, err)
	}
	// 同 SHA 同 PR、不同 kind → 不判重
	if dup, _, err := s.CreateTaskForWebhook(ctx, mk("task-ci", "ci_analysis", StatusPending)); err != nil || dup != "" {
		t.Fatalf("ci_analysis 应与在飞审查并存: dup=%q err=%v", dup, err)
	}
	if dup, _, err := s.CreateTaskForWebhook(ctx, mk("task-de", "describe", StatusPending)); err != nil || dup != "" {
		t.Fatalf("describe 应与在飞审查并存: dup=%q err=%v", dup, err)
	}
	// 同 kind 同 SHA → 仍判重
	if dup, id, err := s.CreateTaskForWebhook(ctx, mk("task-cr2", "code_review", StatusPending)); err != nil || dup != "in_flight" || id != "task-cr" {
		t.Fatalf("同 kind 在飞仍应拦: dup=%q id=%q err=%v", dup, id, err)
	}
	// 同 kind 不同 SHA（新 head）→ 不判重（新提交正常建任务）
	t2 := mk("task-cr3", "code_review", StatusPending)
	t2.HeadSHA = "sha-coexist-2"
	if dup, _, err := s.CreateTaskForWebhook(ctx, t2); err != nil || dup != "" {
		t.Fatalf("新 SHA 应正常建: dup=%q err=%v", dup, err)
	}
}
