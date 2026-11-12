package reportview

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
)

func analystFixture() Report {
	return Report{
		TaskID:      "t1",
		HeadSHA:     "abc",
		Version:     "v6.10.0",
		Template:    reporttmpl.Analyst,
		VerdictText: "⛔ 存在高危问题，请修复后再提交",
		Rule:        "R5.2",
		Meta:        true,
		Plugins:     []string{"correctness"},
		Findings: []plugin.Finding{
			{File: "b.rs", Line: 350, Severity: "critical", Comment: "pid 复用串数据", By: "correctness"},
			{File: "a.rs", Line: 444, Severity: "high", Comment: "off_cpu 口径从微秒变事件个数",
				Chain: "改动侧 JstackProbeAgent.java:162 恒记 1；消费侧 ebpf_dispatcher.rs:454 直接当微秒", By: "correctness",
				SuggestedFix: "恢复 toMillis 并在 jstack.rs 保留 ×1000"},
			{File: "c.rs", Line: 279, Severity: "medium", Comment: "多余转换空壳", By: "correctness"},
			{File: "d.rs", Line: 50, Severity: "low", Comment: "魔法数散落", By: "correctness"},
		},
		Analyst: &plugin.AnalystSections{
			Claims: []plugin.Claim{
				{Claim: "删旧实现并入新类", Evidence: "a.java:110-238", Status: plugin.ClaimImplemented},
				{Claim: "agent 退出时卸载", Evidence: "profiler.rs:507-517 只覆盖被监控进程退出", Status: plugin.ClaimMissing},
				{Claim: "off_cpu 单位变更", Evidence: "未在描述声明", Status: plugin.ClaimUndeclared},
				{Claim: "目录上限 20", Evidence: "pendingDirs>20 写完第 21 个才停", Status: plugin.ClaimPartial},
				{Claim: "与既有 ebpdf 口径一致", Evidence: "units=microseconds 矛盾", Status: plugin.ClaimInconsistent},
			},
			Design: &plugin.Design{
				Triggered:   true,
				ObjectGraph: []string{"Profiler 创建者 profiler.rs:255 → 持有者 Arc → 写者 update_config → 读者 collect → 与进程同寿"},
				Cohesion:    []string{"jstack.rs:279 职责混杂"},
				Coupling:    []string{"同一事实两处采样 jstack.rs:155 vs JstackProbeAgent.java:240"},
				Migration:   "P0 加 Drop 实现；P1 拆采集器。不阻塞合入。如果只做一件事：先修 R1 口径。",
			},
			TestGaps:      []string{"ready 门闩 0 覆盖"},
			OpenQuestions: []string{"off_cpu 口径是否确要改？核实方法：git log --oneline --grep=off_cpu"},
		},
	}
}

func TestRenderAnalystFull(t *testing.T) {
	out := Render(analystFixture())
	for _, want := range []string{
		"## 🔬 Argus 深度审查报告",
		"### 一、需求符合性（声明 vs 代码）",
		"✅ 实现", "❌ 未实现", "➕ 超出声明", "⚠️ 部分", "❗ 口径不一致",
		"### 二、风险", "🔴 R1", "🟠 R2", "证据链", "建议修复",
		"### 三、代码坏味", "魔法数",
		"### 四、结构维度", "对象图", "内聚", "耦合", "迁移建议", "如果只做一件事",
		"### 五、测试缺口", "ready 门闩",
		"### 六、待确认", "git log --oneline",
		"### 结论: ⛔ 存在高危问题",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("analyst 渲染缺 %q:\n%s", want, out)
		}
	}
	// 坏味组不含 high（分组正确性）。
	riskIdx := strings.Index(out, "### 二、风险")
	smellIdx := strings.Index(out, "### 三、代码坏味")
	if riskIdx < 0 || smellIdx < 0 || riskIdx > smellIdx {
		t.Fatalf("风险/坏味段序错乱")
	}
	riskBlock := out[riskIdx:smellIdx]
	if strings.Contains(riskBlock, "魔法数") || strings.Contains(riskBlock, "多余转换") {
		t.Fatalf("high/critical 组混入坏味条目:\n%s", riskBlock)
	}
}

func TestRenderAnalystDegradedFallsBackToStandardSkeleton(t *testing.T) {
	r := analystFixture()
	r.Analyst = nil // 综合段降级
	out := Render(r)
	if strings.Contains(out, "需求符合性") || strings.Contains(out, "结构维度") {
		t.Fatalf("降级后不应有 analyst 段落:\n%s", out)
	}
	if !strings.Contains(out, "### 二、风险") || !strings.Contains(out, "### 结论: ") {
		t.Fatalf("降级应保留风险链+结论骨架:\n%s", out)
	}
}

func TestRenderAnalystN0SectionsOmitted(t *testing.T) {
	r := analystFixture()
	r.Analyst = &plugin.AnalystSections{
		Claims: []plugin.Claim{{Claim: "c", Evidence: "x:1", Status: plugin.ClaimImplemented}},
		Design: &plugin.Design{Triggered: false}, // 未命中：整段省略
	}
	out := Render(r)
	if strings.Contains(out, "结构维度") || strings.Contains(out, "测试缺口") || strings.Contains(out, "待确认") {
		t.Fatalf("N=0 段应省略:\n%s", out)
	}
	if !strings.Contains(out, "需求符合性") {
		t.Fatalf("claims 非空应渲染:\n%s", out)
	}
}

func TestRenderMinimal(t *testing.T) {
	r := analystFixture()
	r.Template = reporttmpl.Minimal
	out := Render(r)
	if !strings.Contains(out, "## 📋 Argus 审查结论") || !strings.Contains(out, "⛔ 存在高危问题") {
		t.Fatalf("minimal 结论先行缺失:\n%s", out)
	}
	if !strings.Contains(out, "### 高危意见") || !strings.Contains(out, "pid 复用串数据") {
		t.Fatalf("minimal 高危列表缺失:\n%s", out)
	}
	if strings.Contains(out, "魔法数") || strings.Contains(out, "需求符合性") || strings.Contains(out, "证据链") {
		t.Fatalf("minimal 不得含坏味/analyst 段:\n%s", out)
	}
	if strings.Contains(out, "meta") || !strings.Contains(out, "task `t1`") {
		t.Fatalf("minimal meta 行缺失:\n%s", out)
	}
}

func TestRenderMinimalHighCap(t *testing.T) {
	r := Report{TaskID: "t", Template: reporttmpl.Minimal, VerdictText: "✅ 通过", Meta: true}
	for i := 0; i < 15; i++ {
		r.Findings = append(r.Findings, plugin.Finding{File: "f.go", Line: i, Severity: "high", Comment: "h"})
	}
	out := Render(r)
	if !strings.Contains(out, "另有 5 条高危/严重意见从略") {
		t.Fatalf("minimal 封顶计数缺失:\n%s", out)
	}
	if strings.Count(out, "`f.go:`") > 10 {
		t.Fatalf("minimal 应封顶 10 条")
	}
}

func TestRenderStandardUnchangedByTemplateField(t *testing.T) {
	// Template=standard / 空 / 未知：与缺省行为一致（standard 渲染不读 Template）。
	base := Report{TaskID: "t", HeadSHA: "h", Version: "v", VerdictText: "✅ 通过", Meta: true,
		Findings: []plugin.Finding{{File: "a.go", Line: 1, Severity: "high", Comment: "x"}}}
	ref := Render(base)
	for _, tmpl := range []string{"", "standard", "bogus", "STANDARD"} {
		r := base
		r.Template = tmpl
		if got := Render(r); got != ref {
			t.Fatalf("Template=%q 与 standard 漂移", tmpl)
		}
	}
	if !strings.Contains(ref, "## 📋 Argus 需求复核报告") {
		t.Fatalf("standard 标题缺失:\n%s", ref)
	}
}

func TestClaimIconVocab(t *testing.T) {
	cases := map[string]string{
		plugin.ClaimImplemented:  "✅",
		plugin.ClaimPartial:      "⚠️",
		plugin.ClaimMissing:      "❌",
		plugin.ClaimUndeclared:   "➕",
		plugin.ClaimInconsistent: "❗",
	}
	for status, want := range cases {
		if got := claimIcon(status); got != want {
			t.Fatalf("claimIcon(%q) = %q, want %q", status, got, want)
		}
		if got := claimStatusLabel(status); got == "" || got == status {
			t.Fatalf("claimStatusLabel(%q) 未映射: %q", status, got)
		}
	}
	if got := claimIcon("bogus"); got != "❓" {
		t.Fatalf("未知状态兜底 ❓, got %q", got)
	}
}
