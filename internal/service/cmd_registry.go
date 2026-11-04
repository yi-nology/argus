// cmd_registry.go —— @bot 评论指令目录与信息类指令（v5.3.0 商业化打磨）。
//
// 指令目录单源：help 渲染、门控判定（isDriveCommand）、冷却提示（matchAnyCommand）
// 共用一张表——新增指令只改 cmdCatalog + 对应 extract*/dispatch*，help 不会漏项。
//
// 商业化三件：
//   - help/帮助：任何时刻可查指令手册（免节流，查询类永远畅通）；
//   - status/进度：最近一轮审查状态与结论（免节流）；
//   - 烧预算指令门控（ARGUS_COMMENT_GATE）：review/recheck/fix/testgen 仅协作者
//     可触发，防公开仓路人评论白烧审查预算；问答/查询/误报反馈类不受门控。
package service

import (
	"argus/internal/commentcmd"
	"context"
	"fmt"
	"strings"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"
)

// dispatchInfo 信息类指令（help/status）：免节流、免门控、零 LLM。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchInfo(ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	switch {
	case commentcmd.ExtractHelp(question):
		body := commentcmd.HelpText(s.Cfg != nil && s.Cfg.FixAgent != "") + "\n\n<!-- argus:help delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "help", len(body))
		return true
	case commentcmd.ExtractStatus(question):
		body := s.statusText(ctx, platform, owner, repo, prNum) + "\n\n<!-- argus:status delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "status", len(body))
		return true
	}
	return false
}

// statusText 审查状态（最近一轮任务 + 意见数 + 历史轮数）。
func (s *Service) statusText(ctx context.Context, platform, owner, repo, prNum string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 📊 PR #%s 审查状态\n", prNum)
	tasks, err := s.Store.ListTasksByPR(ctx, platform, owner, repo, prNum, 5)
	if err != nil || len(tasks) == 0 {
		b.WriteString("暂无审查任务。`@bot review` 可发起一轮全量评审。")
		return b.String()
	}
	t := tasks[0]
	state := t.Status
	if t.Status == "done" && t.Verdict != "" {
		state = "done / " + t.Verdict
	}
	fmt.Fprintf(&b, "- 最近任务 `%s`：%s（%s，head `%s`）\n", shortFP(t.ID), state, t.Kind, shortFP(t.HeadSHA))
	if t.Status == "done" {
		if fs, ok := taskFindings(t.ResultJSON); ok {
			fmt.Fprintf(&b, "- 结论：%s，%d 条意见\n", orDash(t.Verdict), len(fs))
		}
	} else if t.Status == "running" || t.Status == "pending" {
		b.WriteString("- 审查进行中，完成后会回帖报告\n")
	} else if t.Status == "failed" {
		fmt.Fprintf(&b, "- 失败原因：%s\n", orDash(t.FailReason))
	}
	if len(tasks) > 1 {
		fmt.Fprintf(&b, "- 历史：共 %d 轮\n", len(tasks))
	}
	b.WriteString("\n下一步：`@bot recheck fp:短码` 复核单条 · `@bot review` 重新审查 · `@bot help` 指令手册")
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// collabCache 协作者名单缓存（per-Service 懒建，TTL 5 分钟）。
func (s *Service) collabCache() *datacache.LocalCache[string] {
	s.qaThrottleMu.Lock()
	defer s.qaThrottleMu.Unlock()
	if s.collabCch == nil {
		s.collabCch = datacache.NewLocalCache(datacache.LocalConfig[string]{
			TTL:     5 * time.Minute,
			MaxSize: 200,
		})
	}
	return s.collabCch
}

// ackComment 即时回执（v5.3.0）：给触发指令的评论加 👀，告诉用户"已看到"。
// best-effort：能力缺失/失败只留痕，绝不影响主链路。
func (s *Service) ackComment(ctx context.Context, platform, owner, repo string, commentID int64) {
	if commentID <= 0 {
		return
	}
	if err := s.SDK.AddIssueCommentReaction(ctx, platform, owner, repo, commentID, provider.ReactionEyes); err != nil {
		s.log().Warn("argus.comment.ack_failed", "platform", platform,
			"repo", owner+"/"+repo, "comment_id", commentID, "error", err.Error())
	}
}

// actorCanDrive 烧预算指令触发者门控（v5.3.0）。
// ARGUS_COMMENT_GATE=open 直接放行；collaborator 模式查协作者名单（每仓 5 分钟
// 缓存，名单以 "\nu1\nu2\n" 形态缓存、本地子串判定）。平台不支持
// CollaboratorManager 或查询失败时 fail-open 并留痕——能力差异/网络抖动不能把
// 评论链路整个堵死。
func (s *Service) actorCanDrive(ctx context.Context, platform, owner, repo, actor string) bool {
	if s.Cfg == nil || strings.EqualFold(s.Cfg.CommentGate, "open") {
		return true
	}
	if actor == "" {
		return false
	}
	key := platform + "/" + owner + "/" + repo
	joined, _ := s.collabCache().Get(ctx, key)
	if joined == nil {
		list, err := s.SDK.ListCollaborators(ctx, platform, owner, repo)
		if err != nil {
			s.log().Warn("argus.comment.gate_fail_open", "repo", key, "actor", actor, "error", err.Error())
			return true
		}
		names := make([]string, 0, len(list))
		for _, c := range list {
			if c == nil || c.Username == "" {
				continue
			}
			names = append(names, strings.ToLower(c.Username))
		}
		j := "\n" + strings.Join(names, "\n") + "\n"
		_ = s.collabCache().Set(ctx, key, &j)
		joined = &j
	}
	return strings.Contains(*joined, "\n"+strings.ToLower(actor)+"\n")
}
