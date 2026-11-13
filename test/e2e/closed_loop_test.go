package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// v3.5 review 闭环 e2e：两轮任务验证 regret 学习环 + 证据门。
//
// 轮1：correctness 报 2 条；R3.5 保留「真问题」（带 evidence）、剔除「风格噪声」
//
//	→ 风格模式记 PatternRegret
//
// 轮2：correctness 再报同样 2 条；R3.5 两条都保留（带 evidence）
//
//	→ 风格条因历史置信度低被 Merger 标注
//
// 负控：真问题在两轮都不应被标注/丢弃。
func TestScenario17_RegretLearningLoop(t *testing.T) {
	// 真问题：保留并带 evidence
	// 风格噪声：轮1被 R3.5 剔除（不在 draft 输出），轮2保留并带 evidence
	const (
		noiseComment = "建议提取魔法数字为常量"
		realComment  = "存在空指针解引用风险"
		noiseFile    = "b.go"
		realFile     = "a.go"
	)
	correctness := fmt.Sprintf(`{"findings":[
		{"file":%q,"line":10,"severity":"high","comment":%q},
		{"file":%q,"line":3,"severity":"low","comment":%q}
	],"notes":""}`, realFile, realComment, noiseFile, noiseComment)

	// 轮1 draft：只保留真问题（id=0）
	draft1 := fmt.Sprintf(`[{"id":0,"file":%q,"line":10,"severity":"high","comment":%q,
		"evidence":"a.go 第 10 行 req 未判空即解引用","counterexample":"上游已保证非空则不成立，但本路径无校验"}]`,
		realFile, realComment)
	// 轮2 draft：两条都保留并带证据
	draft2 := fmt.Sprintf(`[
		{"id":0,"file":%q,"line":10,"severity":"high","comment":%q,
		 "evidence":"a.go 第 10 行 req 未判空即解引用","counterexample":"上游已保证非空则不成立"},
		{"id":1,"file":%q,"line":3,"severity":"low","comment":%q,
		 "evidence":"b.go 第 3 行数字 3 出现在条件中","counterexample":"若该数字有语义常量定义则为误报"}
	]`, realFile, realComment, noiseFile, noiseComment)

	s := newStack(t, stackOpts{
		correctness:     correctness,
		reflectRounds:   1,
		reflectEvidence: "strict",
		regretLearning:  true,
		r35Drafts:       []string{draft1, draft2},
	})

	ctx := context.Background()

	// ---- 轮1 ----
	t1 := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(ctx, t1); err != nil {
		t.Fatal(err)
	}
	task1 := s.waitForStatus(t, t1, "done")
	if !strings.Contains(task1.Report, realComment) {
		t.Fatalf("轮1 应保留真问题:\n%s", task1.Report)
	}
	if strings.Contains(task1.Report, noiseComment) {
		t.Fatalf("轮1 风格噪声应被 R3.5 剔除:\n%s", task1.Report)
	}
	// regret 落库
	pNoise, err := s.store.GetPattern(ctx, "gitea", "acme", "app", "correctness", noiseComment)
	if err != nil || pNoise == nil || pNoise.Regret < 1 {
		t.Fatalf("轮1 应记风格模式 regret: p=%+v err=%v", pNoise, err)
	}
	if pNoise.Confidence >= 0.50 {
		t.Fatalf("轮1 后风格模式置信度应低于 soft floor: %v", pNoise.Confidence)
	}
	// 真问题无 regret
	pReal, err := s.store.GetPattern(ctx, "gitea", "acme", "app", "correctness", realComment)
	if err != nil || pReal == nil {
		t.Fatalf("真问题应有 pattern 记录: %+v err=%v", pReal, err)
	}
	if pReal.Regret != 0 {
		t.Fatalf("真问题不应有 regret: %+v", pReal)
	}

	// ---- 轮2：风格条重新发表 → 低置信标注 ----
	t2 := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(ctx, t2); err != nil {
		t.Fatal(err)
	}
	task2 := s.waitForStatus(t, t2, "done")
	if !strings.Contains(task2.Report, noiseComment) {
		t.Fatalf("轮2 风格条应重新发表:\n%s", task2.Report)
	}
	if !strings.Contains(task2.Report, "历史置信度低") {
		t.Fatalf("轮2 应标注低置信:\n%s", task2.Report)
	}
	if !strings.Contains(task2.Report, realComment) {
		t.Fatalf("轮2 真问题应保留:\n%s", task2.Report)
	}
	// 负控：真问题不得被标注
	if strings.Contains(task2.Report, realComment+"（本仓库同类意见历史置信度低）") {
		t.Fatalf("真问题不得被低置信标注:\n%s", task2.Report)
	}
}

// v3.5：人工 suppress 既有指纹后，同模式在下一轮被 HardFloor 丢弃（low 条目）。
// 直接用存储 API 预热模式信号，验证 Merger 闸门端到端生效。
func TestScenario18_LearningHardFloorDropsLow(t *testing.T) {
	const comment = "建议提取魔法数字为常量"
	s := newStack(t, stackOpts{
		correctness: fmt.Sprintf(
			`{"findings":[{"file":"b.go","line":3,"severity":"low","comment":%q}],"notes":""}`, comment),
		regretLearning: true,
	})
	ctx := context.Background()

	// 预热：suppress + regret，把置信度压到 HardFloor 以下
	// Confidence(1 posted, 2 regret, 1 suppress, 0) = 3/(3+4+3)=0.30 —— 需再压
	// Confidence(1, 3, 1) = 3/(3+6+3)=0.25 < 0.30
	f := domain.Finding{File: "b.go", Line: 3, Severity: "low", Comment: comment}
	for range 3 {
		if err := s.store.PatternRegret(ctx, "gitea", "acme", "app", "correctness", f, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.PatternSuppress(ctx, "gitea", "acme", "app", "correctness", f, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 再 posted 一次凑 samples
	if err := s.store.PatternPosted(ctx, "gitea", "acme", "app", "correctness", f, time.Now()); err != nil {
		t.Fatal(err)
	}
	p, err := s.store.GetPattern(ctx, "gitea", "acme", "app", "correctness", comment)
	if err != nil || p == nil {
		t.Fatalf("预热失败: %+v err=%v", p, err)
	}
	if p.Confidence >= 0.30 {
		t.Fatalf("预热后置信度应低于 hard floor 0.30: conf=%v samples=%d", p.Confidence, p.Samples)
	}

	tid := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(ctx, tid); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, tid, "done")
	// 丢弃后不得出现在「代码审查意见」列表（行格式 `- \`b.go:3\``）；
	// Degraded 注明里可能带模式样本文本，故不按 comment 全文断言。
	if strings.Contains(task.Report, "`b.go:3`") {
		t.Fatalf("low×低置信应被学习环丢弃:\n%s", task.Report)
	}
	if !strings.Contains(task.Report, "学习环抑制") {
		t.Fatalf("报告应注明学习环抑制:\n%s", task.Report)
	}
}

// v3.5：strict 证据门 —— 保留条缺 evidence 时直接剔除，报告不得出现。
func TestScenario19_EvidenceGateStrictDrops(t *testing.T) {
	const comment = "存在 SQL 拼接风险"
	s := newStack(t, stackOpts{
		correctness: fmt.Sprintf(
			`{"findings":[{"file":"a.go","line":7,"severity":"high","comment":%q}],"notes":""}`, comment),
		reflectRounds:   1,
		reflectEvidence: "strict",
		// draft 保留该条但不给 evidence → 后验门剔除
		r35Drafts: []string{fmt.Sprintf(
			`[{"id":0,"file":"a.go","line":7,"severity":"high","comment":%q}]`, comment)},
	})
	tid := s.createTaskViaAPI(t, 0)
	if err := s.runner.Execute(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	task := s.waitForStatus(t, tid, "done")
	if strings.Contains(task.Report, comment) {
		t.Fatalf("缺 evidence 的保留条应被 strict 门剔除:\n%s", task.Report)
	}
	if !strings.Contains(task.Report, "证据门") {
		t.Fatalf("报告应注明证据门动作:\n%s", task.Report)
	}
}
