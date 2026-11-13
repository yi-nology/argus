// evidence.go —— 证据可信度校验（v6.17.0 方向 A，评估报告 2026-11-12 实锤缺口）。
//
// 背景：SanitizeAnalyst 只钳制字段合法性/条数/长度，未验证每个 file:line 证据
// 是否真实成立（评估报告：「严格验证每个 file:line、声明和风险链是否真实成立」）。
// 方向 A 落地：Evidence 证据记录 + 代码级校验（文件在改动集/触达集、行号有效），
// 高风险声明（missing/inconsistent）证据不可验证时降级标注——提高每条意见的
// 可采信程度，而非增加 Reviewer 数量。
//
// 纪律：
//   - 纯函数（无文件系统 I/O）：校验基于 task.Diff.Files 与 ArtifactCodegraphTouched
//     渲染面交叉——不引新 Artifact 键、不动 runner 注入面；
//   - 软标注不丢弃：证据不可验证的 claim 保留原文，Evidence 追加「⚠️ 证据待验证」
//     ——声明本身可能是真问题，只是证据形态无法确认（不静默丢弃真问题）；
//   - 高风险状态降级：missing/inconsistent（深度评审最高危信号）证据格式解析失败
//     时 Status 降级 partial——状态错误的声明比缺失更有害（NormalizeClaimStatus 同
//     纪律），但降级而非删除（保留线索）。
package plugin

import (
	"regexp"
	"strconv"
	"strings"
)

// EvidenceRef 从 Claim.Evidence 文本解析的 file:line 代码证据引用。
type EvidenceRef struct {
	File string // 仓库相对路径（改动集内/触达集内可校验）
	Line int    // 行号（1-based；0 = 解析失败）
}

// evidenceRefRe file:line 引用正则：支持 `path/to/file.go:123` 形态（文件路径
// 可含中文/短横/下划线/点，行号 1-6 位数字；冒号后非数字的如 URL 不匹配）。
var evidenceRefRe = regexp.MustCompile(`([\w./-]+\.[A-Za-z0-9]{1,8}:\d{1,6})`)

// ParseEvidenceRefs 从 Claim.Evidence 自由文本解析全部 file:line 引用。
// 自由文本里可含叙述（"改动侧 file.go:12 与消费侧 x.go:34 不一致"）。
// 返回空切片 = 无 file:line 形态引用（可能是纯文字差距说明）。
func ParseEvidenceRefs(s string) []EvidenceRef {
	if s == "" {
		return nil
	}
	matches := evidenceRefRe.FindAllStringSubmatch(s, -1)
	seen := map[string]bool{}
	var refs []EvidenceRef
	for _, m := range matches {
		if len(m) != 2 {
			continue
		}
		ref := parseOneRef(m[1])
		if ref == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		refs = append(refs, *ref)
	}
	return refs
}

// parseOneRef "path/to/file.go:123" → EvidenceRef。
func parseOneRef(s string) *EvidenceRef {
	idx := strings.LastIndexByte(s, ':')
	if idx <= 0 {
		return nil
	}
	file := s[:idx]
	line, err := strconv.Atoi(s[idx+1:])
	if err != nil || line <= 0 {
		return nil
	}
	return &EvidenceRef{File: file, Line: line}
}

// EvidenceResult 单条 Claim 的证据校验结论。
type EvidenceResult struct {
	Refs     []EvidenceRef // 解析出的引用（空 = 无 file:line）
	Verified bool          // 全部引用可交叉验证（或无引用时=纯文字，不判）
	Note     string        // 校验注记（追加到 Claim.Evidence 末尾）
}

// VerifyClaimEvidence 校验单条 Claim 的证据（交叉校验：file 在 changed 集或
// touched 集内且行号 > 0）。无 file:line 引用时判定为「纯文字差距说明」——
// 不算证据缺陷（missing/inconsistent 的 evidence 可以是差距叙述），Verified=false
// 但 Note 不标注。
// changed：改动文件清单（Diff.Files 的 Path 列表）；touched：ArtifactCodegraphTouched
// 渲染面里出现过的文件路径（可为空）。
func VerifyClaimEvidence(c Claim, changed, touched []string) EvidenceResult {
	refs := ParseEvidenceRefs(c.Evidence)
	if len(refs) == 0 {
		// 纯文字：无引用可校验，不算缺陷
		return EvidenceResult{Refs: nil, Verified: true, Note: ""}
	}
	inSet := fileSet(changed, touched)
	verified := true
	var badRefs []EvidenceRef
	for _, ref := range refs {
		if ref.Line > 0 && inSet[ref.File] {
			continue
		}
		verified = false
		badRefs = append(badRefs, ref)
	}
	note := ""
	if !verified {
		var b strings.Builder
		b.WriteString(" ⚠️ 证据待验证（")
		for i, r := range badRefs {
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(r.File)
			b.WriteString(":")
			b.WriteString(strconv.Itoa(r.Line))
		}
		b.WriteString(" 不在改动集/触达集，或行号无效）")
		note = b.String()
	}
	return EvidenceResult{Refs: refs, Verified: verified, Note: note}
}

// fileSet changed + touched 的去重集合。
func fileSet(changed, touched []string) map[string]bool {
	m := make(map[string]bool, len(changed)+len(touched))
	for _, f := range changed {
		if f != "" {
			m[f] = true
		}
	}
	for _, f := range touched {
		if f != "" {
			m[f] = true
		}
	}
	return m
}

// VerifiedAnnotation 渲染标注：校验通过 ✅ / 待验证 ⚠️（Evidence 文本追加用）。
func VerifiedAnnotation(v EvidenceResult) string {
	if len(v.Refs) == 0 {
		return "" // 纯文字不标注
	}
	return v.Note
}

// HighRiskClaimStatus 判定高风险声明状态（missing/inconsistent = 深度评审
// 最高危信号，证据必须可验证；partial/implemented/undeclared 证据缺陷只标注）。
func HighRiskClaimStatus(status string) bool {
	return status == ClaimMissing || status == ClaimInconsistent
}

// VerifyClaimsEvidence 批量校验 sec.Claims 证据并就地更新（Evidence 追加
// ⚠️ 注记；高风险状态证据不可验证时降级 partial）。changed=改动文件清单；
// touched=触达集文件清单（ArtifactCodegraphTouched 提取）。返回校验通过/总数。
func VerifyClaimsEvidence(sec *AnalystSections, changed, touched []string) (verified, total int) {
	if sec == nil {
		return 0, 0
	}
	for i := range sec.Claims {
		c := &sec.Claims[i]
		res := VerifyClaimEvidence(*c, changed, touched)
		total++
		if res.Verified {
			verified++
			continue
		}
		c.Evidence += VerifiedAnnotation(res)
		// 高风险状态证据不可验证 → 降级 partial（保留声明本身，状态不再
		// 断言最高危——渲染层状态图标随之变化，降低误报权重）
		if HighRiskClaimStatus(c.Status) {
			c.Status = ClaimPartial
		}
	}
	return verified, total
}
