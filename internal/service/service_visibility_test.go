package service

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"argus/internal/config"
	"argus/internal/storage"
)

// openVisStore 可见性测试用真 Store（sqlite 临时库）。
func openVisStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "vis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestTaskVisibility 数据可见性隔离（v3.140.0）：token 仓库域约束覆盖 task_id
// 维度读取——域外任务 403/不存在语义，域内正常；local 任务按持有 id 放行；
// 清单面更严（local 不列）。
func TestTaskVisibility(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	ctx := context.Background()

	mk := func(kind, platform, owner, repo, number string) string {
		task := storage.NewTask(kind, platform, owner, repo, number)
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	inID := mk("code_review", "gitlab", "demo-user", "argus-validate", "2")
	outID := mk("code_review", "gitlab", "obs", "demo-insights-server", "9")
	localID := mk("diff_preview", "local", "", "", "")

	scoped := ContextWithTokenRepos(ctx, "gitlab:demo-user/*")
	free := ctx

	// 域内任务：放行
	if _, code := s.GetTask(scoped, inID); code != http.StatusOK {
		t.Errorf("域内任务应 200, got %d", code)
	}
	// 域外任务：403
	if _, code := s.GetTask(scoped, outID); code != http.StatusForbidden {
		t.Errorf("域外任务应 403, got %d", code)
	}
	// local 任务：持有 id 放行（预审闭环）
	if _, code := s.GetTask(scoped, localID); code != http.StatusOK {
		t.Errorf("local 任务按持有 id 应 200, got %d", code)
	}
	// 无约束 token：全放行
	if _, code := s.GetTask(free, outID); code != http.StatusOK {
		t.Errorf("无约束应 200, got %d", code)
	}
	// 清单面：域外与 local 都不列
	resp, code := s.ListTasks(scoped, "", 50)
	if code != http.StatusOK {
		t.Fatalf("清单应 200: %d", code)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].TaskId != inID {
		t.Errorf("scoped 清单应只剩域内 1 条: %d", len(resp.Tasks))
	}
	resp2, _ := s.ListTasks(free, "", 50)
	if len(resp2.Tasks) != 3 {
		t.Errorf("无约束清单应 3 条: %d", len(resp2.Tasks))
	}
	// Copilot 面：域外任务按不存在处理
	if _, ok := s.TaskStages(scoped, outID); ok {
		t.Error("域外任务 TaskStages 应不存在语义")
	}
	if _, ok := s.TaskStages(scoped, inID); !ok {
		t.Error("域内任务 TaskStages 应可见")
	}
	if _, ok := s.Findings(scoped, outID); ok {
		t.Error("域外任务 Findings 应不存在语义")
	}
}
