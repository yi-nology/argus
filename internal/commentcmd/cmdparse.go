// Package commentcmd 评论指令词法层（@bot 指令目录/解析/词表的单源，v5.4.4 自
// service 门面剥出）：
//   - 指令目录（help 渲染、门控判定、冷却提示共用一张表，新增指令只改目录+对应
//     解析器，help 不会漏项）；
//   - 词边界前缀解析（CutCommand）与 fp 短码解析（ParseFPHex，fix/suppress/restore
//     三指令共用）；
//   - mention 词法（句首/句中/句尾 mention 提取、引用行豁免、词边界防误命中）；
//   - webhook 事件形态判定（IsCommentEvent/ParseCommentMeta：canonical comment.created
//     与旧词表 issue_comment/cr.note 折叠成归一视图）；
//   - 各指令解析器（review/recheck/fix/describe/changelog/testgen/suppress/restore）。
//
// 本包是纯函数包：不持有连接、不触平台 API、不依赖 Service——「指令长什么样」
// 与「指令怎么执行」分层。执行体在 service 门面（dispatch* / handle*）。
package commentcmd

import "strings"

// isWordByte 词边界判定（命令后紧跟字母/数字/下划线 = 其他单词，不命中）。
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// CutCommand 词边界前缀命令解析：question 以 cmd 开头（"describex" 等其他单词
// 不命中），返回去空白剩余段。边界判定必须在 TrimSpace 之前做（参数本身可以
// 字母开头，如 "testgen internal/x.go"）。
func CutCommand(question, cmd string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(question), cmd)
	if !ok || (rest != "" && isWordByte(rest[0])) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// ParseFPHex 解析指令剩余段的 fp 短码（"fp:" 前缀 + ≥6 位十六进制，取首
// token，归一小写）。fix/suppress/restore 三个指令解析器共用；指令路由归属
// 由各自前缀判定，与本函数无关。
func ParseFPHex(rest string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(rest), "fp:") {
		return "", false
	}
	fp := strings.TrimSpace(rest[len("fp:"):])
	if i := strings.IndexAny(fp, " \t\n"); i >= 0 {
		fp = fp[:i]
	}
	if len(fp) < 6 || len(fp) > 64 {
		return "", false
	}
	for _, r := range fp {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return "", false
		}
	}
	return strings.ToLower(fp), true
}
