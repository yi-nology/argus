// Package testkit 提供测试夹具共用的 shell 脚本拼装助手。测试里生成的 fake
// git/ocr 脚本需要把探针、状态文件等路径内插进脚本正文；TMPDIR/GOTMPDIR
// 可能含空格（macOS 的 Application Support 等），裸拼会生成坏脚本、让
// 夹具在带空格路径下静默失真（重定向跑偏、误报"git 应被调用"）。
package testkit

import "strings"

// Shq 把任意字符串引成 POSIX sh 单引号词：空格、通配符、$、换行都逐字还原，
// 内嵌单引号按 '\” 惯例闭合转接。生成脚本时路径一律经 Shq 内插。
func Shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
