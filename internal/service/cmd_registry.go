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
	"context"
	"fmt"
	"strings"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"
)

// cmdSpec 指令目录项。
type cmdSpec struct {
	word    string   // 主命令词（extract* 的 cutCommand 目标）
	aliases []string // 同义词（中文/英文变体，help 展示与 matchAnyCommand 共用）
	usage   string   // 用法（不含 @bot 前缀）
	example string   // 示例
	desc    string   // 一句话说明
	drive   bool     // 烧预算/改状态：触发者需写权限（ARGUS_COMMENT_GATE）
	info    bool     // 查询类：免节流永远可达
}

// cmdCatalog 指令目录（help 单源）。order 即 help 展示序。
var cmdCatalog = []cmdSpec{
	{word: "review", aliases: []string{"评审", "复核", "重审", "重新审查", "重新评审", "再审", "重跑评审", "rerun", "re-review"},
		usage: "review [补充说明]", example: "请 @argus review 重点看登录模块",
		desc: "触发一轮全量重审并回执任务号", drive: true},
	{word: "recheck", aliases: []string{"复核"},
		usage: "recheck fp:短码 [补充]", example: "@argus recheck fp:ab12cd34 是不是误报",
		desc: "复核单条意见：对照 diff 裁决 成立/误报/存疑，附证据与处置建议", drive: true},
	{word: "ask", aliases: []string{},
		usage: "ask <问题>", example: "@argus ask 这个改动会影响性能吗",
		desc: "基于 PR 上下文问答（ask 可省略，自由文本也会走问答/意图路由）"},
	{word: "status", aliases: []string{"状态", "进度"},
		usage: "status", example: "@argus status",
		desc: "查看最近一轮审查的状态、结论与意见数", info: true},
	{word: "help", aliases: []string{"帮助", "命令", "指令"},
		usage: "help", example: "@argus help",
		desc: "显示本指令手册", info: true},
	{word: "describe", aliases: []string{},
		usage: "describe [补充要求]", example: "@argus describe",
		desc: "生成 PR 变更说明 + 建议标签"},
	{word: "changelog", aliases: []string{},
		usage: "changelog [补充要求]", example: "@argus changelog",
		desc: "生成 CHANGELOG 条目草稿"},
	{word: "testgen", aliases: []string{},
		usage: "testgen [目标路径]", example: "@argus testgen internal/login.go",
		desc: "按 diff 生成单元测试骨架（未经编译验证）", drive: true},
	{word: "fix", aliases: []string{},
		usage: "fix fp:短码", example: "@argus fix fp:ab12cd34",
		desc: "沙箱自动修复该意见并回帖 diff（需 ARGUS_FIX_AGENT 启用）", drive: true},
	{word: "suppress", aliases: []string{},
		usage: "suppress fp:短码", example: "@argus suppress fp:ab12cd34",
		desc: "误报反馈：后续轮次自动抑制 + 知识回流"},
	{word: "restore", aliases: []string{},
		usage: "restore fp:短码", example: "@argus restore fp:ab12cd34",
		desc: "误抑制恢复：意见重新可见"},
}

// cmdWords 目录全部触发词（主词 + 同义词），matchAnyCommand 共用。
func cmdWords() []string {
	var out []string
	for _, c := range cmdCatalog {
		out = append(out, c.word)
		out = append(out, c.aliases...)
	}
	return out
}

// matchAnyCommand question 是否以任一指令词开头（冷却提示用：指令被冷却要
// 有感知，自由文本被冷却保持静默防刷屏）。
func matchAnyCommand(question string) bool {
	for _, w := range cmdWords() {
		if _, ok := cutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// isDriveCommand question 是否为烧预算指令形态（review/recheck/fix/testgen）。
// 权限门控在 HandleComment 统一执行，门控点只此一处。
func isDriveCommand(question string) bool {
	if _, _, k := parseRecheck(question); k != recheckNone {
		return true
	}
	if _, ok := extractReviewCmd(question); ok {
		return true
	}
	if _, ok := extractFix(question); ok {
		return true
	}
	if _, ok := extractTestGen(question); ok {
		return true
	}
	return false
}

// extractHelp 解析 help/帮助/命令/指令。
func extractHelp(question string) bool {
	for _, w := range []string{"help", "帮助", "命令", "指令"} {
		if _, ok := cutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// extractStatus 解析 status/状态/进度。
func extractStatus(question string) bool {
	for _, w := range []string{"status", "状态", "进度"} {
		if _, ok := cutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// dispatchInfo 信息类指令（help/status）：免节流、免门控、零 LLM。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchInfo(ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	switch {
	case extractHelp(question):
		body := s.helpText() + "\n\n<!-- argus:help delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "help", len(body))
		return true
	case extractStatus(question):
		body := s.statusText(ctx, platform, owner, repo, prNum) + "\n\n<!-- argus:status delivery=" + delivery + " -->"
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, body, "status", len(body))
		return true
	}
	return false
}

// helpText 指令手册（目录单源渲染；fix 未启用时标注）。
func (s *Service) helpText() string {
	var b strings.Builder
	b.WriteString("## 📖 Argus 指令速查\n")
	for _, c := range cmdCatalog {
		alias := ""
		if len(c.aliases) > 0 {
			alias = "（同义：" + strings.Join(c.aliases, "/") + "）"
		}
		gate := ""
		if c.drive {
			gate = " 🔒"
		}
		fmt.Fprintf(&b, "- `%s`%s —— %s%s\n", c.usage, alias, c.desc, gate)
	}
	b.WriteString("\n**示例**\n")
	for _, c := range cmdCatalog[:3] {
		fmt.Fprintf(&b, "- %s\n", c.example)
	}
	b.WriteString("\n提示：mention 不必在句首（`请 @argus review` 也行）；引用行（`> …`）里的 @ 不触发；" +
		"🔒 指令需仓库写权限。每 PR 30 秒指令冷却。")
	if s.Cfg != nil && s.Cfg.FixAgent == "" {
		b.WriteString("\n（fix 未启用：需服务端配置 ARGUS_FIX_AGENT）")
	}
	return b.String()
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

// gateDeniedText 烧预算指令被门控拒绝的回帖。
const gateDeniedText = "🔒 该指令需要仓库写权限（协作者）才能触发，避免预算被路人评论消耗。" +
	"请联系仓库维护者执行，或由管理员设置 `ARGUS_COMMENT_GATE=open` 放开限制。"

// throttleNoticeText 指令撞冷却窗时的回帖（有感知，不再静默丢弃）。
const throttleNoticeText = "⏱️ 指令处理中（每 PR 30 秒冷却），请稍后再发。"

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
