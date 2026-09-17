package e2e

import (
	"context"
	"testing"

	"argus/internal/plugin"
)

// v3.0.1 修复回归：Triggers.Events 此前整体失效（Match 对纯 Events 触发器恒 false +
// dispatcher 传空事件类型）。修复后：events 白名单外的 webhook 任务不执行该插件。
func TestScenario22_TriggerEventsFilter(t *testing.T) {
	s := newStack(t, stackOpts{workers: 0})

	// 注册一个只在 cr.opened 参与的 fake 插件（code-review 技能，无超时直通）
	s.reg.Register(&plugin.Registration{
		Manifest: plugin.AgentManifest{
			Name:     "open-only",
			Kind:     plugin.KindBuiltin,
			Skills:   []string{plugin.SkillCodeReview},
			Cost:     plugin.CostFast,
			Timeout:  0, // 默认 120s
			Triggers: plugin.Trigger{Events: []string{"cr.opened"}},
		},
		Plugin:  &countPlugin,
		Enabled: true,
	})

	// ① cr.updated（"推新提交"的归一化形态）：白名单内任务 accepted，但 events
	//    白名单外 → open-only 不应执行
	s.setEvent("synchronize", "sha-s1")
	_, respSync := s.postWebhook(t, "d-sync")
	if respSync.Status != "accepted" {
		t.Fatalf("synchronize 应 accepted: %+v", respSync)
	}
	if err := s.runner.Execute(context.Background(), respSync.TaskId); err != nil {
		t.Fatal(err)
	}
	s.waitForStatus(t, respSync.TaskId, "done")
	if got := countPlugin.invoked; got != 0 {
		t.Fatalf("synchronize 不应触发 open-only 插件，实际执行 %d 次", got)
	}

	// ①b 负控（v3.8.0）：merged 归一化后不在白名单 → skipped。
	// setEvent 现在过 NormalizeCRAction，与真实 SDK 链路一致——此前 fake 绕过
	// 归一化，掩盖了白名单词表脱节（cr.updated 断流）的 P0。
	s.setEvent("merged", "sha-m1")
	_, respMerged := s.postWebhook(t, "d-merged")
	if respMerged.Status != "skipped" {
		t.Fatalf("merged 应 skipped（白名单外）: %+v", respMerged)
	}

	// ② cr.opened：白名单内 → open-only 执行
	s.setEvent("opened", "sha-o1")
	_, respOpen := s.postWebhook(t, "d-open")
	if respOpen.Status != "accepted" {
		t.Fatalf("opened 应 accepted: %+v", respOpen)
	}
	if err := s.runner.Execute(context.Background(), respOpen.TaskId); err != nil {
		t.Fatal(err)
	}
	s.waitForStatus(t, respOpen.TaskId, "done")
	if countPlugin.invoked != 1 {
		t.Fatalf("opened 应触发 open-only 插件 1 次，实际 %d", countPlugin.invoked)
	}

	// ③ CLI 手动任务（无事件类型）：事件条件视为通过 → 执行
	taskID := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	s.waitForStatus(t, taskID, "done")
	if countPlugin.invoked != 2 {
		t.Fatalf("CLI 任务应触发 open-only 插件（事件条件不限制），实际 %d", countPlugin.invoked)
	}
}

// countingPlugin 记录被调用次数。
var countPlugin counting

type counting struct{ invoked int }

func (c *counting) Manifest() plugin.AgentManifest {
	return plugin.AgentManifest{Name: "open-only", Kind: plugin.KindBuiltin}
}

func (c *counting) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	c.invoked++
	return plugin.Result{}, nil
}
