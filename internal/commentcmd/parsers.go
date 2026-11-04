// parsers.go —— 显式指令解析器（v5.4.4 自 service 迁入）。
package commentcmd

import "strings"

// reviewCmdWords 显式重审命令词表。
// 「recheck fp:/复核 fp:」形态归单条复核命令（ParseRecheck 先行消费）。
var reviewCmdWords = []string{
	"re-review", "recheck", "review", "rerun",
	"重新审查", "重新评审", "重跑评审", "评审", "复核", "重审", "再审",
}

// ExtractReviewCmd 解析显式重审命令：命中返回命令后的补充说明（可空）。
// 余下带 fp: 的形态不吞进重审（单条复核语义，交 ParseRecheck/后续路由）。
func ExtractReviewCmd(question string) (string, bool) {
	for _, w := range reviewCmdWords {
		rest, ok := CutCommand(question, w)
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.ToLower(rest), "fp:") {
			return "", false
		}
		return rest, true
	}
	return "", false
}

// ExtractDescribe 解析 describe 命令（前缀 describe，其余为可选补充要求）。
func ExtractDescribe(question string) (string, bool) {
	return CutCommand(question, "describe")
}

// ExtractChangelog 解析 changelog 命令（其余为可选补充要求）。
func ExtractChangelog(question string) (string, bool) {
	return CutCommand(question, "changelog")
}

// ExtractTestGen 解析 testgen 命令（前缀 testgen，可带目标文件路径过滤）。
func ExtractTestGen(question string) (string, bool) {
	return CutCommand(question, "testgen")
}

// ExtractFix 解析 fix fp:短码 指令（词边界 + fp 短码 ≥6 位十六进制）。
// 不匹配时返回 false（落回后续路由，不吞其他意图）。
func ExtractFix(question string) (string, bool) {
	rest, ok := CutCommand(question, "fix")
	if !ok {
		return "", false
	}
	return ParseFPHex(rest)
}

// RecheckKind ParseRecheck 的裁决形态。
type RecheckKind int

const (
	// RecheckNone 非复核指令（交后续路由）。
	RecheckNone RecheckKind = iota
	// RecheckFP recheck/复核 fp:短码 且短码合法。
	RecheckFP
	// RecheckFPBad recheck/复核 + fp: 前缀但短码非法（回错误，不落后续路由）。
	RecheckFPBad
)

// ParseRecheck 解析复核指令：命令词（recheck/复核）+ fp:短码（≥6 位十六进制）。
// 命中命令词但不带 fp: 的形态（「复核一下这个 PR」）返回 RecheckNone——那是
// 全量重审语义，归 review 命令/意图路由；带 fp: 而短码非法则截停报错（不得
// 悄悄滑进重审或 Q&A——用户指的是单条复核，吞掉意图比报错更糟）。
// note 为 fp 短码后的补充说明（可空），作为复核上下文进提示词。
func ParseRecheck(question string) (fp, note string, kind RecheckKind) {
	for _, cmd := range []string{"recheck", "复核"} {
		rest, ok := CutCommand(question, cmd)
		if !ok {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(rest), "fp:") {
			return "", "", RecheckNone
		}
		fp, valid := ParseFPHex(rest)
		if !valid {
			return "", "", RecheckFPBad
		}
		after := strings.TrimSpace(rest[len("fp:"):])
		if i := strings.IndexAny(after, " \t\n"); i >= 0 {
			note = strings.TrimSpace(after[i:])
		}
		return fp, note, RecheckFP
	}
	return "", "", RecheckNone
}
