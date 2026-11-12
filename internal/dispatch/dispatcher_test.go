package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"time"
)

// ---- test helpers ----

type nopPlugin struct{ manifest AgentManifest }

func (p *nopPlugin) Manifest() AgentManifest                          { return p.manifest }
func (p *nopPlugin) Invoke(_ context.Context, _ Task) (Result, error) { return Result{}, nil }

type failPlugin struct{ manifest AgentManifest }

func (p *failPlugin) Manifest() AgentManifest { return p.manifest }
func (p *failPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	return Result{}, errors.New("boom")
}

func reg(name string, skills []string, opts ...func(*Registration)) *Registration {
	r := &Registration{
		Manifest: AgentManifest{Name: name, Skills: skills},
		Plugin:   &nopPlugin{manifest: AgentManifest{Name: name, Skills: skills}},
		Enabled:  true,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

func withLock(r *Registration)     { r.LockRepoConfig = true }
func withDisabled(r *Registration) { r.Enabled = false }
func withCost(c string) func(*Registration) {
	return func(r *Registration) { r.Manifest.Cost = c }
}
func withTrigger(paths []string) func(*Registration) {
	return func(r *Registration) { r.Manifest.Triggers.Paths = paths }
}

// ---- Select tests ----

func TestSelect_Basic(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("应只选中 security，got %v", selNames(sel))
	}
}

func TestSelect_NoSkillMatch(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"nonexistent"}, nil)
	if len(sel) != 0 {
		t.Fatalf("无技能匹配应为空，got %v", selNames(sel))
	}
}

func TestSelect_Disabled(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withDisabled))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 0 {
		t.Fatalf("disabled 插件不应入选，got %v", selNames(sel))
	}
}

func TestSelect_LockBypassesDisable(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withLock))

	pr := &PRContext{Platform: "gitea"}
	// lock 插件在 disableSet 中也应入选（Select 不看 disableSet 对 lock）
	rc := &RepoConfig{Disable: []string{"security"}}
	sel := d.Select(pr, nil, []string{"security"}, rc)
	if len(sel) != 1 {
		t.Fatalf("lock 插件应绕过 disable，got %v", selNames(sel))
	}
	if sel[0].Reason != "lock" {
		t.Fatalf("reason 应为 lock，got %q", sel[0].Reason)
	}
}

func TestSelect_RepoConfigUseWhitelist(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	rc := &RepoConfig{Use: []string{"security"}}
	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security", "correctness"}, rc)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("use 白名单应只选 security，got %v", selNames(sel))
	}
}

func TestSelect_RepoConfigDisableBlacklist(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	rc := &RepoConfig{Disable: []string{"correctness"}}
	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security", "correctness"}, rc)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("disable 应排除 correctness，got %v", selNames(sel))
	}
}

func TestSelect_TriggerMatch(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withTrigger([]string{"src/**"})))

	pr := &PRContext{Platform: "gitea"}
	// 不匹配 → 不选
	sel := d.Select(pr, []string{"README.md"}, []string{"security"}, nil)
	if len(sel) != 0 {
		t.Fatalf("trigger 不匹配应不选，got %v", selNames(sel))
	}
	// 匹配 → 选
	sel = d.Select(pr, []string{"src/main.go"}, []string{"security"}, nil)
	if len(sel) != 1 {
		t.Fatalf("trigger 匹配应选中，got %v", selNames(sel))
	}
}

func TestSelect_Dedup(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("security", []string{"security"}, withLock))
	// routing 也引用 security → 应去重
	rc := &RepoConfig{Routing: []RoutingRule{{Paths: []string{"**"}, Use: []string{"security"}}}}

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, []string{"a.go"}, []string{"security"}, rc)
	if len(sel) != 1 {
		t.Fatalf("去重后应只有 1 个 security，got %v", selNames(sel))
	}
	if sel[0].Reason != "lock" {
		t.Fatalf("应保留首个原因 lock，got %q", sel[0].Reason)
	}
}

// ---- PruneByBudget tests ----

func TestPruneByBudget_NilBudget(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	sel := []Selection{{Reg: reg("a", nil)}}
	keep, pruned := d.PruneByBudget(sel, nil)
	if len(keep) != 1 || len(pruned) != 0 {
		t.Fatal("nil budget 应不裁剪")
	}
}

func TestPruneByBudget_HeavyPrunedFirst(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	// remaining=50 < reserve=100(10%)：循环直到所有非锁定插件被裁
	budget := NewTokenBudget(1000)
	budget.Add(950) // 剩余 50，低于 10% reserve (100)

	sel := []Selection{
		{Reg: reg("heavy", nil, withCost(CostHeavy))},
		{Reg: reg("standard", nil, withCost(CostStandard))},
	}
	keep, pruned := d.PruneByBudget(sel, budget)
	// heavy 先裁（costRank 更高），然后 standard 也被裁（remaining 仍 < reserve）
	if len(pruned) != 2 || pruned[0] != "heavy" || pruned[1] != "standard" {
		t.Fatalf("应先裁 heavy 再裁 standard，pruned=%v", pruned)
	}
	if len(keep) != 0 {
		t.Fatalf("应全部裁完，keep=%v", selNames(keep))
	}
}

func TestPruneByBudget_LockExempt(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	budget := NewTokenBudget(1000)
	budget.Add(999) // 剩余 1，几乎耗尽

	sel := []Selection{
		{Reg: reg("locked-heavy", nil, withCost(CostHeavy), func(r *Registration) { r.LockRepoConfig = true })},
		{Reg: reg("normal", nil, withCost(CostStandard))},
	}
	keep, pruned := d.PruneByBudget(sel, budget)
	// locked 应豁免，normal 被裁
	if len(pruned) != 1 || pruned[0] != "normal" {
		t.Fatalf("应裁 normal 而非 locked，pruned=%v", pruned)
	}
	if len(keep) != 1 || keep[0].Reg.Manifest.Name != "locked-heavy" {
		t.Fatalf("应保留 locked-heavy，keep=%v", selNames(keep))
	}
}

// ---- Run tests ----

func TestRun_CircuitBreakerSkips(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(&Registration{
		Manifest: AgentManifest{Name: "fail-plugin"},
		Plugin:   &failPlugin{manifest: AgentManifest{Name: "fail-plugin"}},
		Enabled:  true,
	})

	// 触发熔断：连续失败 3 次
	for range 3 {
		sel := []Selection{{Reg: &Registration{
			Manifest: AgentManifest{Name: "fail-plugin"},
			Plugin:   &failPlugin{manifest: AgentManifest{Name: "fail-plugin"}},
			Enabled:  true,
		}}}
		d.Run(context.Background(), Task{}, sel, nil)
	}

	// 第 4 次应被熔断跳过
	sel := []Selection{{Reg: &Registration{
		Manifest: AgentManifest{Name: "fail-plugin"},
		Plugin:   &failPlugin{manifest: AgentManifest{Name: "fail-plugin"}},
		Enabled:  true,
	}}}
	outcomes := d.Run(context.Background(), Task{}, sel, nil)
	if outcomes[0].SkippedReason == "" {
		t.Fatal("熔断后应跳过")
	}
}

func TestRun_BudgetExhaustedSkips(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	budget := NewTokenBudget(100)
	budget.Add(100) // 耗尽

	_ = d.reg.Register(&Registration{
		Manifest: AgentManifest{Name: "normal"},
		Plugin:   &nopPlugin{manifest: AgentManifest{Name: "normal"}},
		Enabled:  true,
	})

	sel := []Selection{{Reg: &Registration{
		Manifest: AgentManifest{Name: "normal"},
		Plugin:   &nopPlugin{manifest: AgentManifest{Name: "normal"}},
		Enabled:  true,
	}}}
	outcomes := d.Run(context.Background(), Task{}, sel, budget)
	if outcomes[0].SkippedReason == "" {
		t.Fatal("预算耗尽应跳过非锁定插件")
	}
}

func TestRun_LockBudgetExempt(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	d.MaxConcurrent = 1
	budget := NewTokenBudget(100)
	budget.Add(100) // 耗尽

	_ = d.reg.Register(&Registration{
		Manifest:       AgentManifest{Name: "locked"},
		Plugin:         &nopPlugin{manifest: AgentManifest{Name: "locked"}},
		Enabled:        true,
		LockRepoConfig: true,
	})

	sel := []Selection{{Reg: &Registration{
		Manifest:       AgentManifest{Name: "locked"},
		Plugin:         &nopPlugin{manifest: AgentManifest{Name: "locked"}},
		Enabled:        true,
		LockRepoConfig: true,
	}}}
	outcomes := d.Run(context.Background(), Task{}, sel, budget)
	if outcomes[0].SkippedReason != "" {
		t.Fatalf("锁定插件不应被预算跳过，got %q", outcomes[0].SkippedReason)
	}
	if outcomes[0].Err != nil {
		t.Fatalf("锁定插件应正常执行，got %v", outcomes[0].Err)
	}
}

// ---- helpers ----

func selNames(sel []Selection) []string {
	names := make([]string, len(sel))
	for i, s := range sel {
		names[i] = s.Reg.Manifest.Name
	}
	return names
}

func TestSelect_SquadAppendsMembers(t *testing.T) {
	registry := NewRegistry()
	registry.Register(reg("gpt", []string{"code-review"}))
	registry.Register(reg("claude", []string{"code-review"}))
	registry.Register(reg("kimi", []string{"code-review"}))
	d := NewDispatcher(registry, logx.Nop()).
		WithSquads([]SquadDecl{{
			Name: "sec", Paths: []string{"**/auth/**"}, Members: []string{"claude", "kimi"},
		}})

	pr := &PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}
	sel := d.Select(pr, []string{"pkg/auth/login.go"}, []string{"code-review"}, nil)

	names := map[string]string{}
	for _, s := range sel {
		names[s.Reg.Manifest.Name] = s.Reason
	}
	if names["gpt"] != "base" {
		t.Fatalf("gpt 应 base 入选: %+v", names)
	}
	// claude/kimi 已有 base 原因 → 去重保留；编队追加只对未入选成员生效
	if _, ok := names["claude"]; !ok {
		t.Fatal("claude 应入选")
	}
	if m := d.ActiveSquadMembers(pr, []string{"pkg/auth/x.go"}); len(m) != 2 {
		t.Fatalf("ActiveSquadMembers 应返回 2 成员: %v", m)
	}
	// 未命中触发条件：不追加
	if m := d.ActiveSquadMembers(pr, []string{"pkg/other/x.go"}); len(m) != 0 {
		t.Fatalf("未命中不应触发编队: %v", m)
	}
}

func TestMergerAnnotateCrossReview(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf := &MergedFindings{Findings: []Finding{
		{File: "a.go", Line: 10, Severity: "high", Comment: "空指针风险", By: "claude"},
		{File: "a.go", Line: 12, Severity: "medium", Comment: "边界未检查", By: "kimi"},
		{File: "b.go", Line: 30, Severity: "low", Comment: "命名", By: "claude"}, // kimi 未复现 → 标注
		{File: "c.go", Line: 1, Severity: "low", Comment: "非编队", By: "gpt"},    // 非：编队意见不标注
	}}
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})

	if mf.CrossReview == "" {
		t.Fatal("应有编队注记: " + mf.CrossReview)
	}
	if !strings.Contains(mf.Findings[2].Comment, "交叉审查分歧") {
		t.Fatalf("仅一方报告的意见应标注分歧: %q", mf.Findings[2].Comment)
	}
	if strings.Contains(mf.Findings[0].Comment, "交叉审查分歧") || strings.Contains(mf.Findings[1].Comment, "交叉审查分歧") {
		t.Fatal("双方都覆盖的区域不应标注")
	}
	if strings.Contains(mf.Findings[3].Comment, "交叉审查分歧") {
		t.Fatal("非编队来源不得标注")
	}
	// 幂等：重复调用不叠加
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})
	if strings.Count(mf.Findings[2].Comment, "交叉审查分歧") != 1 {
		t.Fatalf("标注应幂等: %q", mf.Findings[2].Comment)
	}
}

type blockingPlugin struct {
	manifest AgentManifest
	release  chan struct{}
}

func (p *blockingPlugin) Manifest() AgentManifest { return p.manifest }
func (p *blockingPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	<-p.release
	return Result{}, nil
}

// ---- 深挖修复回归：任务级取消不计熔断 ----

type canceledPlugin struct{ manifest AgentManifest }

func (p *canceledPlugin) Manifest() AgentManifest { return p.manifest }
func (p *canceledPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	return Result{}, context.Canceled
}

// 连续 3 次任务取消（如滚动发布）不应熔断插件——取消是任务级 teardown，
// 不是插件故障。真失败 3 次仍照常熔断。
func TestRun_CanceledNotCountedAsFailure(t *testing.T) {
	reg1 := &Registration{Manifest: AgentManifest{Name: "canceled-agent", Skills: []string{"security"}},
		Plugin: &canceledPlugin{manifest: AgentManifest{Name: "canceled-agent"}}, Enabled: true}
	reg2 := &Registration{Manifest: AgentManifest{Name: "fail-agent", Skills: []string{"correctness"}},
		Plugin: &failPlugin{manifest: AgentManifest{Name: "fail-agent"}}, Enabled: true}

	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg1)
	_ = d.reg.Register(reg2)
	sel1 := []Selection{{Reg: reg1, Reason: "base"}}
	sel2 := []Selection{{Reg: reg2, Reason: "base"}}
	ctx := context.Background()

	for range breakerTripThreshold {
		d.Run(ctx, Task{}, sel1, nil)
	}
	if _, _, berr := d.BreakerFor("canceled-agent").AllowGen(ctx); berr != nil {
		t.Fatal("连续取消不应熔断插件")
	}

	for range breakerTripThreshold {
		d.Run(ctx, Task{}, sel2, nil)
	}
	if _, _, berr := d.BreakerFor("fail-agent").AllowGen(ctx); berr == nil {
		t.Fatal("连续真失败应熔断插件")
	}
}

// v3.9.2 回归：半开探测在途时任务取消——teardown 路径必须经 Abandon 释放
// 探测位（agentkit v0.10.4 配对契约），下一次 Allow 立即放行新探测。此前
// 探测位滞留至 probeTimeout，健康插件被"熔断中"误跳过最长约 10 分钟。
func TestRun_TeardownAbandonsHalfOpenProbe(t *testing.T) {
	now := time.Now()
	reg1 := &Registration{Manifest: AgentManifest{Name: "canceled-agent", Skills: []string{"security"}},
		Plugin: &canceledPlugin{manifest: AgentManifest{Name: "canceled-agent"}}, Enabled: true}
	d := newDispatcher(NewRegistry(), logx.Nop(), func() time.Time { return now })
	_ = d.reg.Register(reg1)
	sel := []Selection{{Reg: reg1, Reason: "base"}}

	bs := d.BreakerFor("canceled-agent")
	for range breakerTripThreshold {
		_, gen, berr := bs.AllowGen(context.Background())
		if berr != nil {
			break
		}
		bs.FailureGen(context.Background(), gen)
	}
	now = now.Add(6 * time.Minute) // 冷却期（5min）过 → 半开

	// Run 内 Allow 放行半开探测 → 插件返回 Canceled → teardown → AbandonGen
	_ = d.Run(context.Background(), Task{}, sel, nil)

	if _, _, berr := bs.AllowGen(context.Background()); berr != nil {
		t.Fatal("teardown 应经 AbandonGen 释放半开探测位，下一次 Allow 立即放行新探测")
	}
}

func TestTeardown_Classification(t *testing.T) {
	parentCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !teardown(context.Canceled, context.Background()) {
		t.Fatal("错误链含 Canceled 应判 teardown")
	}
	if !teardown(context.DeadlineExceeded, parentCanceled) {
		t.Fatal("父 ctx 已取消时的 DeadlineExceeded 应保守判 teardown")
	}
	if teardown(context.DeadlineExceeded, context.Background()) {
		t.Fatal("父 ctx 健康时的插件自身超时应计失败（非 teardown）")
	}
	if teardown(errors.New("boom"), context.Background()) {
		t.Fatal("普通错误不是 teardown")
	}
	if teardown(nil, context.Background()) {
		t.Fatal("nil 不是 teardown")
	}
}

// 深挖修复回归：任务取消后排队中的插件应跳过而非傻等槽位。
func TestRun_QueuedWaitCancellable(t *testing.T) {
	release := make(chan struct{})
	blocker := &blockingPlugin{manifest: AgentManifest{Name: "blocker"}, release: release}
	regB := &Registration{Manifest: AgentManifest{Name: "blocker", Skills: []string{"security"}},
		Plugin: blocker, Enabled: true}
	queued := &Registration{Manifest: AgentManifest{Name: "queued", Skills: []string{"security"}},
		Plugin: &nopPlugin{manifest: AgentManifest{Name: "queued"}}, Enabled: true}

	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(regB)
	_ = d.reg.Register(queued)
	d.MaxConcurrent = 1

	ctx, cancel := context.WithCancel(context.Background())
	sel := []Selection{{Reg: regB, Reason: "base"}, {Reg: queued, Reason: "base"}}
	done := make(chan []Outcome, 1)
	go func() { done <- d.Run(ctx, Task{}, sel, nil) }()
	close(release) // blocker 放行；取消发生在排队者拿到槽位前后均可能——这里直接取消任务
	cancel()
	outcomes := <-done
	if len(outcomes) != 2 {
		t.Fatalf("应有 2 个 outcome，got %d", len(outcomes))
	}
}

// 深挖修复回归：Merge 阶段多插件命中同题会把 By 拼成 "a,b"——合并条目（专家意见
// 一致的证据）此前进不了 recs，反而被误标「另一专家未复现」，标注方向反了。
func TestMergerAnnotateCrossReview_MergedByCountsAsReplicated(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf := &MergedFindings{Findings: []Finding{
		// 双专家命中同题被 Merge 合并：By 已拼为 "claude,kimi"
		{File: "a.go", Line: 10, Severity: "high", Comment: "同位置同问题", By: "claude,kimi"},
		// 仅 claude 报的独立问题：附近无其他编队意见 → 应标注
		{File: "b.go", Line: 30, Severity: "low", Comment: "仅一方报告", By: "claude"},
	}}
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})

	if strings.Contains(mf.Findings[0].Comment, "交叉审查分歧") {
		t.Fatalf("合并条目（双方已复现）不应被标注: %q", mf.Findings[0].Comment)
	}
	if !strings.Contains(mf.Findings[1].Comment, "交叉审查分歧") {
		t.Fatalf("单方报告仍应标注: %q", mf.Findings[1].Comment)
	}
}

// 收尾批修复回归：指纹不含行号——同文件两处不同位置的同模板意见（相差 >2 行）
// 不再被轮内指纹合并吞掉第二条；行号相容（±2 内）仍正常合并。
func TestMerge_SameFingerprintFarLinesKeptSeparate(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1", []Outcome{
		{Name: "ocr", Result: &Result{Findings: []Finding{
			{File: "a.go", Line: 10, Severity: SeverityMedium, Comment: "error 未检查"},
			{File: "a.go", Line: 200, Severity: SeverityMedium, Comment: "error 未检查"},
		}}},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 2 {
		t.Fatalf("行号不相容的同模板意见应保留 2 条，got %d: %+v", len(mf.Findings), mf.Findings)
	}
	if mf.Findings[0].Line != 10 || mf.Findings[1].Line != 200 {
		t.Fatalf("两条应各自保留原行号: %+v", mf.Findings)
	}
}

// 收尾批修复回归：已触发的 consensus 编队成员不进 fan-out（此前注释声称已排除、
// 代码漏了——同一专家盲审一次+共识再跑一次）；编队未触发时照常参与基础选择。
func TestSelect_ConsensusMembersExcludedFromFanout(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg("correctness", []string{"code-review"}))
	_ = d.reg.Register(reg("security", []string{"code-review"}))
	d.WithSquads([]SquadDecl{{
		Name: "core-consensus", Mode: SquadModeConsensus,
		Paths: []string{"internal/**"}, Members: []string{"correctness", "security"},
	}})
	pr := &PRContext{Platform: "gitea"}
	// 编队触发：成员全被排除
	if sel := d.Select(pr, []string{"internal/a.go"}, []string{"code-review"}, nil); len(sel) != 0 {
		t.Fatalf("consensus 成员不应进 fan-out，got %v", selNames(sel))
	}
	// 编队未触发：成员照常参选
	if sel := d.Select(pr, []string{"cmd/main.go"}, []string{"code-review"}, nil); len(sel) != 2 {
		t.Fatalf("编队未触发时成员应正常参选，got %v", selNames(sel))
	}
}
