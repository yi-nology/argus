// authz_repo_matrix_test.go —— 跨仓 403 回归矩阵（P0-02，#3）：
// 持仓库约束 token 的调用方，约束外仓库的数据面（reports/stats/usage/
// repo-settings/repos-map/suppress）必须拒绝或行过滤；约束内正常放行。
// 与 authz_probe_test.go（匿名 401 探针）互补：本文件管「已认证但越仓」，
// 两者合起来钉住 BearerMiddleware 后的仓库维度单点判定。
package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/service"
	"github.com/yi-nology/argus/internal/storage"
)

// seedDoneTask 直插 done 任务（报告读取矩阵的种子）。
func seedDoneTask(t *testing.T, s *stack, platform, owner, repo, sha string) *storage.Task {
	t.Helper()
	id := storage.NewTaskID()
	task := &storage.Task{
		ID: id, Kind: "code_review", Platform: platform, Owner: owner, Repo: repo,
		Number: "1", HeadSHA: sha, Status: storage.StatusDone, Verdict: "approve",
		Report: "报告-" + owner + "/" + repo, TokensUsed: 100,
		CreatedAt: time.Now(),
	}
	if err := s.store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

// authzGet/authzPost 带 Bearer 的探针请求，返回状态码与响应体。
func authzDo(t *testing.T, s *stack, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(respBody)
}

// TestAuthzRepoDenialMatrix 跨仓 403/行过滤矩阵：read@myorg token 对 other 仓
// 的报告/统计/用量/地图/配置全部拒绝或剔除；对 myorg 仓正常。admin@myorg token
// 对 other 仓的写面（suppress/repo-settings PUT）同样拒绝。
func TestAuthzRepoDenialMatrix(t *testing.T) {
	s := newStack(t, stackOpts{
		apiTokens: "tok-r:read@gitea:myorg/*,tok-a:admin@gitea:myorg/*",
	})
	seedDoneTask(t, s, "gitea", "myorg", "app", "aaaaaaaaaaaaaaaaaaaa")
	seedDoneTask(t, s, "gitea", "other", "secret", "bbbbbbbbbbbbbbbbbbbb")

	t.Run("reports_by_ref_cross_repo_denied", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/reports/bbbbbbbbbbbbbbbbbbbb", "tok-r", "")
		if code != http.StatusForbidden {
			t.Fatalf("跨仓报告读取 = %d（期望 403；修复前 SHA 全局寻址无仓库约束=泄露）", code)
		}
		code, body := authzDo(t, s, http.MethodGet, "/reports/aaaaaaaaaaaaaaaaaaaa", "tok-r", "")
		if code != http.StatusOK || !strings.Contains(body, "报告-myorg/app") {
			t.Fatalf("约束内报告读取 = %d body=%s（期望 200）", code, body)
		}
	})

	t.Run("stats_overview_rows_filtered", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/stats/overview", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("stats = %d", code)
		}
		if strings.Contains(body, `"other"`) || strings.Contains(body, "secret") {
			t.Fatalf("约束外仓库行透出: %s", body)
		}
		if !strings.Contains(body, `"myorg"`) {
			t.Fatalf("约束内仓库行缺失: %s", body)
		}
	})

	t.Run("usage_monthly_rows_filtered", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/usage/monthly", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("usage = %d", code)
		}
		if strings.Contains(body, `"other"`) {
			t.Fatalf("约束外仓库用量透出: %s", body)
		}
	})

	t.Run("repo_map_cross_repo_denied", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/repos/map?platform=gitea&owner=other&repo=secret", "tok-r", "")
		if code != http.StatusForbidden {
			t.Fatalf("跨仓代码地图 = %d（期望 403）", code)
		}
		code, _ = authzDo(t, s, http.MethodGet, "/repos/map?platform=gitea&owner=myorg&repo=app", "tok-r", "")
		if code == http.StatusForbidden {
			t.Fatalf("约束内代码地图被误拒（403）")
		}
	})

	t.Run("repo_settings_read_write_gated", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/repo-settings/gitea/other/secret", "tok-r", "")
		if code != http.StatusForbidden {
			t.Fatalf("跨仓配置视图 = %d（期望 403）", code)
		}
		code, _ = authzDo(t, s, http.MethodPut, "/repo-settings/gitea/other/secret", "tok-a", `{"monthly_token_budget": 100}`)
		if code != http.StatusForbidden {
			t.Fatalf("跨仓配置写入 = %d（期望 403；admin 档仓库维度）", code)
		}
		code, body := authzDo(t, s, http.MethodPut, "/repo-settings/gitea/myorg/app", "tok-a", `{"monthly_token_budget": 100}`)
		if code != http.StatusOK {
			t.Fatalf("约束内配置写入 = %d body=%s（期望 200）", code, body)
		}
		code, _ = authzDo(t, s, http.MethodGet, "/repo-settings/gitea/myorg/app", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("约束内配置视图 = %d（期望 200）", code)
		}
	})

	t.Run("suppress_cross_repo_denied", func(t *testing.T) {
		body := `{"platform":"gitea","owner":"other","repo":"secret","number":"1","fingerprint":"abcdef123456"}`
		code, _ := authzDo(t, s, http.MethodPost, "/findings/suppress", "tok-a", body)
		if code != http.StatusForbidden {
			t.Fatalf("跨仓意见抑制 = %d（期望 403）", code)
		}
	})

	t.Run("tasks_list_rows_filtered", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/tasks", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("tasks = %d", code)
		}
		if strings.Contains(body, `"other"`) {
			t.Fatalf("约束外任务清单透出: %s", body)
		}
		if !strings.Contains(body, `"myorg"`) {
			t.Fatalf("约束内任务缺失: %s", body)
		}
	})

	// 无仓库约束 token 不受行过滤（向后兼容语义不回退）
	t.Run("unconstrained_token_sees_all", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/stats/overview", "e2e-token", "")
		if code != http.StatusOK {
			t.Fatalf("stats = %d", code)
		}
		if !strings.Contains(body, `"other"`) {
			t.Fatalf("无约束 token 应见全量仓库: %s", body)
		}
	})
}

// TestAuthzMatrixCoversIDL 防漂移门禁（P0-02）：IDL 声明的每条非白名单 🔒 路由
// 必须在 routeAuthzMatrix 有授权门行——新增路由漏配授权门（读面漏接仓库约束
// 的历史形态）在本测试立即红。
func TestAuthzMatrixCoversIDL(t *testing.T) {
	routes := idlProtectedRoutes(t)
	matrix := service.AuthzMatrix()
	idx := map[string]string{}
	for _, r := range matrix {
		idx[r.Method+" "+r.Path] = r.Gate
	}
	missing := []string{}
	for _, r := range routes {
		if _, ok := idx[r.method+" "+r.path]; !ok {
			missing = append(missing, r.method+" "+r.path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("IDL 路由缺授权门矩阵行（读面漏接仓库约束的形态）: %v", missing)
	}
	// 矩阵行不得为死行（IDL 删路由忘删矩阵行同样漂移）
	idlIdx := map[string]bool{}
	for _, r := range routes {
		idlIdx[r.method+" "+r.path] = true
	}
	for k, gate := range idx {
		if gate != service.AuthzGateExempt && !idlIdx[k] {
			t.Fatalf("矩阵行 %s（gate=%s）不在 IDL 路由清单中（死行）", k, gate)
		}
	}
}
