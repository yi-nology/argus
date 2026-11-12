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

// ExtractTemplateFlag 从 review 命令补充说明里剥出 `--template <key>` 任务级
// 模板旗标（v6.11.0）。支持 `--template key` 与 `--template=key` 两种形态；
// 命中时返回 (key, 剥除旗标后的剩余说明, found=true)；旗标缺值或黏连多值返回
// bad=true（调用方截停报错，不吞意图——与 ParseRecheck 的 RecheckFPBad 同纪律）。
// 词表校验（standard|analyst|minimal）不在本层：commentcmd 是纯函数零依赖包，
// 归 service 用 reporttmpl.IsValid 复核。
func ExtractTemplateFlag(rest string) (key, remainder string, found, bad bool) {
	fields := strings.Fields(rest)
	var out []string
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		if tok == "--template" || strings.HasPrefix(tok, "--template=") {
			if found {
				return "", "", true, true // 重复旗标 = 歧义，截停
			}
			found = true
			if strings.HasPrefix(tok, "--template=") {
				key = strings.TrimPrefix(tok, "--template=")
				if key == "" {
					return "", "", true, true // --template= 空值
				}
			} else {
				if i+1 >= len(fields) || strings.HasPrefix(fields[i+1], "--") {
					return "", "", true, true // 缺值
				}
				i++
				key = fields[i]
			}
			continue
		}
		out = append(out, tok)
	}
	if !found {
		return "", strings.TrimSpace(rest), false, false
	}
	return key, strings.Join(out, " "), true, false
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
