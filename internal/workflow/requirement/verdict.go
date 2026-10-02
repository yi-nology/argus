// verdict.go —— 判决与门禁面：Verdict/VerdictScoped 判决矩阵 + 仓库配置加载 + diff 路径提取。
package requirement

import (
	"context"

	"git.example.com/enjoydream/agentkit/reportutil"

	"argus/internal/plugin"
)

// VerdictScoped R5 裁决真值表（design §4，按序求值首条命中即短路），scope 感知
// 变体：inScope 非 nil 时，file 不在本次变更文件集内的 high 意见不触发 R5.2
// （reviewer 经 get_file 能看到未变更文件，对变更外文件的一票否决越权），降计
// COMMENT 级；返回值为越界条数（0 = 无或未启用；inScope 为 nil 恒 0）。
//
//	R5.1 missing+partial>0 → REQUEST_CHANGES（需求未完成，一票否决）
//	R5.2 存在 high finding → REQUEST_CHANGES
//	R5.3 unverifiable>0 或 Ambiguous>0 或 AC 总数 0 或 scope creep 非空 → COMMENT（人工确认）
//	R5.4 存在 medium/low finding → COMMENT
//	R5.5 其余 → APPROVE
//
// 注意：R5.3 的 Ambiguous/AC 总数条件取自 R1 的 spec（而非 R3 report）——R3 失败/缺席时
// 空清单与模糊需求仍必须落入人工确认路径，不能漏进 R5.5（二轮评审修掉的注入洞）。
func VerdictScoped(spec *plugin.RequirementSpec, report *plugin.ComplianceReport, findings []plugin.Finding,
	inScope map[string]bool) (verdict, rule string, outOfScope int) {
	var high, midLow, unverifiable int
	for _, f := range findings {
		switch f.Severity {
		case reportutil.High:
			// scope 闸门：越界 high 不进 R5.2 计数，但保留为 COMMENT 级意见
			if inScope != nil && f.File != "" && !inScope[f.File] {
				outOfScope++
				midLow++
				continue
			}
			high++
		case reportutil.Medium, reportutil.Low:
			midLow++
		}
	}
	missingPartial := false
	if report != nil {
		for _, c := range report.Checks {
			switch c.Status {
			case plugin.ACMissing, plugin.ACPartial:
				missingPartial = true
			case plugin.ACUnverifiable:
				unverifiable++
			}
		}
	}
	if missingPartial {
		return VerdictRequestChanges, "R5.1", outOfScope
	}
	if high > 0 {
		return VerdictRequestChanges, "R5.2", outOfScope
	}
	needHuman := unverifiable > 0 || (report != nil && len(report.ScopeCreep) > 0)
	if spec != nil { // requirement 模式：模糊需求 / 空清单强制人工确认
		needHuman = needHuman || len(spec.Ambiguous) > 0 || len(spec.ACs) == 0
	}
	if needHuman {
		return VerdictComment, "R5.3", outOfScope
	}
	if midLow > 0 {
		return VerdictComment, "R5.4", outOfScope
	}
	return VerdictApprove, "R5.5", outOfScope
}

// loadRepoConfig 仓库主权配置（§3.4：只从默认分支读；失败按无配置 + 告警）。
// runner 注入的 RepoConfig（v3.15.0）优先：diff 过滤与 workflow 共用一次加载结果。
func (w *Workflow) loadRepoConfig(ctx context.Context, task plugin.Task) *plugin.RepoConfig {
	if w.RepoConfig != nil {
		return w.RepoConfig
	}
	pr := task.PR
	if pr.DefaultBranch == "" {
		repo, err := w.SDK.GetRepo(ctx, pr.Platform, pr.Owner, pr.Repo)
		if err != nil {
			w.Log.Warn("argus.workflow.repo_fetch_failed", "error", err.Error())
			return plugin.EmptyRepoConfig()
		}
		pr.DefaultBranch = repo.DefaultBranch
	}
	rc, err := w.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := plugin.ResolveRepoConfig(rc, w.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			w.Log.Warn("argus.workflow.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

func diffPaths(dc *plugin.DiffContext) []string {
	if dc == nil {
		return nil
	}
	out := make([]string, 0, len(dc.Files))
	for _, f := range dc.Files {
		out = append(out, f.Path)
	}
	return out
}
