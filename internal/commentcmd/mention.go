// mention.go —— @bot mention 词法（v5.4.4 自 service 迁入）。
package commentcmd

import "strings"

// ExtractQuestion 从评论正文提取 @bot 后的指令文本（v5.2.0：mention 不再要求
// 行首）。三种形态都触发：句首「@bot 评审一下」、句中「请 @bot 复核 fp:…」、
// 句尾「帮我看看这个 @bot」（句尾无后文时取整条评论去 mention 的文本作指令）。
// 守卫：词边界（邮箱样式 a@bot、更长用户名 @bot2 不误命中）；引用行（> …）里的
// mention 是转述不是指令，跳过。无 mention 或纯 mention 评论返回空串（忽略）。
func ExtractQuestion(body, botUsername string) string {
	if botUsername == "" {
		return ""
	}
	mention := "@" + strings.ToLower(botUsername)
	lower := strings.ToLower(body)
	idx := indexMention(lower, mention, 0)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(body[idx+len(mention):])
	if rest != "" {
		if r, ok := CutCommand(rest, "ask"); ok {
			rest = r
		}
		return rest
	}
	return strings.TrimSpace(stripMentions(body, botUsername))
}

// mentionBoundaryBefore 前边界：@ 前是字母/数字/_.-（邮箱 a@bot、user@host）
// 则不是 mention。. 放进守卫是为挡「mail me at a.b@bot」这类邮箱残段。
func mentionBoundaryBefore(s string, idx int) bool {
	if idx == 0 {
		return true
	}
	c := s[idx-1]
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '.')
}

// mentionBoundaryAfter 后边界：mention 尾后紧跟用户名字符（@bot2 是另一个账号）
// 则不命中。句号/空白/括号等都视为边界（"@bot. 请评审" 正常触发）。
func mentionBoundaryAfter(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	c := s[end]
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-')
}

// inQuoteLine 该位置所在行是否引用行（行首 > 后随空白）——引用里的 mention
// 是对他人话语的转述，不是给 bot 的指令。多行评论引用回复（GitHub/GitLab
// 回帖自动带 > 原文）是常态，不挡会把转述当指令误触发。
func inQuoteLine(s string, idx int) bool {
	lineStart := strings.LastIndexByte(s[:idx], '\n') + 1
	line := strings.TrimLeft(s[lineStart:], " \t")
	return strings.HasPrefix(line, ">")
}

// indexMention 自 from 起查找下一个可触发的 mention 位置（-1 = 无）。
// lower 必须是全小写串；mention 也是小写形态。
func indexMention(lower, mention string, from int) int {
	for i := from; i <= len(lower)-len(mention); {
		rel := strings.Index(lower[i:], mention)
		if rel < 0 {
			return -1
		}
		idx := i + rel
		if mentionBoundaryBefore(lower, idx) && mentionBoundaryAfter(lower, idx+len(mention)) &&
			!inQuoteLine(lower, idx) {
			return idx
		}
		i = idx + 1
	}
	return -1
}

// stripMentions 去除全部可触发 mention（句尾兜底路径用）。
func stripMentions(body, botUsername string) string {
	mention := "@" + strings.ToLower(botUsername)
	lower := strings.ToLower(body)
	var b strings.Builder
	from := 0
	for {
		idx := indexMention(lower, mention, from)
		if idx < 0 {
			b.WriteString(body[from:])
			return b.String()
		}
		b.WriteString(body[from:idx])
		from = idx + len(mention)
	}
}

// ExtractSuppressFP 解析 "suppress fp:xxxxxxxx" 指令（fp 短码 ≥6 位十六进制）。
func ExtractSuppressFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "suppress"))
	return ParseFPHex(rest)
}

// ExtractRestoreFP 解析 "restore fp:xxxxxxxx" 指令。与 ExtractSuppressFP 同形
// 但路由独立：restore 指令不得被 suppress 分支截胡。
func ExtractRestoreFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "restore"))
	return ParseFPHex(rest)
}
