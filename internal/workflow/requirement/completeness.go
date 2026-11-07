// completeness.go —— R5 完备性裁判判定（v3.11.0，G4，对应 REPORT.md「是否审完」缺口）。
//
// 职责分界：R5 真值表管「结论对不对」，本文件管「审得全不全」——插件超时/熔断
// 降级、diff 截断、预算裁剪、R3 unverifiable 堆积、AC 漏判这些「乐观截断」，
// R5 此前不会拦截 APPROVE（R5.0 只管零插件全缺的极端形态）。
//
// 铁律对齐：全部检查是确定性 Go 代码（读运行时事实，不调模型）；产出是结构化
// 完备性报告，动作只有三种且都不直接改写 findings——附注（annotate，默认）、
// 降级门（demote，配置开启时 APPROVE→COMMENT，rule=R5.C）、受限补审（retry，
// 配置开启时对失败插件重试一次，由 workflow 在 fan-out 后执行）。
package requirement

import (
	"fmt"
	"sort"
	"strings"

	"argus/internal/dispatch"
	"argus/internal/plugin"
)

// unverifiableRatioThreshold unverifiable 占比判定阈值（过半 = 大面积不可判定）。
const unverifiableRatioThreshold = 0.5

// JudgeCompleteness 完备性检查（纯函数，确定性）。
//   - sel：本轮选中插件（lock 标识来源）；
//   - outcomes：fan-out（含受限补审后）的最终结果；
//   - pruned：预算裁剪名单（Dispatcher.PruneByBudget 返回）；
//   - spec/report：R1/R3 产物（可为 nil——nil 本身是覆盖缺口信号）。
func JudgeCompleteness(sel []dispatch.Selection, outcomes []dispatch.Outcome, pruned []string,
	task plugin.Task, spec *plugin.RequirementSpec, report *plugin.ComplianceReport) *plugin.CompletenessReport {

	rep := &plugin.CompletenessReport{OK: true}
	fail := func(check, detail string) {
		rep.OK = false
		rep.Failures = append(rep.Failures, detail)
		rep.Items = append(rep.Items, plugin.CompletenessItem{Check: check, OK: false, Detail: detail})
	}
	pass := func(check, detail string) {
		rep.Items = append(rep.Items, plugin.CompletenessItem{Check: check, OK: true, Detail: detail})
	}

	// 1. diff 截断
	if task.Diff != nil && task.Diff.Truncated {
		fail(plugin.CheckDiffTruncated, "diff 超过上限被截断，本次审查仅覆盖保留部分")
	} else {
		pass(plugin.CheckDiffTruncated, "")
	}

	// 2. 插件未产出（超时/熔断/预算/错误）——R5.0 管全缺，这里管部分缺失
	lockNames := map[string]bool{}
	for _, s := range sel {
		if s.Reg.LockRepoConfig {
			lockNames[s.Reg.Manifest.Name] = true
		}
	}
	var missing, missingLock []string
	for _, o := range outcomes {
		if o.SkippedReason == "" && o.Err == nil {
			continue
		}
		reason := o.SkippedReason
		if reason == "" && o.Err != nil {
			reason = "执行失败"
		}
		missing = append(missing, fmt.Sprintf("%s（%s）", o.Name, reason))
		if lockNames[o.Name] {
			missingLock = append(missingLock, o.Name)
		}
	}
	missing = append(missing, prunedNotes(pruned)...)
	if len(missing) > 0 {
		sort.Strings(missing)
		fail(plugin.CheckPluginsMissing, "以下插件未实际产出："+strings.Join(missing, "、"))
	} else {
		pass(plugin.CheckPluginsMissing, "")
	}
	if len(missingLock) > 0 {
		sort.Strings(missingLock)
		fail(plugin.CheckLockPluginMissing, "lockRepoConfig 锁定插件未产出："+strings.Join(missingLock, "、"))
	} else {
		pass(plugin.CheckLockPluginMissing, "")
	}

	// 3. AC 覆盖 + unverifiable 占比（R3 失败/缺席 = 全部未覆盖）
	if spec != nil && len(spec.ACs) > 0 {
		judged := map[string]bool{}
		if report != nil {
			for _, c := range report.Checks {
				judged[c.ID] = true
			}
		}
		var uncovered []string
		for _, ac := range spec.ACs {
			if !judged[ac.ID] {
				uncovered = append(uncovered, ac.ID)
			}
		}
		if len(uncovered) > 0 {
			fail(plugin.CheckACUncovered, "验收条目未被 R3 判定覆盖："+strings.Join(uncovered, "、"))
		} else {
			pass(plugin.CheckACUncovered, "")
		}
		if report != nil && len(report.Checks) > 0 {
			uv, uvRuntime := 0, 0
			for _, c := range report.Checks {
				if c.Status == plugin.ACUnverifiable {
					uv++
					if strings.HasPrefix(c.Detail, r3RuntimeFailPrefix) {
						uvRuntime++
					}
				}
			}
			if ratio := float64(uv) / float64(len(report.Checks)); ratio > unverifiableRatioThreshold {
				switch {
				case uvRuntime == uv:
					// 全量运行中断：不可验证是假象，重跑即救——此时"拆 PR"建议误导人。
					fail(plugin.CheckUnverifiableRatio, fmt.Sprintf(
						"R3 判定 unverifiable 占比 %.0f%%（%d/%d）——均为本轮运行中断（限流/超时类瞬时故障），直接重跑任务即可，无需拆分 PR",
						ratio*100, uv, len(report.Checks)))
				case uvRuntime > 0:
					fail(plugin.CheckUnverifiableRatio, fmt.Sprintf(
						"R3 判定 unverifiable 占比 %.0f%%（%d/%d）——其中 %d 条系运行中断（重跑可救），其余建议拆分 PR 或补充预览环境",
						ratio*100, uv, len(report.Checks), uvRuntime))
				default:
					fail(plugin.CheckUnverifiableRatio, fmt.Sprintf(
						"R3 判定 unverifiable 占比 %.0f%%（%d/%d）——大面积不可判定，建议拆分 PR 或补充预览环境",
						ratio*100, uv, len(report.Checks)))
				}
			} else {
				pass(plugin.CheckUnverifiableRatio, "")
			}
		}
	}

	// 4. reviewer 自报降级（ReAct 落单轮等——审查强度低于名义配置）
	var degraded []string
	for _, o := range outcomes {
		if o.Result != nil && strings.Contains(o.Result.Notes, plugin.DegradationMarker) {
			degraded = append(degraded, o.Name)
		}
	}
	if len(degraded) > 0 {
		fail(plugin.CheckReviewerDegraded, "以下插件自报审查降级："+strings.Join(degraded, "、"))
	} else {
		pass(plugin.CheckReviewerDegraded, "")
	}

	return rep
}

// prunedNotes 预算裁剪名单转未产出描述（与 outcome 缺失同一口径）。
func prunedNotes(pruned []string) []string {
	out := make([]string, 0, len(pruned))
	for _, name := range pruned {
		out = append(out, fmt.Sprintf("%s（token 预算裁剪）", name))
	}
	return out
}
