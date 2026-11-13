package requirement

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// TestVerifyFindingsEvidenceWiring v6.18.0 Finding 级证据校验接线缝（finish 步骤）：
// 改动集（Diff.Files）+ 触达集（ArtifactCodegraphTouched 渲染面）交叉校验；
// 高危（critical/high）证据不可验证降级 medium 退出 R5 否决面 + ⚠️ 软标注；
// 可验证条目与空证据条目零变化。缝函数由 finish 在 applyRepoPolicies 后调用
// （与 synthesisAnalyst 的 VerifyClaimsEvidence 同族、不同面）。
func TestVerifyFindingsEvidenceWiring(t *testing.T) {
	task := analystTask("standard") // Diff.Files = [x.go]
	task.Artifacts[plugin.ArtifactCodegraphTouched] = "命中 x.go:3 与 touch.go:7"
	merged := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "x.go", Line: 3, Severity: "high", Comment: "a",
			Evidence: "x.go:3 改动侧缺判空"}, // 改动集内 → 不动
		{File: "x.go", Line: 4, Severity: "critical", Comment: "b",
			Evidence: "ghost.go:9 未鉴权"}, // 集外高危 → 降级 medium + ⚠️
		{File: "x.go", Line: 5, Severity: "low", Comment: "c",
			Evidence: "touch.go:7 消费侧依赖旧语义"}, // 触达集内 → 不动
		{File: "x.go", Line: 6, Severity: "medium", Comment: "d"}, // 空证据 → 不判
	}}

	verifyFindingsEvidence(merged, task)

	fs := merged.Findings
	if fs[0].Severity != "high" || strings.Contains(fs[0].Evidence, "⚠️") {
		t.Fatalf("改动集内 high 不应动: sev=%q evid=%q", fs[0].Severity, fs[0].Evidence)
	}
	if fs[1].Severity != "medium" || !strings.Contains(fs[1].Evidence, "⚠️ 证据待验证") {
		t.Fatalf("集外 critical 应降级 medium + ⚠️: sev=%q evid=%q", fs[1].Severity, fs[1].Evidence)
	}
	if fs[2].Severity != "low" || strings.Contains(fs[2].Evidence, "⚠️") {
		t.Fatalf("触达集内 low 不应动: sev=%q evid=%q", fs[2].Severity, fs[2].Evidence)
	}
	if fs[3].Severity != "medium" || fs[3].Evidence != "" {
		t.Fatalf("空证据不应动: %+v", fs[3])
	}
}
