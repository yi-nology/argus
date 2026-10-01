// writing.go 写作面任务执行（v3.71.0）：describe 生成并写回 PR 描述、
// changelog 生成变更条目并发评论。不走 R 系列审查管线与插件派发——
// 单步 LLM 写作后直接平台写回，复用 runner 的 marker/落库/播报原语收口。
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/procx"

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
	if m := decodeArtifacts(task.ArtifactsJSON); m != nil {
		in.ChangeSummary = m[plugin.ArtifactChangeSummary]
	}

	prov, err := r.SDK.Provider(pr.Platform)
	if err != nil {
		return fmt.Errorf("获取平台 provider 失败: %w", err)
	}

	var out *requirement.Output
	var commentID int64
	switch task.Kind {
	case plugin.TaskKindDescribe:
		out, err = r.writeDescribe(ctx, task, pr, svc, in, prov)
		if err != nil {
			return err
		}
	case plugin.TaskKindChangelog:
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
	if kind == plugin.TaskKindDescribe {
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

	if _, err := fixverifier.Apply(ctx, dir, findings); err != nil {
		r.Log.Warn("argus.runner.fix_apply_failed", "task", task.ID, "error", err.Error())
		return
	}
	if pr.HeadBranch == "" {
		return
	}
	msg := fmt.Sprintf("fix(argus): 应用 %d 条沙箱验证通过的审查修复\n\nTask: %s\nPR: %s/%s#%s\n\n"+
		"由 argus 修复验证环自动生成（ARGUS_FIX_PUSH=1）；每条修改均已在隔离沙箱通过编译验证。如需回退请 revert 本提交。",
		verified, task.ID, pr.Owner, pr.Repo, pr.Number)
	gitArgs := [][]string{
		{"-C", dir, "add", "-A"},
		{"-C", dir, "-c", "user.name=argus-bot", "-c", "user.email=argus@bot.local",
			"commit", "-m", msg},
		{"-C", dir, "push", "origin", "HEAD:" + pr.HeadBranch},
	}
	for _, argv := range gitArgs {
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, stderr, code, err := procx.Run(runCtx, procx.RunRequest{Argv: append([]string{"git"}, argv...), Timeout: 2 * time.Minute})
		cancel()
		if err != nil || code != 0 {
			r.Log.Warn("argus.runner.fix_push_failed", "task", task.ID, "step", argv[0],
				"code", code, "stderr", logredact.Redact(stderr))
			return
		}
	}
	r.Log.Info("argus.runner.fix_pushed", "task", task.ID, "branch", pr.HeadBranch, "fixes", verified)
}
