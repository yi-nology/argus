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
