// service_reposettings.go —— 服务侧按仓配置 API（v3.187.0）：repo_settings 表的
// 管理/读取面。写入走 admin 档（requiredScope 收口：PUT/DELETE /repo-settings/*），
// 值经 reposettings.Parse 校验后落规范 JSON；读面返回「按仓覆盖 + 全局生效值」
// 合并视图——部署方/外部 AI 一次看清某仓现在到底按什么参数在跑。

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oagentkit/obs/audit"

	"github.com/yi-nology/argus/internal/reposettings"
	"github.com/yi-nology/argus/internal/storage"
)

// 审计动作（service.go 常量同源）。
const (
	auditRepoSettingsUpdated audit.Action = "repo.settings_updated"
	auditRepoSettingsDeleted audit.Action = "repo.settings_deleted"
)

// validRepoSettingsPlatform platform 必须是部署装配过的平台（cfg.Platforms 是
// 权威词表——没装配的平台不可能有任务，配了也没消费面）。
func (s *Service) validRepoSettingsPlatform(platform string) bool {
	if platform == "" {
		return false
	}
	_, ok := s.Cfg.Platforms[platform]
	return ok
}

// validateRepoSettingsIdentity 仓库三元组校验（owner/repo 词法与 suppress 同口径）。
func validateRepoSettingsIdentity(platform, owner, repo string) error {
	if owner == "" || repo == "" {
		return errors.New("owner/repo 不能为空")
	}
	if len(owner) > 128 || len(repo) > 128 {
		return errors.New("owner/repo 过长（上限 128）")
	}
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return errors.New("owner/repo 含非法字符")
	}
	return nil
}

// RepoSettingsView 单仓配置视图：found（是否有按仓覆盖）+ settings（覆盖集）
// + effective（生效值，含全局默认派生）。无覆盖时 effective 即全局默认——观测面
// 一次看清该仓当前真实参数。
func (s *Service) RepoSettingsView(ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	effective := map[string]int{
		"monthly_token_budget": s.Cfg.RepoMonthlyTokenBudget,
		"task_token_budget":    s.Cfg.EffectiveTaskTokenBudget(),
		"max_diff_chars":       s.Cfg.MaxDiffCharsResolved(),
	}
	row, err := s.Store.GetRepoSetting(ctx, platform, owner, repo)
	if err != nil {
		s.log().Error("argus.api.repo_settings_get_failed", "error", err.Error())
		return map[string]any{"error": "查询失败"}, http.StatusInternalServerError
	}
	out := map[string]any{
		"platform": platform, "owner": owner, "repo": repo,
		"found": false, "settings": nil, "effective": effective,
		"source": "global_default",
	}
	if row != nil {
		rs, perr := reposettings.Parse([]byte(row.SettingsJSON))
		if perr != nil {
			// 落库值损坏不应炸读面：降级为默认态并如实标注。
			s.log().Warn("argus.api.repo_settings_corrupt", "error", perr.Error())
			out["corrupt"] = perr.Error()
			return out, http.StatusOK
		}
		if !rs.IsZero() {
			effective["monthly_token_budget"] = rs.MonthlyTokenBudgetOr(effective["monthly_token_budget"])
			effective["task_token_budget"] = rs.TaskTokenBudgetOr(effective["task_token_budget"])
			effective["max_diff_chars"] = rs.MaxDiffCharsOr(effective["max_diff_chars"])
			out["found"] = true
			out["source"] = "repo_override"
		}
		out["settings"] = rs
		out["updated_by"] = row.UpdatedBy
		out["updated_at"] = row.UpdatedAt
	}
	out["effective"] = effective
	return out, http.StatusOK
}

// UpsertRepoSettings 写入单仓覆盖。body 为 JSON（reposettings.Parse 校验）；
// 空覆盖拒收（语义等同 DELETE，用 DELETE 而不是写空对象）。
func (s *Service) UpsertRepoSettings(ctx context.Context, platform, owner, repo string, body []byte) (map[string]any, int) {
	if !s.validRepoSettingsPlatform(platform) {
		return map[string]any{"error": fmt.Sprintf("platform %q 未在部署装配（ARGUS_PLATFORM_*），无消费面", platform)}, http.StatusBadRequest
	}
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	rs, err := reposettings.Parse(body)
	if err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if rs.IsZero() {
		return map[string]any{"error": "空覆盖无意义：不设置任何旋钮请用 DELETE 移除按仓配置"}, http.StatusBadRequest
	}
	norm, err := rs.Normalize()
	if err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	row := &storage.RepoSetting{
		Platform: platform, Owner: owner, Repo: repo,
		SettingsJSON: string(norm), UpdatedBy: TokenFPFromContext(ctx),
		UpdatedAt: time.Now(),
	}
	if err := s.Store.UpsertRepoSetting(ctx, row); err != nil {
		s.log().Error("argus.api.repo_settings_upsert_failed", "error", err.Error())
		return map[string]any{"error": "写入失败"}, http.StatusInternalServerError
	}
	s.Audit.Log(auditRepoSettingsUpdated, "platform", platform, "owner", owner,
		"repo", repo, "settings", string(norm), "token_fp", row.UpdatedBy)
	view, code := s.RepoSettingsView(ctx, platform, owner, repo)
	if code != http.StatusOK {
		// 写成功但视图失败：如实返回写入完成（视图是读面问题）。
		return map[string]any{"written": true, "settings_json": string(norm)}, http.StatusOK
	}
	return view, code
}

// DeleteRepoSettings 移除单仓覆盖（幂等：无配置也 200，附 removed 标记）。
func (s *Service) DeleteRepoSettings(ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	removed, err := s.Store.DeleteRepoSetting(ctx, platform, owner, repo)
	if err != nil {
		s.log().Error("argus.api.repo_settings_delete_failed", "error", err.Error())
		return map[string]any{"error": "删除失败"}, http.StatusInternalServerError
	}
	if removed {
		s.Audit.Log(auditRepoSettingsDeleted, "platform", platform, "owner", owner,
			"repo", repo, "token_fp", TokenFPFromContext(ctx))
	}
	return map[string]any{"removed": removed, "source": "global_default"}, http.StatusOK
}

// ListRepoSettings 全部按仓配置（行数=仓库数量级，直接全量；观测面）。
func (s *Service) ListRepoSettings(ctx context.Context) (map[string]any, int) {
	rows, err := s.Store.ListRepoSettings(ctx)
	if err != nil {
		s.log().Error("argus.api.repo_settings_list_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		rs, perr := reposettings.Parse([]byte(r.SettingsJSON))
		if perr != nil {
			items = append(items, map[string]any{
				"platform": r.Platform, "owner": r.Owner, "repo": r.Repo,
				"corrupt": perr.Error(),
			})
			continue
		}
		items = append(items, map[string]any{
			"platform": r.Platform, "owner": r.Owner, "repo": r.Repo,
			"settings": rs, "updated_by": r.UpdatedBy, "updated_at": r.UpdatedAt,
		})
	}
	return map[string]any{"items": items, "total": len(items)}, http.StatusOK
}

// repoMaxDiffCharsFor describe/testgen 等服务侧 diff 消费点的按仓生效值
// （repo_settings 覆盖 → 全局派生兜底）。读失败降级默认（与 loadRepoConfig
// 同纪律：配置面故障不阻断业务）。
func (s *Service) repoMaxDiffCharsFor(ctx context.Context, platform, owner, repo string) int {
	def := s.Cfg.MaxDiffCharsResolved()
	if s.Store == nil {
		return def
	}
	row, err := s.Store.GetRepoSetting(ctx, platform, owner, repo)
	if err != nil || row == nil {
		return def
	}
	rs, perr := reposettings.Parse([]byte(row.SettingsJSON))
	if perr != nil {
		return def
	}
	return rs.MaxDiffCharsOr(def)
}
