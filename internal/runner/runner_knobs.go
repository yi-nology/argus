// runner_knobs.go —— 任务级按仓旋钮（v3.187.0）：repo_settings 覆盖 → 全局
// 默认的单次加载与合并。Execute 入口读一次（主键点查，失败降级全局默认——
// 配置面故障不阻断审查，与 loadRepoConfig 同纪律），三个生效值随任务体显式
// 传递（Runner 是共享实例，禁止挂实例字段）。
package runner

import (
	"context"

	"argus/internal/reposettings"
	"argus/internal/storage"
)

// taskKnobs 单任务生效旋钮（服务侧 repo_settings 覆盖后的合并结果）。
type taskKnobs struct {
	// monthlyBudget 仓库月度 token 预算闸（repoBudgetGate；0 = 关）。
	monthlyBudget int
	// taskTokenBudget 单任务 token 预算（budget 初值）。
	taskTokenBudget int
	// maxDiffChars diff 总量上限（prepareContext 增量/全量清洗共用）。
	maxDiffChars int
}

// loadTaskKnobs 加载并合并：无覆盖行 / 读失败 / 落库值损坏 一律降级全局默认。
func (r *Runner) loadTaskKnobs(ctx context.Context, task *storage.Task) taskKnobs {
	k := taskKnobs{
		monthlyBudget:   r.Cfg.RepoMonthlyTokenBudget,
		taskTokenBudget: r.Cfg.EffectiveTaskTokenBudget(),
		maxDiffChars:    r.Cfg.MaxDiffCharsResolved(),
	}
	row, err := r.Store.GetRepoSetting(ctx, task.Platform, task.Owner, task.Repo)
	if err != nil {
		r.Log.Warn("argus.runner.knobs_load_failed", "task", task.ID, "error", err.Error())
		return k
	}
	if row == nil {
		return k
	}
	rs, err := reposettings.Parse([]byte(row.SettingsJSON))
	if err != nil {
		r.Log.Warn("argus.runner.knobs_corrupt", "task", task.ID, "error", err.Error())
		return k
	}
	k.monthlyBudget = rs.MonthlyTokenBudgetOr(k.monthlyBudget)
	k.taskTokenBudget = rs.TaskTokenBudgetOr(k.taskTokenBudget)
	k.maxDiffChars = rs.MaxDiffCharsOr(k.maxDiffChars)
	r.Log.Info("argus.runner.knobs_repo_override",
		"task", task.ID, "repo", task.Owner+"/"+task.Repo,
		"monthly_budget", k.monthlyBudget, "task_budget", k.taskTokenBudget,
		"max_diff_chars", k.maxDiffChars)
	return k
}
