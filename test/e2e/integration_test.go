package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/provider"
)

// 真实 LLM 集成测试（不打桩模型，平台仍打桩）。
// 运行：cd lacus/argus && ARGUS_IT=1 go test ./test/e2e/ -run TestIntegration -v
// 依赖 lacus/.env 的 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL（mimo-v2.5 需 MaxTokens>=4096）。

var issueFixture = provider.Issue{
	Number: "12",
	Title:  "用户登录功能",
	Body: "需求描述：\n1. 支持账号密码登录（AC-1）：提供 login(username, password) 能力，用户名或密码为空时拒绝登录。\n" +
		"2. 登录失败保护（AC-2）：连续登录失败 3 次后锁定账号 10 分钟。\n" +
		"验收方式：代码评审 + 单元测试。",
	Author: &provider.CRUser{Username: "alice"},
	Labels: []string{"feature"},
}

// resultRule 从 result_json 提取命中的 R5 规则。
func resultRule(resultJSON string) string {
	var p struct {
		Rule string `json:"rule"`
	}
	_ = json.Unmarshal([]byte(resultJSON), &p)
	return p.Rule
}

func TestIntegration_RealLLM_RequirementCompliance(t *testing.T) {
	if os.Getenv("ARGUS_IT") != "1" {
		t.Skip("跳过真实 LLM 集成测试（设 ARGUS_IT=1 开启）")
	}
	s := newStack(t, stackOpts{realLLM: true, workers: 0})

	// 真实感数据：需求=登录+失败锁定；diff 只实现了登录（AC-2 缺失 → 预期 R5.1 一票否决）
	s.fake.mu.Lock()
	s.fake.cr.Description = "实现用户登录功能，需求见 issue #12。"
	s.fake.cr.SourceBranch = "feat/login-#12"
	s.fake.diff.Files[0].Diff = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,8 @@\n package main\n+\n+func login(username, password string) bool {\n+\t// TODO: 校验用户名密码\n+\tif password == \"\" {\n+\t\treturn false\n+\t}\n+\treturn true\n+}\n"
	s.fake.diff.Files[0].Additions = 7
	s.fake.issues["12"] = &issueFixture
	s.fake.files["main.go"] = "package main\n\nfunc login(username, password string) bool {\n\tif password == \"\" {\n\t\treturn false\n\t}\n\treturn true\n}\n"
	s.fake.mu.Unlock()

	taskID := s.createTaskViaAPI(t, 0)
	start := time.Now()
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, taskID, "done")

	fmt.Printf("\n========== 真实 LLM 集成结果（耗时 %s）==========\n", time.Since(start).Round(time.Second))
	fmt.Printf("verdict=%s rule=%s tokens=%d\n", task.Verdict, resultRule(task.ResultJSON), task.TokensUsed)
	fmt.Println("---------- PR 回帖报告 ----------")
	fmt.Println(task.Report)
	fmt.Println("--------------------------------")

	// 结构断言（真实模型有随机性，断言链路完整产出而非具体结论）
	if task.Verdict == "" {
		t.Fatal("真实 LLM 全链路未产出 verdict")
	}
	if !strings.Contains(task.Report, "Argus 需求复核报告") {
		t.Fatal("报告头缺失")
	}
	created, _, _, _ := s.fake.snapshot()
	if created != 1 {
		t.Fatalf("应回帖 1 次，got %d", created)
	}
}

// 附加：code_review-only（无需求清单）真实 LLM 路径。
func TestIntegration_RealLLM_CodeReviewOnly(t *testing.T) {
	if os.Getenv("ARGUS_IT") != "1" {
		t.Skip("跳过（设 ARGUS_IT=1 开启）")
	}
	s := newStack(t, stackOpts{realLLM: true})
	s.fake.mu.Lock()
	s.fake.diff.Files[0].Diff = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,1 +1,4 @@\n+import \"os\"\n+func run(cmd string) {\n+\texecCommand(cmd)\n+}\n+\n+// TODO: 实现\n"
	s.fake.mu.Unlock()

	taskID := s.createTaskViaAPI(t, 1) // CODE_REVIEW
	if err := s.runner.Execute(context.Background(), taskID); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, taskID, "done")
	fmt.Printf("\n[code_review-only] verdict=%s rule=%s\n%s\n", task.Verdict, resultRule(task.ResultJSON), task.Report)
	if task.Verdict == "" {
		t.Fatal("未产出 verdict")
	}
}
