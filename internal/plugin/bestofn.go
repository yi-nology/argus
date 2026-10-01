// bestofn.go —— 同质 best-of-N 采样聚合（v3.10.0，对应 MiMo Max Mode 的审查域形态）。
//
// 语义：同一插件同 prompt 跑 N 次（高危路径 opt-in，manifest.ReviewN>1），Dispatcher
// 聚合 N 份 findings：
//   - 同一问题（指纹/规范化评语匹配）被 ≥2 份采样报告 → 「采样复现 k/n」标注 +
//     严重度上调一级（low→medium→high）——复现是确定性的可信度信号，不经任何模型；
//   - 仅 1 份采样报告且 severity=low → 「单次采样」标注（人审降权提示）；
//   - N 份互不相同的发现全部保留（漏报防线：单次出现的真问题不丢）。
//
// 铁律对齐：聚合是纯确定性代码；R5 真值表照旧只读结构化计数，N 采样的任何
// 模型自评 verdict 不参与裁决。复现计数不进 Comment——指纹（Fingerprint 由
// 评语派生）与跨轮抑制/学习环身份必须稳定，标注放结构化字段 Finding.Samples，
// 由渲染层展示（v3.9.1 错账教训：标注文本入键会让身份漂移）。
package plugin

import (
	"fmt"
	"strconv"
	"strings"

	ekmathx "git.example.com/enjoydream/ekit/pkg/mathx"

	"git.example.com/enjoydream/agentkit/reportutil"
)

// MaxReviewN reviewN 配置上限（与 MiMo Max Mode N≈5 同量级；防配置滥用放大成本）。
const MaxReviewN = 5

// ClampReviewN 钳制 reviewN 到 [1, MaxReviewN]（0/负数 = 关闭采样，返回 1）。
// v3.82.0 引入 ekit ekmathx.Clamp（语义逐位一致，消除边界分支）。
func ClampReviewN(n int) int {
	return ekmathx.Clamp(n, 1, MaxReviewN)
}

// BumpSeverity 严重度上调一级（low→medium→high；high 封顶）。
// 词表钉死 {high, medium, low}，与 NormalizeSeverity 同源。
func BumpSeverity(s string) string {
	switch s {
	case SeverityLow:
		return SeverityMedium
	case SeverityMedium:
		return SeverityHigh
	default:
		return s
	}
}

// aggregateSamples 聚合 N 份**成功**采样结果（results 由调用方按采样序过滤，
// k=成功数由调用方统计——失败/取消采样不产生 Result）。v3.13.0：聚簇核心下沉
// agentkit reportutil.Aggregate（双通道签名=精确指纹 + file|规范化评语，eq=行号
// 相容——同文件两处不同位置是两个问题）；严重度合并与复现升级语义留在本包
// （审查域词表，不属于通用原语）。部分失败（k<n）时追加降级注记行。
func aggregateSamples(n, k int, results []Result) Result {
	var findings []Finding
	var verdict string
	artifacts := map[string]string{}
	notes := map[string]bool{}
	var noteOrder []string

	for _, r := range results {
		if r.Verdict != "" && verdict == "" {
			verdict = r.Verdict // R5 不读 verdict；保留首份仅供下游展示
		}
		for key, v := range r.Artifact {
			if _, dup := artifacts[key]; !dup {
				artifacts[key] = v
			}
		}
		for _, line := range strings.Split(r.Notes, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || notes[line] {
				continue
			}
			notes[line] = true
			noteOrder = append(noteOrder, line)
		}
		findings = append(findings, r.Findings...)
	}

	groups := reportutil.Aggregate(findings,
		func(f Finding) []string {
			// FingerprintNorm 一次规范化同时给出指纹与规范评语，免同一评语
			// 跑两遍 NormalizeComment（best-of-N 聚簇热路径）。
			fp, norm := FingerprintNorm(f.File, f.Comment, f.Line)
			return []string{"fp:" + fp, "nc:" + f.File + "|" + norm}
		},
		func(a, b Finding) bool { return linesCompatible(a.Line, b.Line) },
	)

	out := Result{Verdict: verdict, Artifact: artifacts}
	for _, g := range groups {
		f := g.Representative
		for _, it := range g.Items[1:] { // severity 合并（与首见者取高）
			f.Severity = HigherSeverity(f.Severity, it.Severity)
		}
		if g.Count >= 2 {
			// 复现 ≥2：确定性可信度信号 → 严重度上调一级 + 复现标注（k/n）
			f.Severity = BumpSeverity(f.Severity)
			f.Samples = strconv.Itoa(g.Count) + "/" + strconv.Itoa(n)
		} else if f.Severity == SeverityLow {
			// 仅单份采样且 low：保留（漏报防线）但标注降权提示
			f.Samples = "1/" + strconv.Itoa(n)
		}
		out.Findings = append(out.Findings, f)
	}
	if k > 0 && k < n {
		noteOrder = append(noteOrder,
			fmt.Sprintf("%s best-of-N：%d/%d 采样成功", DegradationMarker, k, n))
	}
	out.Notes = strings.Join(noteOrder, "\n")
	return out
}

// SamplesAnnotation 渲染层复现计数标注（数据来自 Finding.Samples="k/n"）。
// 返回空串 = 无采样标注（reviewN<=1 或非 low 单次样本）。
func SamplesAnnotation(f Finding) string {
	k, n, ok := parseSamples(f.Samples)
	if !ok || n <= 1 {
		return ""
	}
	if k >= 2 {
		return fmt.Sprintf("（采样复现 %d/%d）", k, n)
	}
	return "（单次采样，低复现）"
}

// confidenceLowThreshold 低信心警示阈值：模型自评低于此值时渲染层加注，
// 提示审阅者对该条目降权（Cursor Bugbot"没把握就不说"的 argus 变体——
// 说了但明示把握程度）。阈值 0.7：低于"较有把握"的直觉分界。
const confidenceLowThreshold = 0.7

// ConfidenceAnnotation 渲染层信心标注（数据来自 Finding.Confidence，可选）。
// 返回空串 = 未提供信心或信心充足。
func ConfidenceAnnotation(f Finding) string {
	if f.Confidence <= 0 || f.Confidence >= confidenceLowThreshold {
		return ""
	}
	return fmt.Sprintf("（⚠️ 模型低信心 %.0f%%，请审阅者自行验证）", f.Confidence*100)
}

func parseSamples(s string) (k, n int, ok bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return 0, 0, false
	}
	k, err1 := strconv.Atoi(s[:i])
	n, err2 := strconv.Atoi(s[i+1:])
	if err1 != nil || err2 != nil || k < 0 || n <= 0 {
		return 0, 0, false
	}
	return k, n, true
}
