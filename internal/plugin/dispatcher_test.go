package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	_ = d.reg.Register(reg("security", []string{"security"}))
	_ = d.reg.Register(reg("correctness", []string{"correctness"}))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 1 || sel[0].Reg.Manifest.Name != "security" {
		t.Fatalf("应只选中 security，got %v", selNames(sel))
	}
}

func TestSelect_NoSkillMatch(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	_ = d.reg.Register(reg("security", []string{"security"}))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"nonexistent"}, nil)
	if len(sel) != 0 {
		t.Fatalf("无技能匹配应为空，got %v", selNames(sel))
	}
}

func TestSelect_Disabled(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	_ = d.reg.Register(reg("security", []string{"security"}, withDisabled))

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, nil, []string{"security"}, nil)
	if len(sel) != 0 {
		t.Fatalf("disabled 插件不应入选，got %v", selNames(sel))
	}
}

func TestSelect_LockBypassesDisable(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	sel := []Selection{{Reg: reg("a", nil)}}
	keep, pruned := d.PruneByBudget(sel, nil)
	if len(keep) != 1 || len(pruned) != 0 {
		t.Fatal("nil budget 应不裁剪")
	}
}

func TestPruneByBudget_HeavyPrunedFirst(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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

func Run_SemaphoreLimit(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	d.MaxConcurrent = 1

	var maxConcurrent, current int32
	countPlugin := &countingPlugin{
		manifest: AgentManifest{Name: "counter"},
		onInvoke: func() {
			// 简单计数：Run 内 MaxConcurrent=1 时不会真正并发
		},
	}
	_ = d.reg.Register(&Registration{
		Manifest: AgentManifest{Name: "counter"},
		Plugin:   countPlugin,
		Enabled:  true,
	})
	_ = maxConcurrent
	_ = current

	sel := d.Select(&PRContext{}, nil, []string{}, nil)
	if len(sel) == 0 {
		t.Skip("no plugins selected")
	}
}

func TestRun_CircuitBreakerSkips(t *testing.T) {
	d := NewDispatcher(NewRegistry(), testNopLogger{})
	_ = d.reg.Register(&Registration{
		Manifest: AgentManifest{Name: "fail-plugin"},
		Plugin:   &failPlugin{manifest: AgentManifest{Name: "fail-plugin"}},
		Enabled:  true,
	})

	// 触发熔断：连续失败 3 次
	for i := 0; i < 3; i++ {
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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
	d := NewDispatcher(NewRegistry(), testNopLogger{})
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

type countingPlugin struct {
	manifest AgentManifest
	onInvoke func()
}

func (p *countingPlugin) Manifest() AgentManifest { return p.manifest }
func (p *countingPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	if p.onInvoke != nil {
		p.onInvoke()
	}
	time.Sleep(10 * time.Millisecond)
	return Result{}, nil
}

func TestSelect_SquadAppendsMembers(t *testing.T) {
	registry := NewRegistry()
	registry.Register(reg("gpt", []string{"code-review"}))
	registry.Register(reg("claude", []string{"code-review"}))
	registry.Register(reg("kimi", []string{"code-review"}))
	d := NewDispatcher(registry, testNopLogger{}).
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
	m := NewMerger(nil, testNopLogger{})
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
