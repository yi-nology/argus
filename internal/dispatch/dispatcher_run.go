// dispatcher_run.go —— fan-out 执行面：预算/熔断双门 + best-of-N 采样 + 单次执行的并发 machinery（Run 本体为熔断配对 B 类裁定项，仅文件归置不拆函数）。

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/syncx"
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

// OutcomeHook 单插件产物落定回调（v3.154.0 R4 逐插件 checkpoint）。
// 仅在 Result 非 nil（成功/部分成功）时触发；失败/跳过/取消不触发。
// 契约：实现方必须快速返回（在 fan-out 热路径上被同步调用）。
type OutcomeHook func(o Outcome)

// Run fan-out 并发执行选中的插件（各带超时），部分失败容忍。
// 返回顺序与输入 Selection 顺序一致。
func (d *Dispatcher) Run(ctx context.Context, task Task, sel []Selection, budget *TokenBudget) []Outcome {
	return d.RunWithHook(ctx, task, sel, budget, nil)
}

// RunWithHook 同 Run，逐插件产物落定（成功）时同步触发 hook。
// hook=nil 与 Run 完全等价；hook 实现方须快速返回（热路径同步调用）。
func (d *Dispatcher) RunWithHook(ctx context.Context, task Task, sel []Selection, budget *TokenBudget, hook OutcomeHook) []Outcome {
	outcomes := make([]Outcome, len(sel))
	// 预算随 Task 注入：插件内 LLM 调用（Reviewer 等）计入任务累计器。
	task.Budget = budget

	// 并发闸门（v3.174.0 手写信号量收编 oekit/concurrency/syncx.Gate）：
	// 限制同时执行的插件数，避免百炼等 API 触发 burst rate limit (429)。
	// Acquire 等待可取消（任务级 ctx 终止即拒）、release 幂等——与手写
	// sem chan 同语义，多一层幂等防双归还。
	gate := syncx.NewGate(d.MaxConcurrent)

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
		// gen 是本次放行的代：上游按代绑定回报，必须原样带进下面的 goroutine，
		// 否则回报会被判为陈旧代作废。
		brk := d.breakerFor(reg.Manifest.Name)
		_, gen, berr := brk.AllowGen(ctx)
		if berr != nil {
			o.SkippedReason = fmt.Sprintf("熔断中（连续失败，%s 冷却后自动探测恢复）", breakerCooldown)
			ReportPluginSkipped("circuit")
			d.log.Warn("argus.plugin.circuit_open", "plugin", reg.Manifest.Name)
			continue
		}

		wg.Add(1)
		async.GoSafe(func() {
			defer wg.Done()
			// 获取并发名额（限制并发）。等待必须可取消：任务级 ctx 终止后，
			// 排队 goroutine 不能继续傻等前序插件跑完（深挖修复）。
			release, gerr := gate.Acquire(ctx)
			if gerr != nil {
				// 排队被取消：Allow 已消费但未执行——Abandon 释放半开
				// 探测位（不计失败；配对契约=Success/Failure/Abandon
				// 恰好其一，v0.10.4）
				brk.AbandonGen(gen)
				o.SkippedReason = "任务已取消（等待执行槽位）"
				ReportPluginSkipped("canceled")
				return
			}
			defer release()
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
					brk.SuccessGen(gen)
					result := "full"
					if k < n {
						result = "partial"
					}
					ReportBestOfN(reg.Manifest.Name, result)
					if hook != nil {
						hook(*o)
					}
				case canceled:
					// 任务级取消不是插件故障：Abandon 释放半开探测位（配对契约）
					brk.AbandonGen(gen)
					o.SkippedReason = "任务已取消"
					d.log.Warn("argus.plugin.sampling_canceled", "plugin", reg.Manifest.Name)
				default:
					brk.FailureGen(ctx, gen)
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
				brk.SuccessGen(gen)
				if hook != nil {
					hook(*o)
				}
			case teardown(err, ctx):
				// 任务级取消/终止不是插件故障：不计熔断连续失败——否则连续 3 次
				// 滚动发布就能把 security 等 lock 插件误熔断 5 分钟（深挖修复）。
				// 结果未知也不计成功——Abandon 释放半开探测位（v0.10.4 配对契约）。
				brk.AbandonGen(gen)
				o.SkippedReason = "任务已取消"
				d.log.Warn("argus.plugin.canceled", "plugin", reg.Manifest.Name,
					"duration_ms", o.Duration.Milliseconds(), "error", err.Error())
			default:
				brk.FailureGen(ctx, gen)
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
