// fence_norm.go —— jsonrepair 消费面的 fence 规范化（v5.7.2）。
//
// pkg v0.38.0 的 StripCodeFence 从正则收紧为行首扫描（嵌套围栏修复 #241
// 的同批改动），"说明文字+同段 ```json" 形态（agent 输出常态，acpx 实弹
// 测试桩即此形态）不再被剥，Unmarshal 报"空输入"。本 helper 在各消费点
// 前统一规范化：非行首 fence 开栏（```后跟语言标记+换行）前补换行——
// 行内 code span（```x``` 无换行）不受影响，行首 fence 原样通过。

package plugin

import "regexp"

var looseFenceRe = regexp.MustCompile("([^\\n`])`{3,}([a-zA-Z0-9_+-]*)[ \\t]*\\n")

// NormalizeFence 非行首 fence 开栏前补换行（幂等：行首形态不匹配）。
func NormalizeFence(s string) string {
	return looseFenceRe.ReplaceAllString(s, "$1\n```$2\n")
}
