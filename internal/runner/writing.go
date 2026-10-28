// writing.go 写作面任务执行（v3.71.0）：describe 生成并写回 PR 描述、
// changelog 生成变更条目并发评论。不走 R 系列审查管线与插件派发——
// 单步 LLM 写作后直接平台写回，复用 runner 的 marker/落库/播报原语收口。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/runtime/procx"

	"argus/internal/domain"
	"argus/internal/fixverifier"
	"argus/internal/plugin"
	"argus/internal/reportview"
	"argus/internal/storage"
	"argus/internal/workflow/requirement"
	"argus/internal/writing"
)

// runWriting 写作任务主链：prepareContext 之后由 run 分派进入。
// 产物不进 reportview 审查模板——report 正文即写作产物，经 FinishTaskFenced
// 落库（GET /tasks/:id 可查）。
func (r *Runner) runWriting(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	diff *plugin.DiffContext, start time.Time) error {

	svc := &writing.Service{LLM: r.LLM}
	in := writing.Input{
		Platform:     pr.Platform,
		Owner:        pr.Owner,
		Repo:         pr.Repo,
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         pr.Body,
		Author:       pr.Author,
		SourceBranch: pr.HeadBranch,
		TargetBranch: pr.BaseBranch,
		DiffText:     diff.Text,
	}
	if m := storage.DecodeJSONMap(task.ArtifactsJSON); m != nil {
		in.ChangeSummary = m[plugin.ArtifactChangeSummary]
	}

	prov, err := r.SDK.Provider(pr.Platform)
	if err != nil {
		return fmt.Errorf("获取平台 provider 失败: %w", err)
	}

	var out *requirement.Output
	var commentID int64
	switch task.Kind {
	case domain.TaskKindDescribe:
		out, err = r.writeDescribe(ctx, task, pr, svc, in, prov)
		if err != nil {
			return err
		}
	case domain.TaskKindChangelog:
		out, commentID, err = r.writeChangelog(ctx, task, pr, svc, in, prov)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知写作任务 kind: %s", task.Kind)
	}

	// 报告正文：任务头 + 写作产物（与审查报告同 header 格式，TaskAPI 可查）。
	rep := reportview.Report{
		TaskID: task.ID, HeadSHA: pr.HeadSHA, Version: r.Version,
		ShowCompliance: false,
	}
	body := reportview.Render(rep)
	body += "\n## ✍️ 写作产物（" + task.Kind + "）\n\n" + strings.TrimSpace(out.Summary)

	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "output": out.Summary})
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone,
		out.Verdict, body, string(resultJSON), "", out.Tokens, commentID); ferr != nil {
		return fmt.Errorf("写作结果落库失败: %w", ferr)
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次写作结果丢弃")
		return nil
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"✅ Argus 写作完成（%s）——产物见%s。\n\n<!-- argus:start task_id=%s -->",
		task.Kind, writingTargetLabel(task.Kind, commentID), task.ID))
	r.announceTerminal(ctx, task, storage.StatusDone, out.Verdict, out, "")
	plugin.ReportTaskTotal(pr.Platform, task.Kind, storage.StatusDone)
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// writeDescribe describe 写作 + 描述写回（管理段替换/追加，不覆盖作者内容）。
func (r *Runner) writeDescribe(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*requirement.Output, error) {

	d, err := svc.Describe(ctx, in)
	if err != nil {
		return nil, err
	}
	section := writing.RenderDescription(d, task.ID)
	newDesc := writing.MergeDescription(pr.Body, section)
	if _, err := prov.UpdateCR(ctx, pr.Owner, pr.Repo, pr.Number,
		provider.UpdateCROptions{Description: newDesc}); err != nil {
		return nil, fmt.Errorf("PR 描述写回失败: %w", err)
	}
	r.Log.Info("argus.runner.describe_written", "task", task.ID,
		"pr", fmt.Sprintf("%s/%s#%s", pr.Owner, pr.Repo, pr.Number), "title", d.Title)
	return &requirement.Output{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    "**" + d.Title + "**\n\n" + d.Body,
		Tokens:     svc.LLM.UsedTokens(),
	}, nil
}

// writeChangelog changelog 条目写作 + 评论发布。
func (r *Runner) writeChangelog(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*requirement.Output, int64, error) {

	entry, err := svc.Changelog(ctx, in)
	if err != nil {
		return nil, 0, err
	}
	body := fmt.Sprintf("## 📝 Changelog 条目\n\n%s\n\n<!-- argus:changelog task_id=%s -->",
		entry, task.ID)
	idStr, err := prov.CreateDiscussion(ctx, pr.Owner, pr.Repo, pr.Number,
		provider.DiscussionOptions{Body: body})
	var commentID int64
	if err != nil {
		r.Log.Warn("argus.runner.changelog_comment_failed", "task", task.ID, "error", err.Error())
	} else {
		commentID = parseCommentID(idStr)
	}
	return &requirement.Output{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    entry,
		Tokens:     svc.LLM.UsedTokens(),
	}, commentID, nil
}

// writingTargetLabel 收口播报里产物去向的措辞。
func writingTargetLabel(kind string, commentID int64) string {
	if commentID > 0 {
		return "评论 #" + fmt.Sprintf("%d", commentID)
	}
	if kind == domain.TaskKindDescribe {
		return "PR 描述"
	}
	return "任务报告"
}

// parseCommentID CreateDiscussion 返回的平台评论 ID（非数字=0，容忍降级）。
func parseCommentID(idStr string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// runCIAnalysis CI 失败归因任务入口（独立于 run 管线：push 的 commit 无 CR，
// 不走 prepareContext）。链路：拉失败 job 日志（provider 可选能力 CILogManager，
// 未实现的平台任务失败降级）→ LLM 归因 → 评论回 PR（有 MR 时）→ 落库收口。
func (r *Runner) runCIAnalysis(ctx context.Context, task *storage.Task, start time.Time) error {
	prov, err := r.SDK.Provider(task.Platform)
	if err != nil {
		return fmt.Errorf("获取平台 provider 失败: %w", err)
	}
	ci, ok := prov.(provider.CILogManager)
	if !ok {
		return fmt.Errorf("平台 %s 未实现 CI 日志能力（CILogManager），无法归因", task.Platform)
	}
	// 关联 MR 优先按任务 Number；无 MR 的 push pipeline 评论降级为跳过（产物只落库）。
	jobs, err := ci.ListFailedJobLogs(ctx, task.Owner, task.Repo, task.HeadSHA)
	if err != nil {
		return fmt.Errorf("拉取失败 job 日志失败: %w", err)
	}
	ciJobs := make([]writing.CIJob, 0, len(jobs))
	for _, j := range jobs {
		ciJobs = append(ciJobs, writing.CIJob{
			Name: j.Name, Stage: j.Stage, WebURL: j.WebURL, Trace: j.Trace, Truncat: j.Truncat,
		})
	}
	analysis, err := (&writing.Service{LLM: r.LLM}).AnalyzeCIFailure(ctx, writing.CIInput{
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
		Number: task.Number, Commit: task.HeadSHA, Jobs: ciJobs,
	})
	if err != nil {
		return err
	}
	var commentID int64
	if task.Number != "" {
		idStr, cerr := prov.CreateDiscussion(ctx, task.Owner, task.Repo, task.Number,
			provider.DiscussionOptions{Body: writing.RenderCIAnalysis(analysis, task.ID)})
		if cerr != nil {
			r.Log.Warn("argus.runner.ci_analysis_comment_failed", "task", task.ID, "error", cerr.Error())
		} else {
			commentID = parseCommentID(idStr)
		}
	}
	r.Log.Info("argus.runner.ci_analysis_done", "task", task.ID, "jobs", len(ciJobs))

	out := &requirement.Output{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    analysis.Conclusion,
		Tokens:     r.LLM.UsedTokens(),
	}
	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "analysis": analysis})
	body := "## ✍️ CI 失败归因\n\n**结论**：" + analysis.Conclusion + "\n\n**根因**：" + analysis.RootCause
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone,
		out.Verdict, body, string(resultJSON), "", out.Tokens, commentID); ferr != nil {
		return fmt.Errorf("归因结果落库失败: %w", ferr)
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次归因结果丢弃")
		return nil
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"✅ Argus CI 归因完成——结论见%s。\n\n<!-- argus:start task_id=%s -->",
		writingTargetLabel(task.Kind, commentID), task.ID))
	r.announceTerminal(ctx, task, storage.StatusDone, out.Verdict, out, "")
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusDone)
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// runDiffPreview 本地 diff 预审任务（v3.123.0）：run 在 prepareContext 之前
// 分派（同 ci_analysis，无 MR 语义）。输入 artifacts["diff"]，经 writing.
// ReviewDiff 单轮执行体（与 MCP review_diff 同步路径 prompt 单源），报告落库
// 供提交方 get_report 轮询，不回帖不进 reportview 审查模板。
func (r *Runner) runDiffPreview(ctx context.Context, task *storage.Task, start time.Time) error {
	diff := storage.DecodeJSONMap(task.ArtifactsJSON)[plugin.ArtifactDiffInput]
	report, err := (&writing.Service{LLM: r.LLM}).ReviewDiff(ctx, diff)
	if err != nil {
		return err
	}
	out := &requirement.Output{
		Verdict:    "comment", // 预审无合并门语义，只出意见报告
		PluginsRun: []string{"writing"},
		Summary:    report,
		Tokens:     r.LLM.UsedTokens(),
	}
	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "report": report})
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone,
		out.Verdict, report, string(resultJSON), "", out.Tokens, 0); ferr != nil {
		return fmt.Errorf("预审结果落库失败: %w", ferr)
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次预审结果丢弃")
		return nil
	}
	r.announceTerminal(ctx, task, storage.StatusDone, out.Verdict, out, "")
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusDone)
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// verifyFixes 修复验证环（v3.74.0）：fixverifier 对带 suggested_fix 的意见做
// 沙箱应用+编译验证并回写 f.Verified。未配置包装命令 / 无工作副本 / 全失败
// 均静默跳过——功能是加分项，永不阻断审查主链路。
func (r *Runner) verifyFixes(ctx context.Context, task *storage.Task, pr *plugin.PRContext, out *requirement.Output) {
	if r.Cfg == nil || r.Cfg.FixVerifyCmd == "" || out.Merged == nil || len(out.Merged.Findings) == 0 || r.WC == nil {
		return
	}
	key := plugin.PRToKey(*pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		r.Log.Debug("argus.runner.fix_verify_skip", "task", task.ID, "reason", err.Error())
		return
	}
	defer r.WC.Release(key)
	results := fixverifier.Verify(ctx, fixverifier.Config{
		Cmd:     r.Cfg.FixVerifyCmd,
		Timeout: r.Cfg.FixVerifyTimeout,
	}, dir, out.Merged.Findings)
	verified := 0
	for i := range results {
		if results[i].OK {
			out.Merged.Findings[i].Verified = true
			verified++
		}
	}
	if verified > 0 {
		r.Log.Info("argus.runner.fix_verified", "task", task.ID,
			"verified", verified, "total", len(results))
		// push 环（v3.74.1，opt-in）：验证通过的修复自动 commit+push 到 PR 源
		// 分支。push 失败（作者已推新提交/无权限）静默降级为不 push——下轮
		// 审查在新的 head 上重新验证再战，绝不阻塞主链路。
		if r.Cfg.FixPush {
			r.pushVerifiedFixes(ctx, task, pr, dir, out.Merged.Findings, verified)
		}
	}
}

// pushVerifiedFixes 把验证通过的修复 commit 并 push 到 PR 源分支。
// 多条修复合并为一个 commit（同一工作副本同一轮，拆分只会放大冲突面）；
// commit message 带任务 ID 与条数可追溯。
func (r *Runner) pushVerifiedFixes(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, dir string, findings []plugin.Finding, verified int) {

	// 先清场再应用（v3.75.3）：工作副本可能有前轮残留（Apply 中途失败/push 失败
	// 未回滚）与 Verify 期包装命令产物（go build 二进制/coverage）——git add -A
	// 会把这些一并推到作者分支。reset --hard + clean -fd 把树恢复到 checkout 态，
	// Apply 的修改随后写入；push 失败路径同样收尾清场。
	for _, cleanup := range [][]string{
		{"-C", dir, "reset", "--hard"},
		{"-C", dir, "clean", "-fd"},
	} {
		if code, stderr, ok := r.gitRun(ctx, dir, cleanup); !ok {
			r.Log.Warn("argus.runner.fix_clean_failed", "task", task.ID,
				"step", cleanup[2], "code", code, "stderr", logredact.Redact(stderr))
			return
		}
	}
	written, err := fixverifier.Apply(ctx, dir, findings)
	if err != nil {
		r.Log.Warn("argus.runner.fix_apply_failed", "task", task.ID, "error", err.Error())
		return
	}
	if pr.HeadBranch == "" || len(written) == 0 {
		return
	}
	// 只 add Apply 实际写入的文件——包装命令产生的未跟踪产物绝不进 commit。
	addArgs := append([]string{"-C", dir, "add", "--"}, slices.Sorted(maps.Keys(written))...)
	msg := fmt.Sprintf("fix(argus): 应用 %d 条沙箱验证通过的审查修复\n\nTask: %s\nPR: %s/%s#%s\n\n"+
		"由 argus 修复验证环自动生成（ARGUS_FIX_PUSH=1）；每条修改均已在隔离沙箱通过编译验证。如需回退请 revert 本提交。",
		verified, task.ID, pr.Owner, pr.Repo, pr.Number)
	gitArgs := [][]string{
		addArgs,
		{"-C", dir, "-c", "user.name=argus-bot", "-c", "user.email=argus@bot.local",
			"commit", "-m", msg},
		{"-C", dir, "push", "origin", "HEAD:" + pr.HeadBranch},
	}
	for _, argv := range gitArgs {
		code, stderr, ok := r.gitRun(ctx, dir, argv)
		if !ok {
			r.Log.Warn("argus.runner.fix_push_failed", "task", task.ID, "step", argv[2],
				"code", code, "stderr", logredact.Redact(stderr))
			// push/commit 失败：清场收尾，不给同 SHA 后续任务留污染树
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "reset", "--hard"})
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "clean", "-fd"})
			return
		}
	}
	r.Log.Info("argus.runner.fix_pushed", "task", task.ID, "branch", pr.HeadBranch,
		"fixes", verified, "files", len(written))
	// 审计留痕（v3.86.0）：bot 写作者分支是最高敏感动作，必须可追溯
	r.Audit.Log("fix.push", "task", task.ID, "platform", pr.Platform, "owner", pr.Owner,
		"repo", pr.Repo, "number", pr.Number, "branch", pr.HeadBranch,
		"fixes", verified, "files", len(written))
}

// gitRun 单条 git 命令执行（2min 超时；ok=false = 非零退出或启动失败）。
func (r *Runner) gitRun(ctx context.Context, dir string, argv []string) (int, string, bool) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, stderr, code, err := procx.Run(runCtx, procx.RunRequest{
		Argv: append([]string{"git"}, argv...), Timeout: 2 * time.Minute,
	})
	return code, stderr, err == nil && code == 0
}
