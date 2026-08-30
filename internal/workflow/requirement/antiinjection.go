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

// ---------- 防线 #2（代码级校验）：无证据不判 implemented ----------

var evidenceRe = regexp.MustCompile(`^\S+\.(go|js|ts|tsx|jsx|py|java|kt|rs|c|h|cpp|cc|php|rb|vue|sql|sh|yaml|yml|json|md|toml|xml|proto|mod|txt|cs|swift|m|scala|dart|ex|exs|groovy|bat|ps1)[:：][0-9]+(-[0-9]+)?$`)

// ValidEvidence 证据必须是 file:line 形态。
func ValidEvidence(e string) bool {
	e = strings.TrimSpace(e)
	if e == "" {
		return false
	}
	e = strings.ReplaceAll(e, "：", ":")
	if i := strings.LastIndex(e, ":"); i >= 0 {
		e = e[:i+1] + strings.Split(e[i+1:], " ")[0]
	}
	return evidenceRe.MatchString(e)
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
