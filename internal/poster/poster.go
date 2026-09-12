// Package poster 回帖服务（design §7-7 / §10.1 / §10.2）：
//   - staleness 门控：存在同 PR 更晚创建的非 failed 任务 ⇒ 本任务 stale（不回帖、不覆盖 review_targets）；
//   - 行内评论随 CreateReview 每轮独立，不追平；
//   - 总结评论 synchronize 时更新既有评论（review_targets.last_summary_comment_id 定位），
//     失败降级为扫描 HTML 注释头（§10.1 审计锚点），再降级新建；
//   - 回帖成功后更新 review_targets（last_summary_comment_id + last_head_sha）。
package poster

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/agentkit/textutil"
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
	// BotUsername bot 账号名（扫描兜底时校验报告头评论作者；空 = 不校验作者）
	BotUsername string
}

// reportHeaderRe 严格报告头：字段齐全且独占一行（head 允许为空——手动任务回填失败时
// 报告头是 head=）。宽匹配 Contains 会被含该前缀的第三方评论劫持更新目标。
var reportHeaderRe = regexp.MustCompile(`(?m)^<!-- argus:report task_id=\S+ head=\S* ver=\S+ -->[ \t]*$`)

// Result 回帖结果。
type Result struct {
	Stale            bool // 命中 staleness：未回帖
	SummaryCommentID int64
	InlinePosted     int
}

// Post 执行回帖。报告内容由 runner 组装成 reportview.Report（本包只管发到哪里、怎么降级）。
// diff 用于行内评论预校验（可为 nil = 跳过校验）。
func (p *Poster) Post(ctx context.Context, task *storage.Task, pr plugin.PRContext,
	diff *plugin.DiffContext, verdict string, rep reportview.Report) (*Result, error) {

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
	// 行内预校验：file 必须在 diff 文件列表内。LLM 幻觉文件/行号直发会被平台
	// 整单拒绝（422），连累评审事件与全部行内意见一起丢——被剔除的留在总结评论里。
	if diff != nil && len(inlineFindings) > 0 {
		inDiff := make(map[string]bool, len(diff.Files))
		for _, df := range diff.Files {
			inDiff[df.Path] = true
		}
		kept := inlineFindings[:0]
		for _, f := range inlineFindings {
			if inDiff[f.File] {
				kept = append(kept, f)
			} else {
				p.Log.Warn("argus.poster.inline_dropped_not_in_diff",
					"task", task.ID, "file", f.File, "line", f.Line)
			}
		}
		inlineFindings = kept
	}
	sort.SliceStable(inlineFindings, func(i, j int) bool {
		return severity.Rank(inlineFindings[i].Severity) > severity.Rank(inlineFindings[j].Severity)
	})
	comments := make([]provider.ReviewComment, 0, len(inlineFindings))
	for _, f := range inlineFindings {
		comments = append(comments, provider.ReviewComment{
			Path: f.File, Line: f.Line,
			Body: inlineCommentBody(f),
		})
	}
	event := "COMMENT"
	switch verdict {
	case "request_changes":
		event = "REQUEST_CHANGES"
	case "approve":
		event = "APPROVE"
	}
	reviewAnchor := "\n\n<!-- argus:review task_id=" + rep.TaskID + " head=" + rep.HeadSHA + " verdict=" + verdict + " -->"
	reviewBody := "Argus 审查结论：" + rep.VerdictText + "\n\n完整报告见总结评论（该评论随每次同步更新）。" + reviewAnchor
	// 评审幂等锚点：任务模型 at-least-once（崩溃/硬取消落在发评审与落终态之间 → 重跑），
	// 重跑会再发一条完整评审（行内评论重复）。锚点只写不查等于没有——发前按 task_id 查一次；
	// 查询失败不阻断正常路径（宁多查一次 API，不因兜底接口缺失丢评审）。
	// 性能注意：ListReviews 拉取该 PR 全部评审（break 早退），大 PR（50+ review）有延迟；
	// 若后续需要优化，可在 tasks 表加 review_posted bool 列跳过此 API 调用。
	alreadyPosted := false
	if posted, err := p.SDK.ListReviews(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); err == nil {
		for _, r := range posted {
			if strings.Contains(r.Body, "argus:review task_id="+rep.TaskID+" ") {
				alreadyPosted = true
				p.Log.Warn("argus.poster.review_already_posted",
					"task", rep.TaskID, "review_id", r.ID, "action", "跳过重复评审（总结评论照常更新）")
				break
			}
		}
	}
	if !alreadyPosted {
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
				// 行内意见可能整体非法（如全部行号越界）——去掉 comments 以纯评论事件兜底，
				// 保住评审记录与结论语义（此前整轮评审事件直接丢失）。
				p.Log.Warn("argus.poster.review_fallback_failed", "error", err2.Error(),
					"fallback", "无行内意见评审")
				if _, err3 := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
					provider.CreateReviewOptions{
						CommitID: pr.HeadSHA,
						Event:    "COMMENT",
						Body:     fallbackBody,
					}); err3 != nil {
					p.Log.Error("argus.poster.review_bare_failed", "error", err3.Error())
					plugin.ReportPluginFailure("poster:review")
				}
			}
		} else {
			inline = len(inlineFindings)
		}
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

	// ② 扫描既有评论的完整报告头（review_targets 记录损坏时的审计兜底，§10.1）。
	// 严格匹配 + 作者校验：宽匹配会被含该前缀的第三方评论劫持更新目标。
	// SDK 只返回第一页（最旧一页，≤50 条），报告评论如已被顶出第一页则扫描不中，
	// 走 ③ 新建——SDK 分页能力补齐前的已知边界。
	if comments, err := p.SDK.ListIssueComments(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); err == nil {
		var found int64
		for _, c := range comments {
			if !reportHeaderRe.MatchString(c.Body) {
				continue
			}
			if p.BotUsername != "" && (c.Author == nil || c.Author.Username != p.BotUsername) {
				continue
			}
			found = c.ID // 列表升序，取最后一个匹配（最新）
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


// inlineCommentBody 拼行内评论正文：严重级 + 意见 + 修复建议。
// SuggestedFix 非空时渲染为 ```suggestion 围栏（Gitea/GitHub 渲染出"一键应用"按钮），
// 建议内容是代码原文，不做 HTML 转义——否则 Apply 后代码被污染。围栏安全：
// 建议含 ``` 或超长时退化为纯文本意见（完整建议仍在总结评论中展示），不让围栏断裂。
func inlineCommentBody(f plugin.Finding) string {
	body := "**[" + strings.ToUpper(f.Severity) + "]** " + reportview.EscapeUntrusted(f.Comment)
	fix := strings.TrimSpace(f.SuggestedFix)
	if fix != "" && !strings.Contains(fix, "```") {
		if t, truncated := textutil.TruncRunes(fix, suggestionMaxRunes); !truncated {
			body += "\n\n```suggestion\n" + fix + "\n```"
		} else {
			body += "\n\n（修复建议过长，已省略；见总结评论）"
		}
	}
	return body + "\n\n<!-- argus:inline by=" + f.By + " -->"
}

// suggestionMaxRunes 单条 suggestion 围栏的建议长度上限（防超大评论刷屏）。
const suggestionMaxRunes = 1200
