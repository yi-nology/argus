// service_eventfanout_test.go —— 跨副本事件 fanout 回归（P1-05，#10）：
// 实例 A 发布（Bus+outbox 双写）→ 实例 B 的 fanout 环回灌 → B 的本地 Bus 订阅者
// 收到。进程内 Bus 行为不回退（A 的订阅者即时收到）。
package service

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// TestOutboxFanoutCrossInstance 双 Service（各自 Bus、同一 outbox 表）模拟双副本。
func TestOutboxFanoutCrossInstance(t *testing.T) {
	dsn := "file://" + t.TempDir() + "/fanout.db"
	storeA, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	// 同库第二实例（fanout 读同一 outbox 表）
	storeB, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })

	busA, busB := progress.NewBus(), progress.NewBus()
	svcA := &Service{Bus: busA, Store: storeA, Cfg: nil, Log: testLogger{}}
	svcB := &Service{Bus: busB, Store: storeB, Cfg: nil, Log: testLogger{}}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svcA.StartOutboxFanout(ctx)
	svcB.StartOutboxFanout(ctx)

	chB, cancelB := busB.Subscribe(ctx)
	defer cancelB()
	chA, cancelA := busA.Subscribe(ctx)
	defer cancelA()

	// A 发布：本地即时 + 落 outbox
	svcA.publishProgress(progress.Event{
		Type: progress.EventTaskCreated, TaskID: "t-x", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "9",
	})

	// A 订阅者即时收到（进程内快路径不回退）
	select {
	case ev := <-chA:
		if ev.TaskID != "t-x" {
			t.Fatalf("A 本地事件载荷失真: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("进程内 Bus 快路径未送达")
	}

	// B 订阅者经 fanout 收到（跨实例，≤500ms 轮询 + 余量）
	select {
	case ev := <-chB:
		if ev.TaskID != "t-x" || ev.Type != progress.EventTaskCreated {
			t.Fatalf("跨实例事件载荷失真: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("跨实例事件 3s 未送达（fanout 失效）")
	}

	// 双源去重：A 的事件在 B 落地一次（若 fanout 未去重，B 会收到两份——
	// A 本地广播不含跨实例路径，此处断言 fanout 去重形态：继续等 1s 无重复）
	time.Sleep(1200 * time.Millisecond)
	select {
	case ev := <-chB:
		t.Fatalf("跨实例事件重复投递（去重失效）: %+v", ev)
	default:
	}
}

// TestOutboxFanoutSameInstanceNoDup v6.12 P1 回归：同实例 at-most-once——本地发布
// 已登记共享去重环，fanout 回灌同 ID 必须跳过。评估实锤的形态：publishProgress
// 先 Bus 广播 + 落 outbox，500ms 后 fanout 回读同 ID 事件再次 Publish。
func TestOutboxFanoutSameInstanceNoDup(t *testing.T) {
	dsn := "file://" + t.TempDir() + "/sameinst.db"
	store, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus := progress.NewBus()
	svc := &Service{Bus: bus, Store: store, Cfg: nil, Log: testLogger{},
		EventSeen: progress.NewDedup(0)} // v6.12：共享环注入（修复点）

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc.StartOutboxFanout(ctx)

	ch, cancelSub := bus.Subscribe(ctx)
	defer cancelSub()

	// 本地发布：Bus 广播 + outbox 落库（同实例双写）
	svc.publishProgress(progress.Event{
		Type: progress.EventTaskCreated, TaskID: "t-dup", Platform: "gitea",
		Owner: "o", Repo: "r", Number: "1",
	})

	// 第 1 份：进程内快路径
	select {
	case ev := <-ch:
		if ev.TaskID != "t-dup" {
			t.Fatalf("本地事件载荷失真: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("进程内 Bus 快路径未送达")
	}

	// 关键断言：fanout 回灌同 ID 必须被共享环拦截（at-most-once）——等 ≥1 轮询周期
	time.Sleep(1200 * time.Millisecond)
	select {
	case ev := <-ch:
		t.Fatalf("同实例事件重复投递（EventSeen 共享环失效）: %+v", ev)
	default:
	}
}

// TestAuthorizeEventFilter SSE 事件过滤（P0-02 配套回归：P1-05 事件源不影响可见性）。
func TestAuthorizeEventFilter(t *testing.T) {
	svc := &Service{Cfg: configForRepomap("/tmp", "")}
	ctx := ContextWithTokenRepos(context.Background(), "gitea:myorg/*")
	cases := []struct {
		platform, owner, repo string
		want                  bool
	}{
		{"gitea", "myorg", "app", true},
		{"gitea", "other", "secret", false},
		{"", "", "", true},              // 无仓语义事件放行
		{"local", "", "", true},         // 本地任务与 taskVisible 口径一致
		{"gitlab", "myorg", "b", false}, // pattern 含平台维度：跨平台同仓库组不匹配
	}
	for _, c := range cases {
		if got := svc.AuthorizeEvent(ctx, c.platform, c.owner, c.repo); got != c.want {
			t.Fatalf("AuthorizeEvent(%s/%s/%s)=%v 期望 %v", c.platform, c.owner, c.repo, got, c.want)
		}
	}
}
