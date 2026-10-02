// cmdparse.go —— 评论指令解析共用件（v3.95.0 收敛：fix/suppress/restore 的 fp
// 短码尾部与 describe/testgen/fix 的词边界前缀此前各持一份逐字复制）。
package service

import "strings"

// isWordByte 词边界判定（命令后紧跟字母/数字/下划线 = 其他单词，不命中）。
func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// cutCommand 词边界前缀命令解析：question 以 cmd 开头（"describex" 等其他单词
// 不命中），返回去空白剩余段。边界判定必须在 TrimSpace 之前做（参数本身可以
// 字母开头，如 "testgen internal/x.go"）。
func cutCommand(question, cmd string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(question), cmd)
	if !ok || (rest != "" && isWordByte(rest[0])) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// parseFPHex 解析指令剩余段的 fp 短码（"fp:" 前缀 + ≥6 位十六进制，取首
// token，归一小写）。fix/suppress/restore 三个指令解析器共用；指令路由归属
// 由各自前缀判定，与本函数无关。
func parseFPHex(rest string) (string, bool) {
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
