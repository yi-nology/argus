package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"git.example.com/enjoydream/agentkit/breaker"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/ekit/concurrency/async"
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
// 实现委托 agentkit/llm.Budget（此前为逐方法克隆）。
type TokenBudget = llm.Budget

// NewTokenBudget 创建任务预算累计器。
func NewTokenBudget(limit int) *TokenBudget { return llm.NewBudget(limit) }

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

// Notes 汇总降级注明文案。错误串统一脱敏：插件错误链可能携带带凭证的
// clone URL（git 传输失败回显），此文本直通报告并作为 PR 评论发布。
func (o Outcome) Notes() string {
	switch {
	case o.SkippedReason != "":
		return fmt.Sprintf("%s 未参与（%s）", o.Name, logredact.Redact(o.SkippedReason))
	case o.Err != nil:
		return fmt.Sprintf("%s 未参与（%s）", o.Name, logredact.Redact(o.Err.Error()))
	default:
		return ""
	}
}

// Dispatcher 确定性路由 + fan-out 执行（design §3.3）。
type Dispatcher struct {
	squads   []SquadDecl
	reg      *Registry
	breakers *breaker.Breakers
	log      logx.Logger
	Now      func() time.Time
	// MaxConcurrent 限制同时执行的插件数（0 = 不限制）。
	// 百炼等 API 有 burst rate limit，并发过高会触发 429。
	MaxConcurrent int
}

func NewDispatcher(reg *Registry, log logx.Logger) *Dispatcher {
	// 熔断参数显式取 agentkit 缺省（trip=3/cooldown=5min），行为不变但
	// SkippedReason 文案可从同一常量派生，不再硬编码"5 分钟"
	// 半开探测时限 ≥ 最长插件超时 + 30s 余量（深挖 P0 遗留修复，agentkit
	// v0.10.1 WithProbeTimeout）：探测时限小于插件超时时，慢而健康的插件
	//（如 600s compliance）在探测在途期间会被并发双探测双跑。
	// 注册表为空（测试/极早调用）回退 agentkit 缺省。
	probe := time.Duration(0)
	for _, r := range reg.Snapshot() {
		if r.Manifest.Timeout > probe {
			probe = r.Manifest.Timeout
		}
	}
	var opts []breaker.Option
	if probe > breaker.DefaultProbeTimeout {
		opts = append(opts, breaker.WithProbeTimeout(probe+30*time.Second))
	}
	return &Dispatcher{reg: reg, breakers: breaker.NewBreakers(breaker.DefaultTripThreshold, breaker.DefaultCooldown, opts...), log: log, Now: time.Now, MaxConcurrent: 2}
}

// Registry 暴露注册表（workflow 加载仓库配置后做信任边界解析用）。
func (d *Dispatcher) Registry() *Registry { return d.reg }

// Breakers 暴露给观测/测试。
func (d *Dispatcher) Breakers() *breaker.Breakers { return d.breakers }

// Select 按 `技能需求 × 仓库配置 × Triggers` 选出插件集合（确定性，无 LLM）。
// 语义：
//   - 全局 enabled=false 的插件永不参与；
//   - Triggers 为空的插件是默认候选；有 Triggers 的仅命中时参与；
//   - lockRepoConfig 插件无视仓库配置，只要触发即参与（红线 3）；
//   - 仓库 use 非空 = 白名单（锁定插件除外）；disable 黑名单；
//   - routing 命中（paths/labels）追加其 use 引用的插件。
//
// WithSquads 注入交叉审查编队声明（plugins.yaml squads 段；链式）。
// 启动期静态校验（深挖修复）：未知 mode 静默按互盲跑（与用户预期的共识语义
// 完全不同）、paths+labels 双空的编队永不触发——此前都是无声死配置。
func (d *Dispatcher) WithSquads(sq []SquadDecl) *Dispatcher {
	for i := range sq {
		s := &sq[i]
		if s.Mode != "" && s.Mode != SquadModeConsensus {
			d.log.Warn("argus.squad.unknown_mode", "squad", s.Name,
				"mode", string(s.Mode), "effect", "按独立（互盲）模式处理")
			s.Mode = ""
		}
		if len(s.Paths) == 0 && len(s.Labels) == 0 {
			d.log.Warn("argus.squad.no_trigger", "squad", s.Name,
				"effect", "paths 与 labels 均为空，该编队永远不会触发")
		}
		if len(s.Members) == 0 {
			d.log.Warn("argus.squad.no_members", "squad", s.Name,
				"effect", "未声明成员，该编队永远不会触发")
		}
	}
	d.squads = sq
	return d
}

// squadTriggered 编队触发谓词：paths 命中变更文件或 labels 命中 PR 标签
// （ActiveSquadMembers 与 Select 的 consensus 排除/编队追加共用单源）。
func squadTriggered(sq SquadDecl, changedFiles []string, labels []string) bool {
	return (len(sq.Paths) > 0 && matchAnyGlob(sq.Paths, changedFiles)) ||
		(len(sq.Labels) > 0 && listOverlap(sq.Labels, labels))
}

// ActiveSquadMembers 命中触发条件的编队成员名（去重、保序；交叉审查分歧标注用）。
// 仅统计独立（互盲）模式编队——consensus 编队成员互看过彼此发现，不存在
// "盲审分歧"，标注反而误导（v3.4）。
func (d *Dispatcher) ActiveSquadMembers(pr *PRContext, changedFiles []string) []string {
	if len(d.squads) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var members []string
	for _, sq := range d.squads {
		if sq.Mode == SquadModeConsensus {
			continue // consensus 编队成员互看过彼此发现，无盲审分歧可标
		}
		// 空 mode 与未知 mode 一律按独立模式处理（配置宽容，行为可预期）
		if !squadTriggered(sq, changedFiles, pr.Labels) {
			continue
		}
		for _, m := range sq.Members {
			if !seen[m] {
				seen[m] = true
				members = append(members, m)
			}
		}
	}
	return members
}

// selector Select 的累积状态机：add 去重门 + base/routing/squads 三条入选通道
// （v3.98.0 由 Select 单体收口，语义逐行保真）。
type selector struct {
	d            *Dispatcher
	pr           *PRContext
	changedFiles []string
	skills       []string
	rc           *RepoConfig
	out          []Selection
	seen         map[string]bool
	consensus    map[string]bool // consensus 编队成员不进 fan-out（v3.4 语义）
	useSet       map[string]bool
	disableSet   map[string]bool
}

func (d *Dispatcher) newSelector(pr *PRContext, changedFiles, skills []string, rc *RepoConfig) *selector {
	s := &selector{d: d, pr: pr, changedFiles: changedFiles, skills: skills, rc: rc,
		seen: map[string]bool{}, consensus: map[string]bool{},
		useSet: map[string]bool{}, disableSet: map[string]bool{}}
	if rc != nil {
		for _, n := range rc.Use {
			s.useSet[n] = true
		}
		for _, n := range rc.Disable {
			s.disableSet[n] = true
		}
	}
	return s
}

// add 入选门：enabled × consensus 排除 × skills 交集 × 去重（保留首个原因）。
func (s *selector) add(reg *Registration, reason string) {
	if !reg.Enabled {
		return
	}
	if s.consensus[reg.Manifest.Name] {
		return
	}
	if !listOverlap(reg.Manifest.Skills, s.skills) {
		return
	}
	if s.seen[reg.Manifest.Name] { // 保留首个原因
		return
	}
	s.seen[reg.Manifest.Name] = true
	s.out = append(s.out, Selection{Reg: reg, Reason: reason})
}

// base 基础通道：Triggers 命中的注册插件（lock 无视仓库配置；use 白名单/disable
// 黑名单只约束普通插件）。
func (s *selector) base() {
	for _, reg := range s.d.reg.Snapshot() {
		if !reg.Enabled {
			continue
		}
		if !listOverlap(reg.Manifest.Skills, s.skills) {
			continue
		}
		triggered := reg.Manifest.Triggers.Empty() ||
			reg.Manifest.Triggers.Match(s.changedFiles, s.pr.Labels, s.pr.EventType)
		if !triggered {
			continue
		}
		if reg.LockRepoConfig {
			s.add(reg, "lock")
			continue
		}
		if s.disableSet[reg.Manifest.Name] {
			continue
		}
		if len(s.useSet) > 0 && !s.useSet[reg.Manifest.Name] {
			continue
		}
		s.add(reg, "base")
	}
}

// routing 通道：仓库 routing 规则命中（paths/labels）追加其 use 引用的插件
// （routing 只引用已注册插件名——解析期已校验）。
func (s *selector) routing() {
	if s.rc == nil {
		return
	}
	for i, rule := range s.rc.Routing {
		if !matchAnyGlob(rule.Paths, s.changedFiles) && !listOverlap(rule.Labels, s.pr.Labels) {
			continue
		}
		for _, n := range rule.Use {
			reg, ok := s.d.reg.Get(n)
			if !ok {
				continue
			}
			if reg.LockRepoConfig {
				s.add(reg, "lock")
				continue
			}
			if s.disableSet[n] {
				continue
			}
			s.add(reg, fmt.Sprintf("routing[%d]", i))
		}
	}
}

// squads 通道：交叉审查编队追加（v3.3）。skills 词表不做限制——编队是"额外专家"，
// 不受仓库 use/disable 白名单约束，但插件必须在注册表且启用中；已在表中的不重复。
// v3.4：consensus 模式编队成员不进 fan-out——他们走 RunConsensus 黑板互看
// （workflow 在 fan-out 后单独召集），盲审并发对它们反而是错误语义。
func (s *selector) squads() {
	for _, sq := range s.d.squads {
		if sq.Mode == SquadModeConsensus {
			continue
		}
		if !squadTriggered(sq, s.changedFiles, s.pr.Labels) {
			continue
		}
		for _, n := range sq.Members {
			reg, ok := s.d.reg.Get(n)
			if !ok || !reg.Enabled || s.seen[n] {
				continue
			}
			s.seen[n] = true
			s.out = append(s.out, Selection{Reg: reg, Reason: "squad:" + sq.Name})
		}
	}
}

// Select 按 `技能需求 × 仓库配置 × Triggers` 选出插件集合（确定性，无 LLM）。
// 语义：
//   - 全局 enabled=false 的插件永不参与；
//   - Triggers 为空的插件是默认候选；有 Triggers 的仅命中时参与；
//   - lockRepoConfig 插件无视仓库配置，只要触发即参与（红线 3）；
//   - 仓库 use 非空 = 白名单（锁定插件除外）；disable 黑名单；
//   - routing 命中（paths/labels）追加其 use 引用的插件。
//
// 稳定顺序：lock > base/use > routing > squad，同类按名称字母序（Registry.Snapshot 已排序）。
func (d *Dispatcher) Select(pr *PRContext, changedFiles []string, skills []string, rc *RepoConfig) []Selection {
	s := d.newSelector(pr, changedFiles, skills, rc)
	// consensus 编队成员不进 fan-out（v3.4 语义）：此前注释声称 Select 已排除、
	// 代码却漏了——同一专家会被盲审一次、黑板共识再跑一次（双倍 token + 结果
	// 双路径入库）。触发条件命中才排除：编队未触发时成员照常走基础选择。
	for _, sq := range d.squads {
		if sq.Mode != SquadModeConsensus {
			continue
		}
		if squadTriggered(sq, changedFiles, pr.Labels) {
			for _, m := range sq.Members {
				s.consensus[m] = true
			}
		}
	}
	s.base()
	s.routing()
	s.squads()
	return s.out
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

// listOverlap 两列表是否有公共元素（技能交集/标签匹配共用谓词，v3.76.0
// 由 skillsIntersect/matchAnyLabel 两份同逻辑合一）。
func listOverlap(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// PruneByBudget 预算吃紧时按 Cost 降序裁剪非锁定插件（heavy 先裁，§10.3）。
// 返回保留的 Selection 与被裁名单（供降级注明）。
func (d *Dispatcher) PruneByBudget(sel []Selection, budget *TokenBudget) (keep []Selection, pruned []string) {
	if budget == nil {
		return sel, nil
	}
	reserve := budget.Limit() * 10 / 100 // 预留 10% 给锁定插件之外的收尾阶段
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
	// 预算随 Task 注入：插件内 LLM 调用（Reviewer 等）计入任务累计器。
	task.Budget = budget

	// 并发信号量：限制同时执行的插件数，避免百炼等 API 触发 burst rate limit (429)。
	var sem chan struct{}
	if d.MaxConcurrent > 0 {
		sem = make(chan struct{}, d.MaxConcurrent)
	}

	var wg sync.WaitGroup
	for i, s := range sel {
		reg := s.Reg
		o := &outcomes[i]
		o.Name = reg.Manifest.Name
		o.Manifest = reg.Manifest

		// 预算门（锁定插件豁免）——先于熔断门：预算跳过的插件不消费 Allow。
		// （agentkit v0.7.0 起半开探测有 DefaultProbeTimeout 超时兜底，此顺序
		// 不再是硬正确性约束，但仍是最优顺序——避免无谓占用探测窗口。）
		if budget != nil && budget.Remaining() <= 0 && !reg.LockRepoConfig {
			o.SkippedReason = "任务 token 预算耗尽"
			ReportPluginSkipped("budget")
			continue
		}
		// 熔断门（Allow 后必然配对 Success/Failure/Abandon——见下方 invoke 分支）
		if !d.breakers.Allow(reg.Manifest.Name) {
			o.SkippedReason = fmt.Sprintf("熔断中（连续失败，%s 冷却后自动探测恢复）", breaker.DefaultCooldown)
			ReportPluginSkipped("circuit")
			d.log.Warn("argus.plugin.circuit_open", "plugin", reg.Manifest.Name)
			continue
		}

		wg.Add(1)
		async.GoSafe(func() {
			defer wg.Done()
			// 获取信号量（限制并发）。等待必须可取消：任务级 ctx 终止后，
			// 排队 goroutine 不能继续傻等前序插件跑完（深挖修复）。
			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					// 排队被取消：Allow 已消费但未执行——Abandon 释放半开
					// 探测位（不计失败；配对契约=Success/Failure/Abandon
					// 恰好其一，v0.10.4）
					d.breakers.Abandon(reg.Manifest.Name)
					o.SkippedReason = "任务已取消（等待执行槽位）"
					ReportPluginSkipped("canceled")
					return
				}
			}
			timeout := reg.Manifest.Timeout
			if timeout <= 0 {
				timeout = 120 * time.Second
			}

			// best-of-N 路径（v3.10.0）：N 份采样并发执行、逐采样独立超时，
			// 聚合后按一次插件执行配对熔断（Allow 一次 → Success/Failure/Abandon
			// 恰好其一）；部分成功照常产结果，降级注记随 Result.Notes 透出。
			if n := ClampReviewN(reg.Manifest.ReviewN); n > 1 {
				start := d.Now()
				res, k, canceled, sampleErr := d.runSampled(ctx, reg.Plugin, task, n, timeout)
				o.Duration = d.Now().Sub(start)
				switch {
				case k > 0:
					o.Result = &res
					d.breakers.Success(reg.Manifest.Name)
					result := "full"
					if k < n {
						result = "partial"
					}
					ReportBestOfN(reg.Manifest.Name, result)
				case canceled:
					// 任务级取消不是插件故障：Abandon 释放半开探测位（配对契约）
					d.breakers.Abandon(reg.Manifest.Name)
					o.SkippedReason = "任务已取消"
					d.log.Warn("argus.plugin.sampling_canceled", "plugin", reg.Manifest.Name)
				default:
					d.breakers.Failure(reg.Manifest.Name)
					ReportPluginFailure(reg.Manifest.Name)
					o.Err = sampleErr
					d.log.Error("argus.plugin.failed", "plugin", reg.Manifest.Name,
						"duration_ms", o.Duration.Milliseconds(), "error", logredact.Redact(sampleErr.Error()))
				}
				obsPluginDuration(reg.Manifest.Name, o.Duration)
				return
			}

			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := d.Now()
			res, err := safeInvoke(cctx, reg.Plugin, task)
			o.Duration = d.Now().Sub(start)
			o.Err = err
			switch {
			case err == nil:
				o.Result = &res
				d.breakers.Success(reg.Manifest.Name)
			case teardown(err, ctx):
				// 任务级取消/终止不是插件故障：不计熔断连续失败——否则连续 3 次
				// 滚动发布就能把 security 等 lock 插件误熔断 5 分钟（深挖修复）。
				// 结果未知也不计成功——Abandon 释放半开探测位（v0.10.4 配对契约）。
				d.breakers.Abandon(reg.Manifest.Name)
				o.SkippedReason = "任务已取消"
				d.log.Warn("argus.plugin.canceled", "plugin", reg.Manifest.Name,
					"duration_ms", o.Duration.Milliseconds(), "error", err.Error())
			default:
				d.breakers.Failure(reg.Manifest.Name)
				ReportPluginFailure(reg.Manifest.Name)
				d.log.Error("argus.plugin.failed", "plugin", reg.Manifest.Name,
					"duration_ms", o.Duration.Milliseconds(), "error", logredact.Redact(err.Error()))
			}
			obsPluginDuration(reg.Manifest.Name, o.Duration)
		})
	}
	wg.Wait()
	return outcomes
}

// runSampled best-of-N 采样执行：同一插件同 prompt 并发跑 n 次（逐采样独立超时），
// 成功采样经 aggregateSamples 聚合（复现计数/严重度升级/降级注记）。
// 返回聚合结果、成功采样数 k、是否发生任务级取消、失败采样错误（k>0 时为 nil）。
// 熔断配对由调用方按 (k, canceled) 统一决策——本函数不触碰 breaker。
func (d *Dispatcher) runSampled(ctx context.Context, p AgentPlugin, task Task, n int, timeout time.Duration) (Result, int, bool, error) {
	type sampleRes struct {
		res Result
		err error
	}
	results := make([]sampleRes, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		async.GoSafe(func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			results[i].res, results[i].err = safeInvoke(sctx, p, task)
		})
	}
	wg.Wait()

	var ok []Result
	canceled := false
	var lastErr error
	for _, r := range results {
		switch {
		case r.err == nil:
			ok = append(ok, r.res)
		case teardown(r.err, ctx):
			canceled = true
		default:
			lastErr = r.err
		}
	}
	if len(ok) == 0 {
		if lastErr == nil && canceled {
			lastErr = context.Canceled
		}
		return Result{}, 0, canceled, lastErr
	}
	agg := aggregateSamples(n, len(ok), ok)
	return agg, len(ok), canceled, nil
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

// teardown 判定失败是否源于任务级终止（优雅停机/任务中止）而非插件自身故障：
//   - 错误链含 context.Canceled —— 调用方主动取消；
//   - 父 ctx 已带错误（取消或整体超时）时的 DeadlineExceeded —— 插件级超时
//     与任务级终止叠加，无法归因插件，保守不计数。
//
// 插件自身超时（父 ctx 健康、插件 cctx 到期）不算 teardown，照常计入熔断。
func teardown(err error, parent context.Context) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return parent.Err() != nil && errors.Is(err, context.DeadlineExceeded)
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
