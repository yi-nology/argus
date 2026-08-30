package plugin

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
)

// costRank 越大越贵（预算裁剪按降序裁：heavy 先裁）。
func costRank(cost string) int {
	switch cost {
	case CostHeavy:
		return 3
	case CostStandard:
		return 2
	default:
		return 1
	}
}

// TokenBudget 任务内 token 累计器（design §10.3：预算判定用任务内累计器，
// 不用 Prometheus 指标判定——那是聚合计数器，避免基数爆炸）。
type TokenBudget struct {
	limit int
	used  int64
	mu    sync.Mutex
}

func NewTokenBudget(limit int) *TokenBudget { return &TokenBudget{limit: limit} }

func (b *TokenBudget) Add(n int) {
	b.mu.Lock()
	b.used += int64(n)
	b.mu.Unlock()
}

func (b *TokenBudget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.used)
}

func (b *TokenBudget) Limit() int { return b.limit }

func (b *TokenBudget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.limit - int(b.used)
	if r < 0 {
		return 0
	}
	return r
}

// Selection 一条被选中的插件及其入选原因（可观测）。
type Selection struct {
	Reg    *Registration
	Reason string // base | use | lock | routing(paths=...|labels=...)
}

// Outcome 单插件执行结果（含跳过/失败原因，供降级注明）。
type Outcome struct {
	Name          string
	Manifest      AgentManifest
	Result        *Result
	Err           error
	SkippedReason string // 非空 = 未执行（熔断/预算/裁剪）
	Duration      time.Duration
}

// Notes 汇总降级注明文案。
func (o Outcome) Notes() string {
	switch {
	case o.SkippedReason != "":
		return fmt.Sprintf("%s 未参与（%s）", o.Name, o.SkippedReason)
	case o.Err != nil:
		return fmt.Sprintf("%s 未参与（%v）", o.Name, o.Err)
	default:
		return ""
	}
}

// Dispatcher 确定性路由 + fan-out 执行（design §3.3）。
type Dispatcher struct {
	reg      *Registry
	breakers *Breakers
	log      logx.Logger
	Now      func() time.Time
}

func NewDispatcher(reg *Registry, log logx.Logger) *Dispatcher {
	return &Dispatcher{reg: reg, breakers: NewBreakers(), log: log, Now: time.Now}
}

// Registry 暴露注册表（workflow 加载仓库配置后做信任边界解析用）。
func (d *Dispatcher) Registry() *Registry { return d.reg }

// Breakers 暴露给观测/测试。
func (d *Dispatcher) Breakers() *Breakers { return d.breakers }

// Select 按 `技能需求 × 仓库配置 × Triggers` 选出插件集合（确定性，无 LLM）。
// 语义：
//   - 全局 enabled=false 的插件永不参与；
//   - Triggers 为空的插件是默认候选；有 Triggers 的仅命中时参与；
//   - lockRepoConfig 插件无视仓库配置，只要触发即参与（红线 3）；
//   - 仓库 use 非空 = 白名单（锁定插件除外）；disable 黑名单；
//   - routing 命中（paths/labels）追加其 use 引用的插件。
func (d *Dispatcher) Select(pr *PRContext, changedFiles []string, skills []string, rc *RepoConfig) []Selection {
	want := map[string]bool{}
	for _, s := range skills {
		want[s] = true
	}
	var out []Selection
	add := func(reg *Registration, reason string) {
		if !reg.Enabled {
			return
		}
		if !skillsIntersect(reg.Manifest.Skills, skills) {
			return
		}
		for _, s := range out { // 去重，保留首个原因
			if s.Reg.Manifest.Name == reg.Manifest.Name {
				return
			}
		}
		out = append(out, Selection{Reg: reg, Reason: reason})
	}

	useSet := map[string]bool{}
	if rc != nil {
		for _, n := range rc.Use {
			useSet[n] = true
		}
	}
	disableSet := map[string]bool{}
	if rc != nil {
		for _, n := range rc.Disable {
			disableSet[n] = true
		}
	}

	for _, reg := range d.reg.Snapshot() {
		if !reg.Enabled {
			continue
		}
		if !skillsIntersect(reg.Manifest.Skills, skills) {
			continue
		}
		triggered := reg.Manifest.Triggers.Empty() ||
			reg.Manifest.Triggers.Match(changedFiles, pr.Labels, pr.EventType)
		if !triggered {
			continue
		}
		if reg.LockRepoConfig {
			add(reg, "lock")
			continue
		}
		if disableSet[reg.Manifest.Name] {
			continue
		}
		if len(useSet) > 0 && !useSet[reg.Manifest.Name] {
			continue
		}
		add(reg, "base")
	}

	// routing 命中追加（routing 只引用已注册插件名——解析期已校验）
	if rc != nil {
		for i, rule := range rc.Routing {
			matched := matchAnyGlob(rule.Paths, changedFiles) || matchAnyLabel(rule.Labels, pr.Labels)
			if !matched {
				continue
			}
			for _, n := range rule.Use {
				reg, ok := d.reg.Get(n)
				if !ok {
					continue
				}
				if reg.LockRepoConfig {
					add(reg, "lock")
					continue
				}
				if disableSet[n] {
					continue
				}
				add(reg, fmt.Sprintf("routing[%d]", i))
			}
		}
	}

	// 稳定顺序：lock > base/use > routing，同类按注册顺序（Select 遍历已有序）
	return out
}

func skillsIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func matchAnyGlob(pats, files []string) bool {
	for _, f := range files {
		for _, p := range pats {
			if GlobMatch(p, f) {
				return true
			}
		}
	}
	return false
}

func matchAnyLabel(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if w == h {
				return true
			}
		}
	}
	return false
}

// PruneByBudget 预算吃紧时按 Cost 降序裁剪非锁定插件（heavy 先裁，§10.3）。
// 返回保留的 Selection 与被裁名单（供降级注明）。
func (d *Dispatcher) PruneByBudget(sel []Selection, budget *TokenBudget) (keep []Selection, pruned []string) {
	const reserveRatio = 0.1 // 预留 10% 给锁定插件之外的收尾阶段
	if budget == nil {
		return sel, nil
	}
	reserve := budget.Limit() * 10 / 100
	keep = append([]Selection(nil), sel...)
	for budget.Remaining() < reserve {
		// 找 keep 中 cost 最高且非 lock 的下标
		idx := -1
		topRank := -1
		for i, s := range keep {
			if s.Reg.LockRepoConfig {
				continue
			}
			r := costRank(s.Reg.Manifest.Cost)
			if r > topRank {
				topRank = r
				idx = i
			}
		}
		if idx < 0 {
			break // 只剩锁定插件：预算豁免，超预算继续跑（§10.3）
		}
		pruned = append(pruned, keep[idx].Reg.Manifest.Name)
		keep = append(keep[:idx], keep[idx+1:]...)
	}
	// 预算彻底耗尽时锁定插件继续跑 + 告警
	if budget.Remaining() <= 0 {
		for _, s := range keep {
			if s.Reg.LockRepoConfig {
				d.log.Warn("argus.budget.lock_exceeded", "plugin", s.Reg.Manifest.Name,
					"reason", "预算已耗尽，lockRepoConfig 插件豁免继续执行")
			}
		}
	}
	return keep, pruned
}

// Run fan-out 并发执行选中的插件（各带超时），部分失败容忍。
// 返回顺序与输入 Selection 顺序一致。
func (d *Dispatcher) Run(ctx context.Context, task Task, sel []Selection, budget *TokenBudget) []Outcome {
	outcomes := make([]Outcome, len(sel))
	var wg sync.WaitGroup
	for i, s := range sel {
		reg := s.Reg
		o := &outcomes[i]
		o.Name = reg.Manifest.Name
		o.Manifest = reg.Manifest

		// 熔断门
		if !d.breakers.Allow(reg.Manifest.Name) {
			o.SkippedReason = "熔断中（连续失败，5 分钟后探测恢复）"
			d.log.Warn("argus.plugin.circuit_open", "plugin", reg.Manifest.Name)
			continue
		}
		// 预算门（锁定插件豁免）
		if budget != nil && budget.Remaining() <= 0 && !reg.LockRepoConfig {
			o.SkippedReason = "任务 token 预算耗尽"
			continue
		}

		wg.Add(1)
		go func(i int, reg *Registration, o *Outcome) {
			defer wg.Done()
			timeout := reg.Manifest.Timeout
			if timeout <= 0 {
				timeout = 120 * time.Second
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := d.Now()
			res, err := safeInvoke(cctx, reg.Plugin, task)
			o.Duration = d.Now().Sub(start)
			o.Err = err
			if err == nil {
				o.Result = &res
				d.breakers.Success(reg.Manifest.Name)
			} else {
				d.breakers.Failure(reg.Manifest.Name)
				obsPluginFailure(reg.Manifest.Name)
				d.log.Error("argus.plugin.failed", "plugin", reg.Manifest.Name,
					"duration_ms", o.Duration.Milliseconds(), "error", err.Error())
			}
			obsPluginDuration(reg.Manifest.Name, o.Duration)
		}(i, reg, o)
	}
	wg.Wait()
	return outcomes
}

// safeInvoke 防插件 panic 拖垮 worker（超时由 ctx + 适配器内部尊重 ctx 实现；
// cli/remote 适配器自己保证子进程随 ctx 终止）。
func safeInvoke(ctx context.Context, p AgentPlugin, task Task) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("插件 panic: %v", r)
		}
	}()
	return p.Invoke(ctx, task)
}

// OutcomeNames 结果里的插件名列表（报告元信息）。
func OutcomeNames(outcomes []Outcome) []string {
	var names []string
	for _, o := range outcomes {
		if o.Err == nil && o.SkippedReason == "" {
			names = append(names, o.Name)
		}
	}
	sort.Strings(names)
	return names
}
