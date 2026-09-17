package e2e

import (
	"context"
	"strings"
	"testing"

	"argus/internal/plugin"
)

// 场景21：best-of-N 同质采样（v3.10.0）——同一插件 reviewN=3，两次采样报同一
// medium 意见、第三次无发现：复现 2/3 把 medium 升级为 high 驱动 R5.2（无采样
// 时 medium 只落 R5.4 COMMENT），报告与行内评论带「采样复现 2/3」标注。
// 复现计数是确定性信号（指纹匹配），不经任何模型——与裁决不走 LLM 铁律一致。
func TestScenario21_BestOfNReplicationBump(t *testing.T) {
	finding := `{"findings":[{"file":"main.go","line":3,"severity":"medium","comment":"并发写共享 map 未加锁"}],"notes":""}`
	empty := `{"findings":[],"notes":""}`
	s := newStack(t, stackOpts{
		extraAgents: []plugin.AgentDecl{{
			Name: "concurrency", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview}, Needs: []string{"diff"},
			Cost: plugin.CostStandard, Timeout: "60s", ReviewN: 3,
		}},
		personaSeq: map[string][]string{
			"【Argus concurrency】": {finding, finding, empty},
		},
	})
	taskID := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, taskID, "done")
	if task.Verdict != "request_changes" {
		t.Fatalf("复现升级应驱动 R5.2 request_changes，got %s", task.Verdict)
	}
	if !strings.Contains(task.Report, "采样复现 2/3") {
		t.Fatalf("报告缺复现标注:\n%s", task.Report)
	}
	_, _, reviews, _ := s.fake.snapshot()
	if len(reviews) != 1 || len(reviews[0].Comments) != 1 {
		t.Fatalf("复现意见应产生 1 条行内评论: %+v", reviews)
	}
	if !strings.Contains(reviews[0].Comments[0].Body, "采样复现 2/3") {
		t.Fatalf("行内评论缺复现标注: %q", reviews[0].Comments[0].Body)
	}
}
