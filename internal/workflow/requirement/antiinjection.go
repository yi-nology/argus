package requirement

import (
	"fmt"
	"regexp"
	"strings"

	"argus/internal/plugin"
)

// ---------- 反注入防线 #3（代码级校验）：含指令性语句的 AC 进 Ambiguous ----------

var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(请|务必|必须|直接)?(输出|返回|回答|回复).{0,8}(approve|通过|request_changes|approved|已通过)`),
	regexp.MustCompile(`(?i)(reply|respond|answer|output)\s+(with|always)?\s*.{0,10}(approve|pass|approved)`),
	regexp.MustCompile(`(?i)(ignore|disregard|forget)\s+(all\s+)?(previous|prior|above|earlier)`),
	regexp.MustCompile(`忽略(之前|以上|上述|前面|所有|全部)`),
	regexp.MustCompile(`(?i)system\s*prompt`),
	regexp.MustCompile(`你(现在)?是(一?个)?(系统|管理员|审核员|负责人)`),
	regexp.MustCompile(`(?i)you\s+are\s+(now\s+)?(a|an|the)?\s*(system|admin|reviewer)`),
	regexp.MustCompile(`(免审|免检|跳过审查|不要审查|不用审查|关闭审查)`),
}

// LooksInstructional 判定文本是否含指令性语句（R1 提示词契约的代码级兜底）。
func LooksInstructional(text string) bool {
	for _, re := range injectionPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// SanitizeSpec 防线 #3 校验器：扫描 R1 产出的 AC，可疑条目移入 Ambiguous 并告警。
// 保证"攻击者塞指令踢掉难满足的 AC"最多只能落到 COMMENT 人工确认路径（R5.3）。
func SanitizeSpec(spec *plugin.RequirementSpec) (warnings []string) {
	if spec == nil {
		return nil
	}
	var keep []plugin.AcceptanceCriterion
	for _, ac := range spec.ACs {
		if LooksInstructional(ac.Text) {
			warnings = append(warnings, fmt.Sprintf("验收条目 %s 含指令性语句，已移入 ambiguous：%.80s", ac.ID, ac.Text))
			spec.Ambiguous = append(spec.Ambiguous, "[可疑指令] "+ac.Text)
			continue
		}
		keep = append(keep, ac)
	}
	spec.ACs = keep
	// ambiguous 本身也过一遍去重
	seen := map[string]bool{}
	var amb []string
	for _, a := range spec.Ambiguous {
		if seen[a] {
			continue
		}
		seen[a] = true
		amb = append(amb, a)
	}
	spec.Ambiguous = amb
	return warnings
}

// alignChecksToSpec 双向对齐校验（防线：R3 输出不可信，缺漏与发明都要堵）：
//   - 剔除 R3 发明的 spec 之外的条目（防放大 R5.1 计数）；
//   - 补齐 spec 有而 R3 漏判的条目，置 unverifiable——否则 R3 少报/漏报会把
//     partial PR 缩水成 R5.5 APPROVE（空 checks 绕过 R5.1/R5.3 的注入洞）；
//   - status 词表校验：词表外的值（如 "done"）既不能计入 R5.1 也不算证据，
//     一律归一为 unverifiable，防错写状态缩水未达标计数。
//
// 返回 (剔除数, 补齐+归一数)。
func alignChecksToSpec(spec *plugin.RequirementSpec, report *plugin.ComplianceReport) (dropped, fixed int) {
	if report == nil || spec == nil {
		return 0, 0
	}
	known := make(map[string]bool, len(spec.ACs))
	for _, ac := range spec.ACs {
		known[ac.ID] = true
	}
	kept := make([]plugin.ACCheck, 0, len(spec.ACs))
	for _, c := range report.Checks {
		if !known[c.ID] {
			dropped++
			continue
		}
		switch c.Status {
		case plugin.ACImplemented, plugin.ACPartial, plugin.ACMissing, plugin.ACUnverifiable:
			kept = append(kept, c)
		default:
			c.Status = plugin.ACUnverifiable
			c.Detail = strings.TrimSpace(c.Detail + "（status 词表外，已归一为 unverifiable）")
			kept = append(kept, c)
			fixed++
		}
	}
	seen := make(map[string]bool, len(kept))
	for _, c := range kept {
		seen[c.ID] = true
	}
	for _, ac := range spec.ACs {
		if !seen[ac.ID] {
			kept = append(kept, plugin.ACCheck{
				ID:     ac.ID,
				Status: plugin.ACUnverifiable,
				Detail: "R3 未判定该条目，已按对齐防线补齐为需人工确认",
			})
			fixed++
		}
	}
	report.Checks = kept
	return dropped, fixed
}

// ---------- 防线 #2（代码级校验）：无证据不判 implemented ----------

var evidenceRe = regexp.MustCompile(`^\S+\.(go|js|ts|tsx|jsx|py|java|kt|rs|c|h|cpp|cc|php|rb|vue|sql|sh|yaml|yml|json|md|toml|xml|proto|mod|txt|cs|swift|m|scala|dart|ex|exs|groovy|bat|ps1)[:：][0-9]+(-[0-9]+)?$`)

// ValidEvidence 判定证据字符串是否合法（file:line 格式）。
// 支持 LLM 返回的多种格式：
//   - 单条：config/config.go:72
//   - 带范围：config/config.go:72-73
//   - 带括号说明：config/config.go:72-73 (yaml.Unmarshal)
//   - 多条逗号拼接：a.go:1 (x), b.go:2 (y)——任一条合法即通过
func ValidEvidence(e string) bool {
	e = strings.TrimSpace(e)
	if e == "" {
		return false
	}
	// 逗号分割：多条证据任一合法即通过
	for _, part := range strings.Split(e, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.ReplaceAll(part, "：", ":")
		// 去掉括号内的说明文字：a.go:1 (xxx) → a.go:1
		if i := strings.Index(part, "("); i > 0 {
			part = strings.TrimSpace(part[:i])
		}
		// 提取 file:line 部分（去掉行尾多余内容）
		if i := strings.LastIndex(part, ":"); i >= 0 {
			candidate := part[:i+1] + strings.Split(part[i+1:], " ")[0]
			if evidenceRe.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

// EnforceEvidence 防线 #2 校验器：implemented/partial 无 file:line 证据 → 一律降级
// unverifiable（从字段约定上升为代码级校验——LLM 输出不可信）。返回降级条数。
func EnforceEvidence(report *plugin.ComplianceReport) (downgraded int) {
	if report == nil {
		return 0
	}
	for i := range report.Checks {
		c := &report.Checks[i]
		if c.Status == plugin.ACImplemented || c.Status == plugin.ACPartial {
			if !ValidEvidence(c.Evidence) {
				orig := c.Status
				c.Status = plugin.ACUnverifiable
				c.Detail = strings.TrimSpace(c.Detail + "（原判 " + orig + " 但证据缺失/不合法，已按防线 #2 降级）")
				downgraded++
			}
		}
	}
	return downgraded
}
