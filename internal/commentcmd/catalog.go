// catalog.go —— @bot 评论指令目录单源（v5.3.0 商业化打磨，v5.4.4 迁 commentcmd）。
//
// help 渲染、门控判定（IsDriveCommand）、冷却提示（MatchAnyCommand）共用一张表
// ——新增指令只改目录 + 对应解析器，help 不会漏项。
//
// 商业化三件：
//   - help/status 免节流（查询类永远畅通）；
//   - 烧预算指令门控（ARGUS_COMMENT_GATE=collaborator 默认）：review/recheck/fix/
//     testgen 仅协作者可触发，防公开仓路人评论白烧预算；问答/查询/误报反馈类
//     不受门控。
package commentcmd

import (
	"fmt"
	"strings"
)

// cmdSpec 指令目录项。
type cmdSpec struct {
	word    string   // word 主命令词（CutCommand 目标）
	aliases []string // aliases 同义词（help 展示与 MatchAnyCommand 共用）
	usage   string   // usage 用法（不含 @bot 前缀）
	example string   // example 示例
	desc    string   // desc 一句话说明
	drive   bool     // drive 烧预算/改状态：触发者需写权限（ARGUS_COMMENT_GATE）
	info    bool     // info 查询类：免节流永远可达
}

// cmdCatalog 指令目录（help 单源）。order 即 help 展示序。
var cmdCatalog = []cmdSpec{
	{word: "review", aliases: []string{"评审", "重审", "重新审查", "重新评审", "再审", "重跑评审", "rerun", "re-review"},
		usage: "review [补充说明]", example: "请 @argus review 重点看登录模块",
		desc: "触发一轮全量重审并回执任务面", drive: true},
	{word: "recheck", aliases: []string{"复核"},
		usage: "recheck fp:短码 [补充]", example: "@argus recheck fp:ab12cd34 是不是误报",
		desc: "复核单条意见：对照 diff 裁决 成立/误报/存疑，附证据与处置建议", drive: true},
	{word: "ask", aliases: []string{},
		usage: "ask <问题>", example: "请 @argus ask 这个改动会影响性能吗",
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

// cmdWords 目录全部触发词（主词 + 同义词），MatchAnyCommand 共用。
func cmdWords() []string {
	var out []string
	for _, c := range cmdCatalog {
		out = append(out, c.word)
		out = append(out, c.aliases...)
	}
	return out
}

// MatchAnyCommand question 是否以任一指令词开头（冷却提示用：指令被冷却要有
// 感知，自由文本被冷却保持静默防刷屏）。
func MatchAnyCommand(question string) bool {
	for _, w := range cmdWords() {
		if _, ok := CutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// IsDriveCommand question 是否为烧预算指令形态（review/recheck/fix/testgen）。
// 权限门控在 HandleComment 统一执行，门控点只此一处。
func IsDriveCommand(question string) bool {
	if _, _, k := ParseRecheck(question); k != RecheckNone {
		return true
	}
	if _, ok := ExtractReviewCmd(question); ok {
		return true
	}
	if _, ok := ExtractFix(question); ok {
		return true
	}
	if _, ok := ExtractTestGen(question); ok {
		return true
	}
	return false
}

// ExtractHelp 解析 help/帮助/命令/指令。
func ExtractHelp(question string) bool {
	for _, w := range []string{"help", "帮助", "命令", "指令"} {
		if _, ok := CutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// ExtractStatus 解析 status/状态/进度。
func ExtractStatus(question string) bool {
	for _, w := range []string{"status", "状态", "进度"} {
		if _, ok := CutCommand(question, w); ok {
			return true
		}
	}
	return false
}

// HelpText 指令手册（目录单源渲染；fixEnabled=false 时标注 fix 未启用）。
func HelpText(fixEnabled bool) string {
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
	if !fixEnabled {
		b.WriteString("\n（fix 未启用：需服务端配置 ARGUS_FIX_AGENT）")
	}
	return b.String()
}

// GateDeniedText 烧预算指令被门控拒绝的回帖。
const GateDeniedText = "🔒 该指令需要仓库写权限（协作者）才能触发，避免预算被路人评论消耗。" +
	"请联系仓库维护者执行，或由管理员设置 `ARGUS_COMMENT_GATE=open` 放开限制。"

// ThrottleNoticeText 指令撞冷却窗时的回帖（有感知，不再静默丢弃）。
const ThrottleNoticeText = "⏱️ 指令处理中（每 PR 30 秒冷却），请稍后再发。"
