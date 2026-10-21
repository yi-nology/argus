package dispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"

	"argus/internal/plugin"
)

// ---- aggregateSamples ----

func TestAggregateSamples_ReplicationAndBump(t *testing.T) {
	// 3 采样：2 份报同一 medium 问题（同 file:line 同评语），1 份报另一 low 问题
	same := Finding{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "错误处理缺失"}
	results := []Result{
		{Findings: []Finding{same, {File: "b.go", Line: 1, Severity: SeverityLow, Comment: "命名风格"}}},
		{Findings: []Finding{same}},
		{Findings: []Finding{same}},
	}
	agg := aggregateSamples(3, 3, results)
	if len(agg.Findings) != 2 {
		t.Fatalf("应聚合为 2 条，got %d", len(agg.Findings))
	}
	first := agg.Findings[0]
	if first.File != "a.go" {
		t.Fatalf("首条应保持采样序 a.go，got %s", first.File)
	}
	if first.Severity != SeverityHigh {
		t.Fatalf("复现 3/3 应升一级 medium→high，got %s", first.Severity)
	}
	if first.Samples != "3/3" {
		t.Fatalf("Samples=3/3，got %q", first.Samples)
	}
	if Fingerprint(first.File, first.Comment, first.Line) == "" {
		t.Fatal("指纹可计算（评语未被标注污染）")
	}
	second := agg.Findings[1]
	if second.Samples != "1/3" || second.Severity != SeverityLow {
		t.Fatalf("单次 low 应保留并标注 1/3，got sev=%s samples=%q", second.Severity, second.Samples)
	}
}

func TestAggregateSamples_WordDriftMerges(t *testing.T) {
	// 措辞漂移（规范化后同评语、行号相容）→ 同题合并计数；不相容行号独立成条
	results := []Result{
		{Findings: []Finding{{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "Error handling missing."}}},
		{Findings: []Finding{{File: "a.go", Line: 4, Severity: SeverityMedium, Comment: "error handling missing"}}},
		{Findings: []Finding{{File: "a.go", Line: 30, Severity: SeverityLow, Comment: "error handling missing"}}},
	}
	agg := aggregateSamples(3, 3, results)
	if len(agg.Findings) != 2 {
		t.Fatalf("漂移措辞应并为一组 + 独立行号一条，got %d", len(agg.Findings))
	}
	if agg.Findings[0].Samples != "2/3" {
		t.Fatalf("复现 2/3，got %q", agg.Findings[0].Samples)
	}
	if agg.Findings[1].Samples != "1/3" {
		t.Fatalf("不相容行号独立计数 1/3，got %q", agg.Findings[1].Samples)
	}
}

func TestAggregateSamples_PartialFailureNote(t *testing.T) {
	results := []Result{
		{Findings: []Finding{{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "x"}}},
		{Findings: []Finding{{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "x"}}},
	}
	agg := aggregateSamples(3, 2, results)
	if !strings.Contains(agg.Notes, DegradationMarker) || !strings.Contains(agg.Notes, "2/3") {
		t.Fatalf("部分成功应有降级注记，notes=%q", agg.Notes)
	}
	if agg.Findings[0].Samples != "2/3" {
		t.Fatalf("复现 2/3，got %q", agg.Findings[0].Samples)
	}
}

func TestClampReviewN(t *testing.T) {
	cases := map[int]int{-1: 1, 0: 1, 1: 1, 2: 2, 5: 5, 6: MaxReviewN, 99: MaxReviewN}
	for in, want := range cases {
		if got := ClampReviewN(in); got != want {
			t.Fatalf("ClampReviewN(%d)=%d，want %d", in, got, want)
		}
	}
}

func TestBumpSeverity(t *testing.T) {
	if BumpSeverity(SeverityLow) != SeverityMedium || BumpSeverity(SeverityMedium) != SeverityHigh ||
		BumpSeverity(SeverityHigh) != SeverityHigh {
		t.Fatal("升级链 low→medium→high→high 不符")
	}
}

func TestSamplesAnnotation(t *testing.T) {
	if ann := SamplesAnnotation(Finding{Samples: "2/3"}); !strings.Contains(ann, "2/3") {
		t.Fatalf("复现标注错误: %q", ann)
	}
	if ann := SamplesAnnotation(Finding{Samples: "1/3"}); !strings.Contains(ann, "单次") {
		t.Fatalf("单次标注错误: %q", ann)
	}
	if ann := SamplesAnnotation(Finding{}); ann != "" {
		t.Fatalf("无采样应空标注，got %q", ann)
	}
	if ann := SamplesAnnotation(Finding{Samples: "bad"}); ann != "" {
		t.Fatalf("非法格式应空标注，got %q", ann)
	}
}

// ---- Dispatcher.Run best-of-N ----

// seqPlugin 按调用序返回预设结果（并发采样用原子序；failOn 命中的轮次返回错误）。
type seqPlugin struct {
	manifest AgentManifest
	mu       sync.Mutex
	calls    int
	outputs  []Result
	failOn   map[int]bool
}

func (p *seqPlugin) Manifest() AgentManifest { return p.manifest }
func (p *seqPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	fail := p.failOn[i]
	var out Result
	if len(p.outputs) > 0 {
		out = p.outputs[i%len(p.outputs)]
	}
	p.mu.Unlock()
	if fail {
		return Result{}, errors.New("sample boom")
	}
	return out, nil
}

func sampleReg(name string, n int, p AgentPlugin) *Registration {
	return &Registration{
		Manifest: AgentManifest{Name: name, Skills: []string{plugin.SkillCodeReview}, ReviewN: n, Timeout: 5 * time.Second},
		Plugin:   p,
		Enabled:  true,
	}
}

func TestDispatcher_RunBestOfN_Aggregated(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	finding := Finding{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "同题发现"}
	p := &seqPlugin{manifest: AgentManifest{Name: "expert", ReviewN: 3}, outputs: []Result{
		{Findings: []Finding{finding}},
		{Findings: []Finding{finding}},
		{Findings: []Finding{}},
	}}
	_ = d.reg.Register(sampleReg("expert", 3, p))

	sel := d.Select(&PRContext{Platform: "gitea"}, nil, []string{plugin.SkillCodeReview}, nil)
	outcomes := d.Run(context.Background(), Task{}, sel, nil)
	if len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].Result == nil {
		t.Fatalf("应单 Outcome 成功: %+v", outcomes)
	}
	fs := outcomes[0].Result.Findings
	if len(fs) != 1 || fs[0].Samples != "2/3" {
		t.Fatalf("复现计数 2/3: %+v", fs)
	}
	// 熔断配对：3 次采样共享一次 Allow→Success，熔断仍健康
	if _, _, berr := d.BreakerFor("expert").AllowGen(context.Background()); berr != nil {
		t.Fatal("全成功不应影响熔断健康")
	}
}

func TestDispatcher_RunBestOfN_PartialFailure(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	finding := Finding{File: "a.go", Line: 3, Severity: SeverityMedium, Comment: "同题发现"}
	p := &seqPlugin{manifest: AgentManifest{Name: "expert", ReviewN: 3}, failOn: map[int]bool{1: true},
		outputs: []Result{{Findings: []Finding{finding}}, {Findings: []Finding{finding}}, {Findings: []Finding{finding}}}}
	_ = d.reg.Register(sampleReg("expert", 3, p))

	sel := d.Select(&PRContext{Platform: "gitea"}, nil, []string{plugin.SkillCodeReview}, nil)
	outcomes := d.Run(context.Background(), Task{}, sel, nil)
	o := outcomes[0]
	if o.Err != nil || o.Result == nil {
		t.Fatalf("部分成功应照常产结果: %+v", o)
	}
	if !strings.Contains(o.Result.Notes, "best-of-N") || !strings.Contains(o.Result.Notes, "2/3") {
		t.Fatalf("部分成功应有降级注记: %q", o.Result.Notes)
	}
	if _, _, berr := d.BreakerFor("expert").AllowGen(context.Background()); berr != nil {
		t.Fatal("部分失败（有成功采样）不应计熔断失败")
	}
}

func TestDispatcher_RunBestOfN_AllFail(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	p := &seqPlugin{manifest: AgentManifest{Name: "expert", ReviewN: 2},
		failOn: map[int]bool{0: true, 1: true}}
	_ = d.reg.Register(sampleReg("expert", 2, p))

	sel := d.Select(&PRContext{Platform: "gitea"}, nil, []string{plugin.SkillCodeReview}, nil)
	outcomes := d.Run(context.Background(), Task{}, sel, nil)
	if outcomes[0].Err == nil || outcomes[0].Result != nil {
		t.Fatalf("全失败应透出错误: %+v", outcomes[0])
	}
	if !strings.Contains(outcomes[0].Notes(), "expert 未参与") {
		t.Fatalf("应有未参与注明: %q", outcomes[0].Notes())
	}
}

func TestDispatcher_RunBestOfN_OffByDefault(t *testing.T) {
	// ReviewN=0/1：单次执行语义不变（只 invoke 一次）
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	p := &seqPlugin{manifest: AgentManifest{Name: "expert"}, outputs: []Result{{Findings: []Finding{
		{File: "a.go", Line: 3, Severity: SeverityLow, Comment: "x"},
	}}}}
	_ = d.reg.Register(sampleReg("expert", 1, p))

	sel := d.Select(&PRContext{Platform: "gitea"}, nil, []string{plugin.SkillCodeReview}, nil)
	outcomes := d.Run(context.Background(), Task{}, sel, nil)
	fs := outcomes[0].Result.Findings
	if len(fs) != 1 || fs[0].Samples != "" {
		t.Fatalf("reviewN=1 不应有采样标注: %+v", fs)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 1 {
		t.Fatalf("reviewN=1 应只调用一次，got %d", p.calls)
	}
}

func TestConfidenceAnnotation(t *testing.T) {
	if ann := ConfidenceAnnotation(Finding{Confidence: 0}); ann != "" {
		t.Fatalf("未提供信心应无标注: %q", ann)
	}
	if ann := ConfidenceAnnotation(Finding{Confidence: 0.9}); ann != "" {
		t.Fatalf("高信心应无标注: %q", ann)
	}
	ann := ConfidenceAnnotation(Finding{Confidence: 0.4})
	if ann == "" || !strings.Contains(ann, "40%") {
		t.Fatalf("低信心应有百分比标注: %q", ann)
	}
}
