// runner_progressmarker_test.go —— v6.1.5 执行期 start marker 进度条纯函数单测：
// 已过 ✅ / 当前 ⏳ / 未到「未开始」三态、子阶段前缀归位、词表外保守降级与锚点行。
package runner

import (
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

// 中段阶段：R1/R2 已过 ✅，R3 当前 ⏳，R35/R4/R5 未到「未开始」。
func TestStageProgressBar_MidStage(t *testing.T) {
	got := stageProgressBar(plugin.LayerR3)
	if !strings.Contains(got, "R1 需求解析 ✅") {
		t.Fatalf("R1 应为已过 ✅: %q", got)
	}
	if !strings.Contains(got, "R2 变更理解 ✅") {
		t.Fatalf("R2 应为已过 ✅: %q", got)
	}
	if !strings.Contains(got, "R3 完成度判定 ⏳") {
		t.Fatalf("R3 应为当前 ⏳: %q", got)
	}
	for _, want := range []string{"R35 报告反思 未开始", "R4 专家插件 未开始", "R5 终审裁决 未开始"} {
		if !strings.Contains(got, want) {
			t.Fatalf("未到阶段应显式「未开始」（%s）: %q", want, got)
		}
	}
	// 全部阶段按 StageTimeline 规范序出现一次
	if n := strings.Count(got, " · "); n != len(plugin.StageTimeline)-1 {
		t.Fatalf("应列出全部 %d 个阶段: %q", len(plugin.StageTimeline), got)
	}
}

// 首阶段：无 ✅；末阶段：无「未开始」。
func TestStageProgressBar_FirstAndLast(t *testing.T) {
	first := stageProgressBar(plugin.LayerR1)
	if !strings.Contains(first, "R1 需求解析 ⏳") {
		t.Fatalf("R1 应为当前 ⏳: %q", first)
	}
	if strings.Contains(first, "✅") {
		t.Fatalf("首阶段不应有已过 ✅: %q", first)
	}
	last := stageProgressBar(plugin.LayerR5)
	if !strings.Contains(last, "R5 终审裁决 ⏳") {
		t.Fatalf("R5 应为当前 ⏳: %q", last)
	}
	if strings.Contains(last, "未开始") {
		t.Fatalf("末阶段不应有「未开始」: %q", last)
	}
	if !strings.Contains(last, "R4 专家插件 ✅") {
		t.Fatalf("R4 应为已过 ✅: %q", last)
	}
}

// 子阶段前缀回退（与 StageRouter 同语义）：R1a 归到 R1 槽位。
func TestStageProgressBar_SubStagePrefix(t *testing.T) {
	if got := stageSlot(plugin.LayerR1A); got != 0 {
		t.Fatalf("R1a 应前缀回退到 R1 槽位 0: %d", got)
	}
	got := stageProgressBar(plugin.LayerR1A)
	if !strings.Contains(got, "R1 需求解析 ⏳") {
		t.Fatalf("R1a 进度应显示在 R1: %q", got)
	}
}

// 词表外阶段（含空串）：保守全部「未开始」，不错报任何完成。
func TestStageProgressBar_UnknownStage(t *testing.T) {
	for _, cur := range []string{"merger", ""} {
		got := stageProgressBar(cur)
		if strings.Contains(got, "✅") || strings.Contains(got, "⏳") {
			t.Fatalf("词表外 %q 不应报任何进度: %q", cur, got)
		}
		if !strings.Contains(got, "未开始") {
			t.Fatalf("词表外应全「未开始」: %q", got)
		}
	}
}

// 完整 marker 文案：审查中 + 进度行 + 幂等锚点行，顺序固定（锚点收尾）。
func TestStartMarkerProgressBody(t *testing.T) {
	got := startMarkerProgressBody("t-42", plugin.LayerR2)
	head := "🔍 Argus 审查中…"
	if !strings.HasPrefix(got, head+"\n\n") {
		t.Fatalf("应以「%s」开头: %q", head, got)
	}
	if !strings.Contains(got, "进度：R1 需求解析 ✅ · R2 变更理解 ⏳") {
		t.Fatalf("应含进度行: %q", got)
	}
	anchor := "<!-- argus:start task_id=t-42 -->"
	if !strings.HasSuffix(got, anchor) {
		t.Fatalf("锚点行应收尾（幂等定位依赖）: %q", got)
	}
	// 顺序：审查中 < 进度 < 锚点
	if strings.Index(got, head) > strings.Index(got, "进度：") ||
		strings.Index(got, "进度：") > strings.Index(got, anchor) {
		t.Fatalf("段落顺序应为 审查中→进度→锚点: %q", got)
	}
}
