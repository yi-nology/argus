// Package poster 回帖服务（design §7-7 / §10.1 / §10.2）：
//   - staleness 门控：存在同 PR 更晚创建的非 failed 任务 ⇒ 本任务 stale（不回帖、不覆盖 review_targets）；
//   - 行内评论随 CreateReview 每轮独立，不追平；
//   - 总结评论 synchronize 时更新既有评论（review_targets.last_summary_comment_id 定位），
//     失败降级为扫描 HTML 注释头（§10.1 审计锚点），再降级新建；
//   - 回帖成功后更新 review_targets（last_summary_comment_id + last_head_sha）。
package poster

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/reportutil"
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
	// v3.51.0 路径归一化兜底：模型常以子项目根为参照报路径、缺 git 仓根前缀
	//（实测 demo-insights/{agent,server} 两仓 100% 复现，行内评论因此从未命中）。
	// 精确 miss 时在 Included 文件中做唯一尾段匹配（/ 段边界），唯一命中才修正——
	// 0/多命中维持剔除（错发位置比不发更糟）。剔除数进 rep.Degraded 用户可见。
	dropped := 0
	if diff != nil && len(inlineFindings) > 0 {
		inDiff := make(map[string]bool, len(diff.Files))
		var included []string
		for _, df := range diff.Files {
			inDiff[df.Path] = true
			if df.Included {
				included = append(included, df.Path)
			}
		}
		kept := inlineFindings[:0]
		for _, f := range inlineFindings {
			if inDiff[f.File] {
				kept = append(kept, f)
			} else if fixed, ok := uniqueSuffixMatch(included, f.File); ok {
				p.Log.Warn("argus.poster.inline_path_normalized",
					"task", task.ID, "from", f.File, "to", fixed, "line", f.Line)
				f.File = fixed
				kept = append(kept, f)
			} else {
				dropped++
				p.Log.Warn("argus.poster.inline_dropped_not_in_diff",
					"task", task.ID, "file", f.File, "line", f.Line)
			}
		}
		inlineFindings = kept
		if dropped > 0 {
			rep.Degraded = append(rep.Degraded, fmt.Sprintf(
				"%d 条意见的文件路径无法对应 diff 中的文件（模型路径引用偏差），仅在总结中展示、未行内标注", dropped))
		}
	}
	sort.SliceStable(inlineFindings, func(i, j int) bool {
		return reportutil.Rank(inlineFindings[i].Severity) > reportutil.Rank(inlineFindings[j].Severity)
	})
	inlineFindings = clampInlineByMaxComments(inlineFindings, &rep)
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
	// v3.36.0：review_posted 库标记短路——重跑任务大多不再走 ListReviews 平台查询。
	// v3.36.0：review_posted 库标记短路——此前重跑任务必 ListReviews 拉全量
	// 评审（大 PR 有延迟）。查库失败/未标记时回退平台锚点查询（宁多查一次）。
	alreadyPosted := false
	if t, gerr := p.Store.GetTask(ctx, rep.TaskID); gerr == nil && t != nil && t.ReviewPosted {
		alreadyPosted = true
		p.Log.Info("argus.poster.review_skip_by_flag", "task", rep.TaskID,
			"action", "跳过重复评审（review_posted；总结评论照常更新）")
	}
	if posted, err := p.SDK.ListReviews(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); err == nil && !alreadyPosted {
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
		reviewSent := false
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
				} else {
					reviewSent = true
				}
			} else {
				reviewSent = true
			}
		} else {
			inline = len(inlineFindings)
			reviewSent = true
		}
		if reviewSent {
			// 发成功落库标记（v3.36.0）：重跑短路平台锚点查询
			if merr := p.Store.MarkReviewPosted(ctx, rep.TaskID); merr != nil {
				p.Log.Warn("argus.poster.mark_posted_failed", "error", merr.Error(),
					"hint", "未落库标记：重跑回退 ListReviews 锚点查询（幂等不丢）")
			}
		}
	}

	// ---- 总结评论：更新 → 扫描兜底 → 历史缓存兜底 → 新建（四级降级）----
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
	// 历史评论 ID 缓存追加（收尾批修复）：供"评论被顶出 SDK 分页第一页"后的
	// 直连更新兜底，防每轮新建报告评论
	if err := p.Store.AppendReviewTargetComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, commentID); err != nil {
		p.Log.Warn("argus.poster.known_comment_append_failed", "error", err.Error())
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
		// 评论被删（404）是持久故障：不清缓存则每轮都先撞一次失败更新再降级。
		// 清除缓存直走 ②/③，③ 新建成功后回写自愈（深挖修复）。
		if provider.IsNotFound(err) {
			p.Log.Warn("argus.poster.summary_comment_gone", "comment_id", target.LastSummaryCommentID,
				"action", "清除缓存定位，走扫描/新建")
			if e2 := p.Store.ClearReviewTargetComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); e2 != nil {
				p.Log.Warn("argus.poster.target_clear_failed", "error", e2.Error())
			}
		} else {
			p.Log.Warn("argus.poster.summary_update_failed", "comment_id", target.LastSummaryCommentID,
				"error", err.Error(), "fallback", "scan")
		}
	}

	// ② 扫描既有评论的完整报告头（review_targets 记录损坏时的审计兜底，§10.1）。
	// 严格匹配 + 作者校验：宽匹配会被含该前缀的第三方评论劫持更新目标。
	// SDK 只返回第一页（最旧一页，≤50 条），报告评论如已被顶出第一页则扫描不中，
	// 走 ③ 新建——SDK 分页能力补齐前的已知边界。
	// 扫描步限时：SDK 分页缺陷曾在此无限循环卡死 worker（Forgejo 忽略 page 参数），
	// 超时/失败一律降级 ③ 新建并 WARN 留痕——宁可重复评论也不能卡死队列。
	scanCtx, scanCancel := context.WithTimeout(ctx, summaryScanTimeout)
	comments, scanErr := p.SDK.ListIssueComments(scanCtx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	scanCancel()
	if scanErr != nil {
		p.Log.Warn("argus.poster.summary_scan_failed", "error", scanErr.Error(), "fallback", "create")
	} else {
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

	// ③ 历史缓存兜底（收尾批修复）：报告评论被顶出 SDK 分页第一页后 ② 恒不中，
	// 每轮新建→顶得更远→恶性循环。缓存 ID 全部是本 bot 发出的报告评论（Append
	// 于回帖成功后记录，封顶 10 条），逐个尝试直连更新，首个成功即返回。
	if cached, cerr := p.Store.KnownReviewTargetComments(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); cerr == nil {
		var lastTried int64
		if t, gerr := p.Store.GetReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); gerr == nil && t != nil {
			lastTried = t.LastSummaryCommentID
		}
		for _, id := range cached {
			if id == lastTried { // ① 已试过
				continue
			}
			if _, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, id, body); err == nil {
				p.Log.Info("argus.poster.summary_cached_updated", "comment_id", id, "source", "known_comment_ids")
				return id, nil
			}
		}
	} else {
		p.Log.Warn("argus.poster.known_comment_list_failed", "error", cerr.Error())
	}

	// ④ 新建
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
	if ann := plugin.SamplesAnnotation(f); ann != "" {
		// best-of-N 复现计数（结构化字段渲染，非评语后缀——指纹身份不受影响）
		body += ann
	}
	fix := strings.TrimSpace(f.SuggestedFix)
	if fix != "" && !strings.Contains(fix, "```") {
		if _, truncated := textutil.TruncRunes(fix, suggestionMaxRunes); !truncated {
			body += "\n\n```suggestion\n" + fix + "\n```"
		} else {
			body += "\n\n（修复建议过长，已省略；见总结评论）"
		}
	}
	return body + "\n\n<!-- argus:inline by=" + f.By + " -->"
}

// suggestionMaxRunes 单条 suggestion 围栏的建议长度上限（防超大评论刷屏）。
const suggestionMaxRunes = 1200

// summaryScanTimeout 总结评论定位（扫描既有评论）步的时间上限：
// 该步若卡死会阻塞整个 worker，宁可降级新建评论也不能挂起队列。
const summaryScanTimeout = 60 * time.Second

// clampInlineByMaxComments 仓库级行内评论上限（v3.15.0 review.max_comments；
// 0 = 不限）：调用方已按严重度降序排好，这里保留 top N，其余仅在总结评论列出
// （Findings 段完整，透明可查），超限事实以降级注明写入报告。
func clampInlineByMaxComments(inlineFindings []plugin.Finding, rep *reportview.Report) []plugin.Finding {
	if rep.MaxInlineComments <= 0 || len(inlineFindings) <= rep.MaxInlineComments {
		return inlineFindings
	}
	dropped := len(inlineFindings) - rep.MaxInlineComments
	rep.Degraded = append(rep.Degraded, fmt.Sprintf(
		"仓库配置 review.max_comments=%d：%d 条行内意见超出上限，仅在总结评论列出",
		rep.MaxInlineComments, dropped))
	return inlineFindings[:rep.MaxInlineComments]
}

// uniqueSuffixMatch 在 paths 中找 file 的唯一尾段匹配（/ 段边界，防 pcap.go 误配
// pcapDownload.go）。模型路径幻觉的常见形态是缺 git 仓根前缀（子项目根参照）——
// 唯一命中返回修正路径；0/多命中返回 false（多义时错发位置比不发更糟）。
func uniqueSuffixMatch(paths []string, file string) (string, bool) {
	if file == "" {
		return "", false
	}
	hit := ""
	for _, p := range paths {
		if p == file || strings.HasSuffix(p, "/"+file) {
			if hit != "" {
				return "", false
			}
			hit = p
		}
	}
	return hit, hit != ""
}
