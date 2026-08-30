package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
)

// v3.0.1 评审项回归：仓库配置（.reviewagents.yaml）并入 GET /agents 可观测。
// 覆盖：配置原文回显（信任边界规范化后）、各插件生效状态、告警（未知名忽略/锁定禁用拒绝）。
func TestScenario11_AgentsRepoConfigView(t *testing.T) {
	s := newStack(t, stackOpts{})

	// 仓库配置：use 引用未注册名 ocr（→告警+剔除）、disable 锁定插件 security（→拒绝+告警）、
	// routing 引用 frontend-visual（→routed）
	s.fake.mu.Lock()
	s.fake.files[".reviewagents.yaml"] = `use: [requirement-compliance, ocr]
disable: [security]
routing:
  - {paths: ["web/**", "*.vue"], use: [frontend-visual]}
`
	s.fake.mu.Unlock()

	rec := performRequest(s.engine, "GET",
		"/agents?platform=gitea&owner=acme&repo=app", "", []utHeader{
			{Key: "Authorization", Value: "Bearer e2e-token"},
		})
	if rec.Code != 200 {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Agents []struct {
				Name string `json:"name"`
			} `json:"agents"`
			RepoConfig *struct {
				Loaded  bool     `json:"loaded"`
				Use     []string `json:"use"`
				Disable []string `json:"disable"`
				Routing []struct {
					Paths []string `json:"paths"`
					Use   []string `json:"use"`
				} `json:"routing"`
				Warnings []string `json:"warnings"`
				Plugins  []struct {
					Name       string  `json:"name"`
					RepoEffect string  `json:"repoEffect"`
					RoutedBy   *string `json:"routedBy"`
					Note       *string `json:"note"`
				} `json:"plugins"`
			} `json:"repoConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v\n%s", err, rec.Body.String())
	}
	rc := env.Data.RepoConfig
	if rc == nil || !rc.Loaded {
		t.Fatalf("repoConfig 应存在且 loaded=true: %+v", rc)
	}

	// 信任边界规范化回显：未知名（ocr，本栈未注册 cli 插件）剔除、disable 中锁定项剔除
	if fmt.Sprint(rc.Use) != "[requirement-compliance]" {
		t.Fatalf("use 回显（规范化后）错误: %v", rc.Use)
	}
	if len(rc.Disable) != 0 {
		t.Fatalf("disable 中的锁定项应被剔除: %v", rc.Disable)
	}
	if len(rc.Routing) != 1 || len(rc.Routing[0].Paths) != 2 {
		t.Fatalf("routing 回显错误: %+v", rc.Routing)
	}

	// 各插件生效状态
	effect := map[string]string{}
	notes := map[string]string{}
	for _, p := range rc.Plugins {
		effect[p.Name] = p.RepoEffect
		if p.Note != nil {
			notes[p.Name] = *p.Note
		}
	}
	if effect["security"] != "locked" || notes["security"] == "" {
		t.Fatalf("security 应 locked 且有拒绝备注: %+v", effect)
	}
	// ocr 未注册 → 只进告警、不进插件列表（可观测语义：视图只覆盖已注册插件）
	for _, name := range []string{"ocr"} {
		if _, ok := effect[name]; ok {
			t.Fatalf("未注册插件 %s 不应出现在 plugins 视图", name)
		}
	}
	if effect["correctness"] != "excluded_by_use" {
		t.Fatalf("correctness 应 excluded_by_use: %v", effect)
	}
	if effect["frontend-visual"] != "routed" {
		t.Fatalf("frontend-visual 应 routed: %v", effect)
	}

	// 告警：未知名（ocr）+ 锁定拒绝（security）
	foundUnknown, foundLock := false, false
	for _, w := range rc.Warnings {
		if contains(w, "ocr") {
			foundUnknown = true
		}
		if contains(w, "security") {
			foundLock = true
		}
	}
	if !foundUnknown || !foundLock {
		t.Fatalf("告警缺失: %v", rc.Warnings)
	}

	// 不带 repo 参数：不返回 repoConfig（纯全局清单，向后兼容）
	rec2 := performRequest(s.engine, "GET", "/agents", "", []utHeader{
		{Key: "Authorization", Value: "Bearer e2e-token"},
	})
	if !contains(rec2.Body.String(), `"repoConfig"`) {
		return // 兼容性成立
	}
	t.Fatal("无 repo 参数时不应返回 repoConfig")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
