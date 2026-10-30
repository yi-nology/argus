package service

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"argus/internal/config"
	"argus/internal/storage"
)

func repoSettingsTestService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "reposettings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := &Service{Cfg: &config.Config{
		Platforms:              map[string]config.PlatformConfig{"gitea": {}},
		RepoMonthlyTokenBudget: 10_000_000,
		TaskTokenBudget:        400_000,
		MaxDiffChars:           120_000,
	}, Store: st}
	return svc, context.Background()
}

func TestRepoSettingsViewDefault(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	view, code := svc.RepoSettingsView(ctx, "gitea", "acme", "web")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if view["found"] != false || view["source"] != "global_default" {
		t.Fatalf("默认态视图错: %v", view)
	}
	eff := view["effective"].(map[string]int)
	if eff["monthly_token_budget"] != 10_000_000 || eff["task_token_budget"] != 400_000 || eff["max_diff_chars"] != 120_000 {
		t.Fatalf("生效值应为全局默认: %v", eff)
	}
}

func TestRepoSettingsUpsertAndEffective(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	body := []byte(`{"monthly_token_budget": 5000000, "task_token_budget": 800000}`)
	view, code := svc.UpsertRepoSettings(ctx, "gitea", "acme", "web", body)
	if code != http.StatusOK {
		t.Fatalf("code = %d, view = %v", code, view)
	}
	if view["found"] != true || view["source"] != "repo_override" {
		t.Fatalf("覆盖视图错: %v", view)
	}
	eff := view["effective"].(map[string]int)
	if eff["monthly_token_budget"] != 5_000_000 || eff["task_token_budget"] != 800_000 {
		t.Fatalf("覆盖未生效: %v", eff)
	}
	if eff["max_diff_chars"] != 120_000 {
		t.Fatalf("未覆盖旋钮应落全局默认: %v", eff)
	}
	// updated_by：无 token 指纹的内部 ctx 为空串（字段存在即可）。
	if _, ok := view["updated_by"]; !ok {
		t.Fatal("视图缺 updated_by")
	}

	// 第二个仓不受影响。
	v2, _ := svc.RepoSettingsView(ctx, "gitea", "acme", "api")
	if v2["found"] != false {
		t.Fatalf("他仓应保持默认态: %v", v2)
	}
}

func TestRepoSettingsUpsertRejects(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	cases := map[string]struct {
		platform, body string
	}{
		"未装配平台":  {"github", `{"max_diff_chars": 1000}`},
		"非法JSON": {"gitea", `not-json`},
		"未知字段":   {"gitea", `{"maxDiffChars": 1000}`},
		"零值":     {"gitea", `{"task_token_budget": 0}`},
		"空覆盖":    {"gitea", `{}`},
	}
	for name, tc := range cases {
		_, code := svc.UpsertRepoSettings(ctx, tc.platform, "acme", "web", []byte(tc.body))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: 应 400，得到 %d", name, code)
		}
	}
	// 身份词法。
	if _, code := svc.UpsertRepoSettings(ctx, "gitea", "ac me", "web", []byte(`{"max_diff_chars":1000}`)); code != http.StatusBadRequest {
		t.Fatal("非法 owner 应 400")
	}
}

func TestRepoSettingsDeleteIdempotent(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	if _, code := svc.UpsertRepoSettings(ctx, "gitea", "acme", "web",
		[]byte(`{"max_diff_chars": 200000}`)); code != http.StatusOK {
		t.Fatal("前置 upsert 失败")
	}
	out, code := svc.DeleteRepoSettings(ctx, "gitea", "acme", "web")
	if code != http.StatusOK || out["removed"] != true {
		t.Fatalf("首删: (%d, %v)", code, out)
	}
	out, code = svc.DeleteRepoSettings(ctx, "gitea", "acme", "web")
	if code != http.StatusOK || out["removed"] != false {
		t.Fatalf("二删应幂等: (%d, %v)", code, out)
	}
	view, _ := svc.RepoSettingsView(ctx, "gitea", "acme", "web")
	if view["found"] != false {
		t.Fatalf("删后应回默认态: %v", view)
	}
}

func TestRepoSettingsList(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	for _, r := range [][3]string{{"gitea", "acme", "web"}, {"gitea", "acme", "api"}} {
		if _, code := svc.UpsertRepoSettings(ctx, r[0], r[1], r[2],
			[]byte(`{"max_diff_chars": 1000}`)); code != http.StatusOK {
			t.Fatalf("upsert %v 失败", r)
		}
	}
	list, code := svc.ListRepoSettings(ctx)
	if code != http.StatusOK || list["total"] != 2 {
		t.Fatalf("List: (%d, %v)", code, list)
	}
	items := list["items"].([]map[string]any)
	if len(items) != 2 || items[0]["repo"] != "api" {
		t.Fatalf("List 排序或内容错: %v", items)
	}
	b, _ := json.Marshal(items[0]["settings"])
	if string(b) != `{"max_diff_chars":1000}` {
		t.Fatalf("settings 规范形态错: %s", b)
	}
}

func TestRepoMaxDiffCharsFor(t *testing.T) {
	svc, ctx := repoSettingsTestService(t)
	if got := svc.repoMaxDiffCharsFor(ctx, "gitea", "acme", "web"); got != 120_000 {
		t.Fatalf("默认应为全局值: %d", got)
	}
	if _, code := svc.UpsertRepoSettings(ctx, "gitea", "acme", "web",
		[]byte(`{"max_diff_chars": 200000}`)); code != http.StatusOK {
		t.Fatal("upsert 失败")
	}
	if got := svc.repoMaxDiffCharsFor(ctx, "gitea", "acme", "web"); got != 200_000 {
		t.Fatalf("覆盖未生效: %d", got)
	}
	if got := svc.repoMaxDiffCharsFor(ctx, "gitea", "acme", "api"); got != 120_000 {
		t.Fatalf("他仓不应被波及: %d", got)
	}
}

// requiredScope 新规则：PUT|DELETE /repo-settings* = admin；PUT 兜底 admin
// （写语义 fail-closed）；GET /repo-settings* = read 兜底。
func TestRequiredScopeRepoSettings(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
	}{
		{"PUT", "/repo-settings/gitea/acme/web", "admin"},
		{"DELETE", "/repo-settings/gitea/acme/web", "admin"},
		{"GET", "/repo-settings/gitea/acme/web", "read"},
		{"GET", "/repo-settings", "read"},
		{"PUT", "/future-write-route", "admin"},
		{"POST", "/tasks", "ci"},
	}
	for _, tc := range cases {
		if got := requiredScope(tc.method, tc.path); got != tc.want {
			t.Fatalf("%s %s: want %s got %s", tc.method, tc.path, tc.want, got)
		}
	}
}
