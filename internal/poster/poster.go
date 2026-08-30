// Package poster 回帖服务（design §7-7 / §10.1 / §10.2）：
//   - staleness 门控：存在同 PR 更晚创建的非 failed 任务 ⇒ 本任务 stale（不回帖、不覆盖 review_targets）；
//   - 行内评论随 CreateReview 每轮独立，不追平；
//   - 总结评论 synchronize 时更新既有评论（review_targets.last_summary_comment_id 定位），
//     失败降级为扫描 HTML 注释头（§10.1 审计锚点），再降级新建；
//   - 回帖成功后更新 review_targets（last_summary_comment_id + last_head_sha）。
package poster

import (
	"context"
	"sort"
	"strings"

	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/yi-nology/git-platform-sdk/provider"

	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/reportview"
	"argus/internal/storage"
)

// Poster 回帖执行器。
type Poster struct {
	SDK     *argusctx.SDKCtx
	Store   *storage.Store
	Version string
	Log     logx.Logger
}

// Result 回帖结果。
type Result struct {
	Stale            bool // 命中 staleness：未回帖
	SummaryCommentID int64
	InlinePosted     int
}

// Post 执行回帖。报告内容由 runner 组装成 reportview.Report（本包只管发到哪里、怎么降级）。
func (p *Poster) Post(ctx context.Context, task *storage.Task, pr plugin.PRContext,
	verdict string, rep reportview.Report) (*Result, error) {

	// ---- staleness 门控（§10.2 单条件判定）----
	stale, err := p.Store.HasNewerNonFailedTask(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		task.ID, task.CreatedAt)
	if err != nil {
		return nil, err
	}
	if stale {
		p.Log.Warn("argus.poster.stale_skip",
			"task", task.ID, "pr", pr.Owner+"/"+pr.Repo+"#"+pr.Number,
			"head", pr.HeadSHA, "reason", "存在同 PR 更晚创建的非 failed 任务")
		return &Result{Stale: true}, nil
	}

	// ---- 评审记录（v3.0.1 试点改造：每轮必留，PR 页可见审查状态）----
	// verdict 映射评审事件：request_changes→REQUEST_CHANGES / comment→COMMENT / approve→APPROVE。
	// 有行内 findings 时一并附上；无行内意见也提交评审事件（否则 PR 页看不到任何审查动作，
	// 评审状态恒为"暂无评审员"——真实试点暴露的可用性缺陷）。
	inline := 0
	var inlineFindings []plugin.Finding
	for _, f := range rep.Findings {
		if f.Line > 0 && f.File != "" {
			inlineFindings = append(inlineFindings, f)
		}
	}
	sort.SliceStable(inlineFindings, func(i, j int) bool {
		return plugin.SeverityRank(inlineFindings[i].Severity) > plugin.SeverityRank(inlineFindings[j].Severity)
	})
	comments := make([]provider.ReviewComment, 0, len(inlineFindings))
	for _, f := range inlineFindings {
		comments = append(comments, provider.ReviewComment{
			Path: f.File, Line: f.Line,
			Body: "**[" + strings.ToUpper(f.Severity) + "]** " + f.Comment + "\n\n<!-- argus:inline by=" + f.By + " -->",
		})
	}
	event := "COMMENT"
	switch verdict {
	case "request_changes":
		event = "REQUEST_CHANGES"
	case "approve":
		event = "APPROVE"
	}
	reviewBody := "Argus 审查结论：" + rep.VerdictText + "\n\n完整报告见总结评论（该评论随每次同步更新）。" +
		"\n\n<!-- argus:review task_id=" + rep.TaskID + " head=" + rep.HeadSHA + " verdict=" + verdict + " -->"
	if _, err := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		provider.CreateReviewOptions{
			CommitID: pr.HeadSHA,
			Event:    event,
			Body:     reviewBody,
			Comments: comments,
		}); err != nil {
		// 平台策略降级（真实试点发现）：PR 作者本人不能给自己提 blocking 评审
		//（Forgejo/Gitea "reject your own pull is not allowed"）。生产部署应使用
		// 独立 bot 账号；共用账号时自动降级为 COMMENT——审查记录仍然落地，
		// 结论语义保留在评审 body 与总结评论中。
		p.Log.Warn("argus.poster.review_failed", "error", err.Error(), "event", event,
			"fallback", "COMMENT 评审")
		fallbackBody := "⚠️ 平台限制：当前账号与 PR 作者相同，无法提交 " + event +
			" 评审，已降级为评论。原判定：" + rep.VerdictText + "\n\n" + reviewBody
		if _, err2 := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
			provider.CreateReviewOptions{
				CommitID: pr.HeadSHA,
				Event:    "COMMENT",
				Body:     fallbackBody,
				Comments: comments,
			}); err2 != nil {
			p.Log.Error("argus.poster.review_fallback_failed", "error", err2.Error())
			plugin.ReportPluginFailure("poster:review")
		}
	} else {
		inline = len(inlineFindings)
	}

	// ---- 总结评论：更新 → 扫描兜底 → 新建（三级降级，§7-7 / §10.2）----
	body := reportview.Render(rep)
	commentID, err := p.postSummary(ctx, pr, body)
	if err != nil {
		plugin.ReportPluginFailure("poster")
		return nil, err
	}

	// ---- 回帖成功：更新 review_targets（staleness 不读它；同 SHA 去重读 last_head_sha）----
	if err := p.Store.UpsertReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		commentID, pr.HeadSHA); err != nil {
		p.Log.Error("argus.poster.target_update_failed", "error", err.Error())
	}

	return &Result{SummaryCommentID: commentID, InlinePosted: inline}, nil
}

// postSummary 三级降级定位总结评论。
func (p *Poster) postSummary(ctx context.Context, pr plugin.PRContext, body string) (int64, error) {
	// ① review_targets.last_summary_comment_id
	if target, _ := p.Store.GetReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); target != nil &&
		target.LastSummaryCommentID > 0 {
		_, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
			target.LastSummaryCommentID, body)
		if err == nil {
			return target.LastSummaryCommentID, nil
		}
		p.Log.Warn("argus.poster.summary_update_failed", "comment_id", target.LastSummaryCommentID,
			"error", err.Error(), "fallback", "scan")
	}

	// ② 扫描既有评论的 HTML 注释头（review_targets 记录损坏时的审计兜底，§10.1）
	// 只看最近 50 条：高频 PR 评论量大，全量扫描既慢又无必要（报告评论永远是较新的）。
	if comments, err := p.SDK.ListIssueComments(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); err == nil {
		var found int64
		start := 0
		if len(comments) > 50 {
			start = len(comments) - 50
		}
		for _, c := range comments[start:] { // 取最后一个（最新）匹配
			if strings.Contains(c.Body, reportview.ReportHeaderPrefix) {
				found = c.ID
			}
		}
		if found > 0 {
			if _, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, found, body); err == nil {
				return found, nil
			}
			p.Log.Warn("argus.poster.summary_scan_update_failed", "comment_id", found, "fallback", "create")
		}
	}

	// ③ 新建
	c, err := p.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body)
	if err != nil {
		return 0, err
	}
	return c.ID, nil
}
