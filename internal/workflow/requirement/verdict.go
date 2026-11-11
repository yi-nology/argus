// verdict.go —— 判决与门禁面：Verdict/VerdictScoped 判决矩阵 + judgeAndGate
// 总门（完备性裁判/scope 闸门/R5.C 降级门）+ 仓库配置加载 + diff 路径提取。

package requirement

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/textx/reportutil"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
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
		case reportutil.High, reportutil.Critical: // v5.5.0 textx#20 四档：critical 并入一票否决（掉出计数=最严重问题被 R5.5 APPROVE，四档升级实弹踩坑）
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
		emptyAC := len(spec.ACs) == 0
		// 无需求类变更豁免（v3.143.0）：变更**只**落在 docs/tests 路径、零意见、
		// 零模糊时，AC=0 是合法形态（纯文档/纯测试本来就没有可验收需求）而非
		// 可疑形态——不进 R5.3 人工确认，落 R5.5 APPROVE。判定是确定性路径分类，
		// 不信任任何模型自判；带代码变更的 PR 进不了豁免，「空清单绕过 R5.1/R5.3」
		// 的注入洞保持封死。eval-real 实测：生产模型对 docs/tests 变更系统性产出
		// 零 AC，clean-docs/clean-tests 两案被 R5.3 压成 comment（期望 approve）。
		if emptyAC && len(spec.Ambiguous) == 0 && len(findings) == 0 &&
			inScope != nil && len(inScope) > 0 && allBenignPaths(inScope) {
			emptyAC = false
		}
		needHuman = needHuman || len(spec.Ambiguous) > 0 || emptyAC
	}
	if needHuman {
		return VerdictComment, "R5.3", outOfScope
	}
	if midLow > 0 {
		return VerdictComment, "R5.4", outOfScope
	}
	return VerdictApprove, "R5.5", outOfScope
}

// benignPathClasses 无需求类路径（docs/tests）判定：纯文档与纯测试变更天然
// 没有可验收需求清单。只看路径形态（扩展名/目录约定），零模型参与。
func benignPath(p string) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"),
		strings.HasSuffix(lower, ".rst"), strings.HasSuffix(lower, ".txt"):
		return true
	case strings.HasPrefix(base, "readme"), base == "license", base == "changelog",
		base == "authors", base == "contributors", base == "notice":
		return true
	case strings.HasPrefix(lower, "docs/"), strings.Contains(lower, "/docs/"),
		strings.HasPrefix(lower, "documentation/"), strings.Contains(lower, "/documentation/"):
		return true
	case strings.HasSuffix(lower, "_test.go"), strings.HasSuffix(lower, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(lower, ".py"), // pytest 惯例
		strings.HasSuffix(lower, ".test.js"), strings.HasSuffix(lower, ".test.ts"),
		strings.HasSuffix(lower, ".test.jsx"), strings.HasSuffix(lower, ".test.tsx"),
		strings.HasSuffix(lower, ".test.mjs"), strings.HasSuffix(lower, ".test.cjs"),
		strings.HasSuffix(lower, ".spec.js"), strings.HasSuffix(lower, ".spec.ts"),
		strings.HasSuffix(lower, ".spec.jsx"), strings.HasSuffix(lower, ".spec.tsx"),
		strings.HasSuffix(lower, "_test.sh"), strings.HasSuffix(lower, "_test.bash"),
		strings.HasSuffix(lower, "_test.rb"), strings.HasSuffix(lower, "test.java"):
		return true
	case strings.HasPrefix(lower, "test/"), strings.HasPrefix(lower, "tests/"),
		strings.Contains(lower, "/test/"), strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "/testdata/"), strings.HasPrefix(lower, "testdata/"):
		return true
	}
	return false
}

// allBenignPaths 变更文件集是否全部为无需求类（空集 = false，保守）。
func allBenignPaths(files map[string]bool) bool {
	if len(files) == 0 {
		return false
	}
	for p := range files {
		if !benignPath(p) {
			return false
		}
	}
	return true
}

// loadRepoConfig 仓库主权配置（§3.4：只从默认分支读；失败按无配置 + 告警）。
// runner 注入的 RepoConfig（v3.15.0）优先：diff 过滤与 workflow 共用一次加载结果。
func (w *Workflow) loadRepoConfig(ctx context.Context, task plugin.Task) *plugin.RepoConfig {
	if w.RepoConfig != nil {
		return w.RepoConfig
	}
	pr := task.PR
	branch, err := w.SDK.ResolveBranch(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_fetch_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	pr.DefaultBranch = branch
	rc, err := w.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, branch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := dispatch.ResolveRepoConfig(rc, w.Dispatcher.Registry()); len(warns) > 0 {
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

// judgeAndGate R5 确定性裁决与全部门：完备性裁判 → scope 闸门 → 真值表 →
// R5.0 空产出守门 → SAST 失败注明 → R5.C 完备性降级门 → 增量审查范围透出。
// merged 的 Degraded 按原顺序追加。
func (w *Workflow) judgeAndGate(task plugin.Task, spec *plugin.RequirementSpec,
	report *plugin.ComplianceReport, merged *plugin.MergedFindings, sel []dispatch.Selection,
	outcomes []dispatch.Outcome, pruned []string, retryNotes []string) (verdict, rule string, completeness *plugin.CompletenessReport) {
	w.onStage(plugin.LayerR5)

	// G4 完备性裁判（v3.11.0）：确定性检查「审得全不全」（插件缺失/截断/AC 漏判/
	// 降级审查）。失败项经 Output.Completeness 进报告独立「完备性检查」段
	//（不并入 Merged.Degraded，避免与插件降级说明重复）；demote 策略在 R5 后降级。
	completeness = JudgeCompleteness(sel, outcomes, pruned, task, spec, report)
	merged.Degraded = append(merged.Degraded, retryNotes...)

	// R5：确定性真值表（按序求值，首条命中即短路；模型自评 verdict 不参与）。
	// v3.8.0：有 diff 文件集时启用 scope 闸门——reviewer 经 get_file 看到未变更
	// 文件后对本次变更之外的文件报 high，此前可直接驱动 R5.2 REQUEST_CHANGES
	//（一票否决了与本次变更无关的意见）；现在越界 high 只计入 COMMENT 级并注明。
	r5Start := time.Now()
	var scope map[string]bool
	if task.Diff != nil && len(task.Diff.Files) > 0 {
		scope = make(map[string]bool, len(task.Diff.Files))
		for _, f := range task.Diff.Files {
			// 仓库级排除文件（v3.15.0 RepoExcluded）不进审查视野（diff 文本不含），
			// 与「get_file 读到的未变更文件」同属越界——其上的意见不驱动 R5.2
			//（v9 夹具 47 钉住：排除文件上的 high 此前会一票否决，排除形同虚设）。
			if f.RepoExcluded {
				continue
			}
			scope[f.Path] = true
		}
	}
	verdict, rule, outOfScope := VerdictScoped(spec, report, merged.Findings, scope)
	if outOfScope > 0 {
		merged.Degraded = append(merged.Degraded,
			fmt.Sprintf("%d 条意见位于本次变更范围之外（get_file 读到的未变更文件），未计入高危裁决", outOfScope))
	}
	plugin.ReportStageDuration(plugin.LayerR5, time.Since(r5Start))
	plugin.ReportLayerExec(plugin.LayerR5)

	// R5.0 守门（深挖 P0）：零插件实际产出时禁入 R5.5 APPROVE。skills 词表不匹配/
	// 插件全被禁用/全数熔断/跳过时，空 findings 是"没人审"而不是"没问题"——
	// 静默 APPROVE 会把未审查的变更放进主干。降级为 COMMENT + Degraded 注明。
	// SAST 前置产出不计入 ran（它不是审查 agent，不能把"只有 gitleaks 跑了"
	// 洗成"有人审过"）。
	sastNames := make(map[string]bool, len(w.SASTOutcomes))
	for _, o := range w.SASTOutcomes {
		sastNames[o.Name] = true
	}
	ran := 0
	for _, o := range outcomes {
		if sastNames[o.Name] {
			continue
		}
		if o.SkippedReason == "" && o.Err == nil {
			ran++
		}
	}
	if ran == 0 {
		merged.Degraded = append(merged.Degraded,
			"无任何审查插件实际产出（全被禁用/熔断/跳过，原因见上方 Degraded）——本结论在未审查状态下给出，仅作参考")
		if verdict == VerdictApprove {
			verdict, rule = VerdictComment, "R5.0"
		}
	}
	// SAST 失败降级注明（产出空 ≠ 扫描成功；覆盖事实必须可见）。
	for _, o := range w.SASTOutcomes {
		if o.Err != nil {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("SAST %s 扫描失败：%s（审查继续）", o.Name, logredact.Redact(o.Err.Error())))
		}
	}
	// G4 降级门（opt-in demote）：完备性检查不过时不允许白给 APPROVE（v3.11.0）。
	// 这是 R5.0 精神向「部分覆盖」的延伸：R5.0 管全缺，R5.C 管部分缺失/截断/漏判。
	if !completeness.OK && w.CompletenessPolicy == plugin.CompletenessDemote && verdict == VerdictApprove {
		verdict, rule = VerdictComment, "R5.C"
	}
	plugin.ReportVerdict(task.PR.Platform, task.Kind, verdict, rule)
	// 增量审查范围透出（v3.8.0）：AlreadyReviewed 文件未重审此前零痕迹——
	// 与大 PR 截断同构的"覆盖范围收缩"，结论可信度的一部分必须可见。
	if task.Diff != nil {
		reviewed := 0
		for _, f := range task.Diff.Files {
			if f.AlreadyReviewed {
				reviewed++
			}
		}
		if reviewed > 0 {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("增量审查：%d 个文件与上轮完全一致未重审，本次仅覆盖其余变更", reviewed))
		}
	}
	// 大 PR 截断透出（深挖 P0 → v3.11.0 由完备性裁判 CheckDiffTruncated 承接，
	// 注记统一从 completeness.DegradedNotes() 产出，此处不再重复）。
	return verdict, rule, completeness
}
