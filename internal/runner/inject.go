// inject.go —— 任务前置注入器（OCR delegate / SAST / CodeGraph）与 diff 辅助。
// 与 runner 主编排（Execute/run/finalize）分离：注入器只读工作副本并往
// Task.Artifacts / Workflow 写产物，失败一律告警降级、不阻断审查。
package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/diffx"
	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/textutil"
	"git.example.com/enjoydream/agentkit/workcopy"

	"argus/internal/codegraph"
	"argus/internal/ocrdeleg"
	"argus/internal/plugin"
	"argus/internal/sastscan"
	"argus/internal/workflow/requirement"
)

// reviewGuidelinesPath 仓库级审查指引文件（默认分支根目录）。
const reviewGuidelinesPath = ".argus/review-guidelines.md"

// reviewGuidelinesMaxRunes 指引注入 prompt 的长度上限。
const reviewGuidelinesMaxRunes = 4000

// fetchReviewGuidelines 读取仓库默认分支的审查指引。任何失败（无文件/无权限/
// 网络错误）都视为"无指引"：审查照常进行，不因指引缺失而失败。
func (r *Runner) fetchReviewGuidelines(ctx context.Context, pr *plugin.PRContext) string {
	if pr == nil || pr.DefaultBranch == "" {
		return ""
	}
	body, err := r.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo,
		reviewGuidelinesPath, pr.DefaultBranch)
	if err != nil || strings.TrimSpace(body) == "" {
		return ""
	}
	t, _ := textutil.TruncRunes(body, reviewGuidelinesMaxRunes)
	return strings.TrimSpace(t)
}

// injectOCRDelegate OCR delegate 委托前置（design §2）：dispatch 前在工作副本执行
// `ocr delegate preview/rule`（确定性子命令，OCR 端零 LLM、零凭证），把文件分诊与
// 仓库规则组渲染成提示词 section 注入 Artifacts。仅 code_review 任务生效；未配置
// ARGUS_OCR_BIN、无工作副本池或任何执行失败 → 告警降级，审查照常。
func (r *Runner) injectOCRDelegate(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task) {
	if r.Cfg == nil || r.Cfg.OcrBin == "" || r.WC == nil || ptask.Kind != plugin.TaskKindCodeReview {
		return
	}
	paths := includedPaths(diff)
	if len(paths) == 0 {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		// workcopy 错误链可能携带带凭证 clone URL（git 回显 origin URL），进日志前
		// 统一脱敏——与 §10.2 任务失败信息同纪律
		r.Log.Warn("argus.ocrdeleg.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.WC.Release(key)
	r.injectOCRDelegateInDir(ctx, dir, pr, paths, ptask)
}

// injectOCRDelegateInDir delegate 前置的执行段（dir 由调用方就绪；pool 语义在
// injectOCRDelegate，本函数可独立单测）。
func (r *Runner) injectOCRDelegateInDir(ctx context.Context, dir string, pr *plugin.PRContext,
	paths []string, ptask *plugin.Task) {
	began := time.Now()
	cfg := ocrdeleg.Config{Bin: r.Cfg.OcrBin, Timeout: r.Cfg.OcrTimeout}
	base := pr.BaseBranch
	if base == "" {
		base = pr.DefaultBranch
	}
	tri, err := ocrdeleg.Preview(ctx, cfg, dir, base)
	if err != nil {
		// OCR 在工作树里 shell git，stderr 同样可能回显带凭证 URL——先脱敏再进日志
		r.Log.Warn("argus.ocrdeleg.preview_failed", "error", logredact.Redact(err.Error()))
		return
	}
	// preview 成功而 rule 失败：分诊照常注入，规则缺席只是少一组清单。
	groups, err := ocrdeleg.Rules(ctx, cfg, dir, paths)
	if err != nil {
		r.Log.Warn("argus.ocrdeleg.rules_failed", "error", logredact.Redact(err.Error()))
	}
	if s := ocrdeleg.RenderTriage(tri); s != "" {
		ptask.Artifacts[plugin.ArtifactOCRTriage] = s
	}
	if s := ocrdeleg.RenderRules(groups); s != "" {
		ptask.Artifacts[plugin.ArtifactOCRRules] = s
	}
	// 原始产物留档（人工回溯 OCR 原文；DB 只存合并后 findings）——失败仅告警。
	if r.Cfg.OcrHistoryDir != "" {
		err := ocrdeleg.WriteHistory(r.Cfg.OcrHistoryDir, ocrdeleg.HistoryRecord{
			PR:      worktreeKey(pr).String(),
			Base:    base,
			Preview: tri,
			Rules:   groups,
			Took:    time.Since(began).Round(time.Millisecond).String(),
		})
		if err != nil {
			r.Log.Warn("argus.ocrdeleg.history_failed", "error", logredact.Redact(err.Error()))
		}
	}
}

// injectSAST 确定性 SAST 前置（v3.15.0 P0-3）：工作副本上跑 gitleaks/semgrep，
// 产出作为独立 Outcome 注入 workflow（finish 内并入合并，零 LLM、零任务 token）。
// 未配置 ARGUS_SAST_* 或无工作副本池时静默跳过；扫描失败由 workflow 降级注明。
func (r *Runner) injectSAST(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, wf *requirement.Workflow) {
	if !r.Cfg.SASTEnabled() || r.WC == nil {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		// workcopy 错误链可能携带带凭证 clone URL，进日志前统一脱敏
		r.Log.Warn("argus.sast.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.WC.Release(key)
	base := pr.BaseBranch
	if base == "" {
		base = pr.DefaultBranch
	}
	cfg := sastscan.Config{
		GitleaksBin:   r.Cfg.SASTGitleaksBin,
		SemgrepBin:    r.Cfg.SASTSemgrepBin,
		SemgrepConfig: r.Cfg.SASTSemgrepConfig,
		Timeout:       r.Cfg.SASTTimeout,
	}
	// semgrep 聚焦扫描（v3.21.0）：只扫 diffprep 纳入审查的变更文件——与实际
	// 审查范围一致，避免全仓扫描的时长与信噪比劣化。
	wf.SASTOutcomes = sastscan.Run(ctx, cfg, dir, base, includedPaths(diff), r.Log)
	if len(wf.SASTOutcomes) > 0 {
		r.Log.Info("argus.sast.done", "tools", len(wf.SASTOutcomes))
	}
}

// injectCodeGraph Go 符号索引前置（v3.15.0 P0-1）：变更含 Go 文件时在工作副本上
// 以 go/parser 建索引——Workflow.CodeGraph 供 R3/R4 挂 get_symbol/get_references
// 工具；触达符号（diff hunk 行区间与符号定义的交集）渲染为提示词 section 注入
// Artifacts。索引纯内存，建完即释放工作副本。任何失败告警降级，不阻断审查。
func (r *Runner) injectCodeGraph(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, wf *requirement.Workflow) {
	if !r.Cfg.Codegraph || r.WC == nil {
		return
	}
	if !hasIncludedGoFiles(diff) {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.codegraph.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.WC.Release(key)
	idx, err := codegraph.NewIndex(dir, 2000)
	if err != nil {
		r.Log.Warn("argus.codegraph.index_failed", "error", logredact.Redact(err.Error()))
		return
	}
	wf.CodeGraph = idx
	if s := renderTouchedSymbols(idx, diffx.HunkLines(diff.Text)); s != "" {
		ptask.Artifacts[plugin.ArtifactCodegraphTouched] = s
	}
	r.Log.Info("argus.codegraph.ready", "indexed", idx.IndexedFiles(), "skipped", idx.SkippedFiles())
}

// maxTouchedSymbols 触达符号清单上限（防巨型 PR 的清单吃预算）。
const maxTouchedSymbols = 30

// renderTouchedSymbols 汇总触达符号并渲染提示词 section（含可用性提示与数据区纪律）。
func renderTouchedSymbols(idx *codegraph.Index, ranges map[string][][2]int) string {
	seen := map[string]bool{}
	var lines []string
	for file, rs := range ranges {
		for _, r := range rs {
			for _, s := range idx.TouchedByLines(file, r[0], r[1]) {
				name := s.Name
				if s.Recv != "" {
					name = s.Recv + "." + s.Name
				}
				key := s.File + ":" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				lines = append(lines, fmt.Sprintf("- %s %s（%s:%d）", s.Kind, name, s.File, s.Line))
				if len(lines) >= maxTouchedSymbols {
					lines = append(lines, "…（其余略）")
					return renderTouchedSection(lines)
				}
			}
		}
	}
	return renderTouchedSection(lines)
}

func renderTouchedSection(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "以下 %d 个符号的代码行被本次变更触达（代码图索引，可作为审查入口）：\n", len(lines))
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("可用 get_symbol 查定义与签名、get_references 查调用方/使用点。")
	return plugin.DataFence("变更触达符号（代码图索引）", strings.TrimRight(b.String(), "\n"))
}

// hasIncludedGoFiles 变更中是否有进入审查文本的 Go 文件。
func hasIncludedGoFiles(diff *plugin.DiffContext) bool {
	if diff == nil {
		return false
	}
	for _, f := range diff.Files {
		if f.Included && strings.HasSuffix(f.Path, ".go") {
			return true
		}
	}
	return false
}

// includedPaths diffprep 收进 Text 的文件（OCR delegate rule 的路径入参与
// argus 实际审查范围保持一致）。
func includedPaths(diff *plugin.DiffContext) []string {
	if diff == nil {
		return nil
	}
	paths := make([]string, 0, len(diff.Files))
	for _, f := range diff.Files {
		if f.Included {
			paths = append(paths, f.Path)
		}
	}
	return paths
}

// worktreeKey 将 PRContext 转为 workcopy.WorktreeKey（与 adapter_cli.prToKey 同构；
// 4 行结构体不值得为它反向依赖插件适配器包）。
func worktreeKey(pr *plugin.PRContext) workcopy.WorktreeKey {
	return workcopy.WorktreeKey{
		Platform:      pr.Platform,
		Owner:         pr.Owner,
		Repo:          pr.Repo,
		Number:        pr.Number,
		HeadSHA:       pr.HeadSHA,
		DefaultBranch: pr.DefaultBranch,
	}
}
