package chaos_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/chaos"
)

// TestScriptedInjectorFailFirst 剧本注入器：前 N 次注入、之后放行，调用计数
// 与裁决一一对应（Calls = 实际被询问次数，重试断言的口径单源）。
func TestScriptedInjectorFailFirst(t *testing.T) {
	inj := chaos.NewScriptedInjector()
	inj.Script("llm.review", chaos.Script{Fault: chaos.ErrDeadline, FailFirst: 2})
	op := chaos.Op{Family: chaos.FamilyLLM, Kind: chaos.LLMTimeout, Target: "llm.review"}
	ctx := context.Background()

	for i := range 5 {
		err := inj.Fault(ctx, op)
		if i < 2 && !errors.Is(err, chaos.ErrDeadline) {
			t.Fatalf("第 %d 次应注入 ErrDeadline，got %v", i+1, err)
		}
		if i >= 2 && err != nil {
			t.Fatalf("第 %d 次应放行，got %v", i+1, err)
		}
	}
	if got := inj.Calls("llm.review"); got != 5 {
		t.Fatalf("Calls = %d, want 5", got)
	}
	if got := inj.Calls("git.api"); got != 0 {
		t.Fatalf("未装配 target 的计数应为 0，got %d", got)
	}
}

// TestRetryTransientBounded 瞬态故障：有界重试，恢复后成功；重试次数有上界。
func TestRetryTransientBounded(t *testing.T) {
	inj := chaos.NewScriptedInjector()
	inj.Script("git.api", chaos.Script{Fault: chaos.ErrRateLimited, FailFirst: 2})
	op := chaos.Op{Family: chaos.FamilyRateLimit, Kind: chaos.GitRateLimited, Target: "git.api"}
	calls := 0
	err := chaos.Retry(context.Background(), inj, op, 5, 0, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("恢复后应成功: %v", err)
	}
	// 口径：每次尝试恰好询问注入器一次（inj=3），fn 只在放行时执行（calls=1）。
	if calls != 1 || inj.Calls("git.api") != 3 {
		t.Fatalf("calls=%d inj=%d, want 1/3", calls, inj.Calls("git.api"))
	}
}

// TestRetryExhausted 瞬态故障持续：重试至上界后返回末次错误。
func TestRetryExhausted(t *testing.T) {
	inj := chaos.NewScriptedInjector()
	inj.Script("llm.review", chaos.Script{Fault: chaos.ErrUnreachable, Always: true})
	op := chaos.Op{Family: chaos.FamilyNetwork, Kind: chaos.NetLLMUnreachable, Target: "llm.review"}
	calls := 0
	err := chaos.Retry(context.Background(), inj, op, 3, 0, func(context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, chaos.ErrUnreachable) {
		t.Fatalf("耗尽后应返回 ErrUnreachable: %v", err)
	}
	if calls != 0 || inj.Calls("llm.review") != 3 {
		t.Fatalf("calls=%d inj=%d, want 0/3（耗尽前业务体只被放行才执行）", calls, inj.Calls("llm.review"))
	}
}

// TestRetryFailFastOnPermanent 非瞬态故障（进程死亡/租约失守/空输出）：立即返回，
// 不重试（重试无意义或有害的语义契约）。
func TestRetryFailFastOnPermanent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"kill", chaos.ErrKilled},
		{"lease", chaos.ErrLeaseLost},
		{"empty", chaos.ErrEmptyOutput},
		{"malformed", chaos.ErrMalformedOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inj := chaos.NewScriptedInjector()
			inj.Script("x", chaos.Script{Fault: tc.err, FailFirst: 10})
			op := chaos.Op{Target: "x"}
			err := chaos.Retry(context.Background(), inj, op, 5, 0, func(context.Context) error {
				return nil
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("应 fail-fast 返回 %v: %v", tc.err, err)
			}
			if inj.Calls("x") != 1 {
				t.Fatalf("非瞬态应只尝试 1 次，got %d", inj.Calls("x"))
			}
		})
	}
}

// TestIsTransientClassification 瞬态分类契约：网络/限流/超时为瞬态，
// 死亡/租约/空输出/非结构化为非瞬态。
func TestIsTransientClassification(t *testing.T) {
	transient := []error{chaos.ErrUnreachable, chaos.ErrRateLimited, chaos.ErrDeadline, context.DeadlineExceeded}
	permanent := []error{chaos.ErrKilled, chaos.ErrLeaseLost, chaos.ErrEmptyOutput, chaos.ErrMalformedOutput}
	for _, err := range transient {
		if !chaos.IsTransient(err) {
			t.Errorf("%v 应为瞬态", err)
		}
	}
	for _, err := range permanent {
		if chaos.IsTransient(err) {
			t.Errorf("%v 应为非瞬态", err)
		}
	}
}

// TestMatrixCoverageHelpers 覆盖检查：缺格/越格显式暴露。
func TestMatrixCoverageHelpers(t *testing.T) {
	full := make([]chaos.Scenario, 0, 16)
	for _, c := range chaos.Matrix() {
		full = append(full, chaos.Scenario{Cell: c})
	}
	if missing := chaos.MissingCells(full); len(missing) != 0 {
		t.Fatalf("全量对位不应缺格: %+v", missing)
	}
	if extra := chaos.ExtraCells(full); len(extra) != 0 {
		t.Fatalf("全量对位不应越格: %+v", extra)
	}
	if missing := chaos.MissingCells(nil); len(missing) != len(chaos.Matrix()) {
		t.Fatalf("空场景应全缺: %d", len(missing))
	}
	orphan := chaos.Cell{Family: "nope", Kind: "ghost", Point: "x"}
	if extra := chaos.ExtraCells([]chaos.Scenario{{Cell: orphan}}); len(extra) != 1 {
		t.Fatalf("孤儿格应被暴露: %+v", extra)
	}
}

// TestReportRender 报告双形态：JSON 可解析回读、Markdown 含矩阵表头与逐格行。
func TestReportRender(t *testing.T) {
	scenarios := []chaos.Scenario{
		{Cell: chaos.Matrix()[0], Probe: func(context.Context) error { return nil }},
		{Cell: chaos.Matrix()[1], Probe: func(context.Context) error { return errors.New("恢复语义不成立") }},
	}
	rep := chaos.Run(context.Background(), scenarios)
	if rep.Pass() {
		t.Fatal("含失败格的报告不应 Pass")
	}
	if rep.Total != 2 || rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("Total/Passed/Failed = %d/%d/%d, want 2/1/1", rep.Total, rep.Passed, rep.Failed)
	}

	var jsonBuf bytes.Buffer
	if err := rep.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonBuf.String(), `"status": "fail"`) {
		t.Fatal("JSON 报告应含失败格状态")
	}

	var mdBuf bytes.Buffer
	if err := rep.WriteMarkdown(&mdBuf); err != nil {
		t.Fatal(err)
	}
	md := mdBuf.String()
	for _, want := range []string{"# 混沌测试矩阵报告", "| 故障族 |", "恢复语义不成立"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺 %q", want)
		}
	}
}

// TestRunNilProbe 缺 probe 的格按失败记录（不 panic 不短路）。
func TestRunNilProbe(t *testing.T) {
	rep := chaos.Run(context.Background(), []chaos.Scenario{{Cell: chaos.Matrix()[0]}})
	if rep.Pass() || rep.Cells[0].Status != chaos.StatusFail {
		t.Fatalf("缺 probe 应记失败: %+v", rep.Cells)
	}
	if !strings.Contains(rep.Cells[0].Error, "probe 未装配") {
		t.Fatalf("错误信息应说明缺 probe: %q", rep.Cells[0].Error)
	}
}

// TestRunProbePanicContained probe panic 被单格捕获（混沌用例自身不许炸矩阵）。
func TestRunProbePanicContained(t *testing.T) {
	rep := chaos.Run(context.Background(), []chaos.Scenario{
		{Cell: chaos.Matrix()[0], Probe: func(context.Context) error { panic("boom") }},
	})
	if rep.Pass() || !strings.Contains(rep.Cells[0].Error, "panic") {
		t.Fatalf("panic 应被捕获记失败: %+v", rep.Cells)
	}
}

// TestKindFamilyMapping Kind→Family 归族完备（新增 Kind 忘归族会在这里炸）。
func TestKindFamilyMapping(t *testing.T) {
	for _, c := range chaos.Matrix() {
		if c.Kind.Family() != c.Family {
			t.Errorf("Kind %s 归族 = %q, want %q", c.Kind, c.Kind.Family(), c.Family)
		}
	}
}

// TestBackoffZero 退避基步长为 0 时等待为 0（测试确定性）。
func TestBackoffZero(t *testing.T) {
	inj := chaos.NewScriptedInjector()
	inj.Script("x", chaos.Script{Fault: chaos.ErrDeadline, FailFirst: 2})
	start := time.Now()
	_ = chaos.Retry(context.Background(), inj, chaos.Op{Target: "x"}, 3, 0, func(context.Context) error { return nil })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("零退避不应真等待: %v", d)
	}
}
