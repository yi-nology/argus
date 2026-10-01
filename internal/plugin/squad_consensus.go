// squad_consensus.go —— 编队黑板共识模式（v3.4，agentkit v0.8.0 blackboard 架构原语）。
//
// 与 v3.3 互盲编队（mode 留空）的差别：consensus 编队的成员不进 fan-out 盲审，
// 而是在共享黑板上轮转协作——每位专家每轮看到此前所有人报告的发现，只补充新问题；
// 一整轮无人补充 = 共识达成（分析饱和），或到轮次上限为止（agentkit README
// "Blackboard 该用"：多视角分析同一材料且视角间要互看、贡献顺序不可预知、
// 需要共识收敛信号）。
//
// 互看通道 = Task.Artifacts["squad_board"]（逐轮重建的增量视图），消费方为
// builtin Reviewer 与 acpx 适配器的提示词装配。纪律：
//   - 成员失败不中止召集（blackboard.Convene 的 Act 出错会中止整轮——这里把
//     成员失败转成黑板 note 条目 + 降级注明，返回"本轮未贡献"）；
//   - 预算/熔断门与 Dispatcher.Run 同语义（Allow 后必然配对 Success/Failure）；
//   - 轮次上限 2（首轮全量 + 一轮互看补漏）：共识收益递减而成本线性上涨，
//     比 agentkit 缺省 3 收紧一档；
//   - 收敛的发现按成员归属构造独立 Outcome 进 Merger——指纹去重/抑制过滤/
//     跨轮合并全部复用既有管线，共识成员不参与分歧标注（互看后无盲审分歧）。
package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/blackboard"
	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/textutil"
)

// ArtifactSquadBoard 共享黑板视图在 Task.Artifacts 中的注入键（v3.4）。
const ArtifactSquadBoard = "squad_board"

// consensusMaxRounds 黑板共识轮次上限：首轮全量 + 一轮互看补漏。
const consensusMaxRounds = 2

// 黑板条目词表（Entry.Kind）。
const (
	boardKindTask    = "task"    // 种子条目：任务素材摘要
	boardKindFinding = "finding" // 成员报告的发现
	boardKindNote    = "note"    // 执行失败/预算跳过等备注
)

// seedMaxRunes 种子条目截断上限（变更摘要可能很长，黑板要轻）。
const seedMaxRunes = 2000

// ConsensusSquads 返回触发条件命中的 consensus 模式编队（Select 的编队段对应物；
// 这些编队的成员不进 fan-out，由 RunConsensus 单独召集）。
func (d *Dispatcher) ConsensusSquads(pr *PRContext, changedFiles []string) []SquadDecl {
	var out []SquadDecl
	for _, sq := range d.squads {
		if sq.Mode != SquadModeConsensus {
			continue
		}
		if (len(sq.Paths) > 0 && matchAnyGlob(sq.Paths, changedFiles)) ||
			(len(sq.Labels) > 0 && listOverlap(sq.Labels, pr.Labels)) {
			out = append(out, sq)
		}
	}
	return out
}

// consensusMember 单个共识成员的运行期状态（Outcome 组装依据）。
type consensusMember struct {
	reg       *Registration
	collected []Finding // 跨轮累计的新发现（已在黑板上的不重复收）
	ran       bool      // 至少完整执行过一次
	lastErr   error
	skipped   string // 非空 = 全程未执行（预算/熔断/解析失败）
}

// consensusEnv 单成员 Act 执行环境（闭包捕获集显式化）。taskCtx 是任务级
// context——teardown 判定（取消/终止≠插件故障）用的是它而非 actCtx（成员自身
// 超时派生），二者刻意区分。
type consensusEnv struct {
	task    Task
	squad   string
	budget  *TokenBudget
	boardFP map[string]bool
	st      *consensusMember
	name    string
	taskCtx context.Context
}

// RunConsensus 召集一个 consensus 编队：黑板种子 → 成员轮转互看 → 共识收敛，
// 返回按成员归属拆分的 Outcome（进 Merger 走既有合并管线）。
func (d *Dispatcher) RunConsensus(ctx context.Context, task Task, sq SquadDecl, budget *TokenBudget) []Outcome {
	members, outcomes := d.resolveConsensusMembers(sq)
	if len(members) == 0 {
		return outcomes
	}

	board := blackboard.NewBoard()
	board.Seed(boardKindTask, squadSeed(task, sq))
	d.log.Info("argus.squad.consensus_start", "squad", sq.Name, "members", len(members))
	start := time.Now()

	// 名字 → 成员状态（Act 经 env 携带回写）
	stateByName := map[string]*consensusMember{}
	for _, m := range members {
		stateByName[m.reg.Manifest.Name] = m
	}

	// 黑板已有指纹（跨成员跨轮去重：谁先报了，后面的人再报不算新发现）
	boardFP := map[string]bool{}

	specialists := make([]blackboard.Specialist, 0, len(members))
	for _, m := range members {
		name := m.reg.Manifest.Name
		env := &consensusEnv{task: task, squad: sq.Name, budget: budget,
			boardFP: boardFP, st: stateByName[name], name: name, taskCtx: ctx}
		specialists = append(specialists, blackboard.Specialist{
			Name: name,
			Act: func(actCtx context.Context, b *blackboard.Board, since int64) (bool, error) {
				return d.consensusAct(env, actCtx, b, since)
			},
		})
	}

	cr, err := blackboard.Convene(ctx, board, specialists, &blackboard.ConveneOptions{MaxRounds: consensusMaxRounds})
	if err != nil {
		// Convene 只在 Board/specialists 非法时报错（Act 失败已被上面转为 note），
		// 已收集成果照常返回
		d.log.Error("argus.squad.consensus_convene_failed", "squad", sq.Name, "error", err.Error())
	} else if anyConsensusContribution(members) {
		d.log.Info("argus.squad.consensus_done", "squad", sq.Name,
			"rounds", cr.Rounds, "board_entries", cr.Total,
			"findings", len(boardFP), "elapsed_ms", time.Since(start).Milliseconds())
	} else {
		// 深挖修复：预算耗尽/熔断/执行失败在 Act 里都折叠成 contributed=false，
		// Convene 对「一整轮无人贡献」即报收敛——全员无法贡献与真收敛不可分，
		// 共识被系统性高估。此处显式区分留痕。
		d.log.Warn("argus.squad.consensus_stalled", "squad", sq.Name,
			"rounds", cr.Rounds, "members", len(members),
			"hint", "全员未贡献（预算/熔断/失败），非共识收敛")
	}

	fillConsensusOutcomes(members, outcomes)
	return outcomes
}

// resolveConsensusMembers 成员解析：注册且启用（与 Select 的编队段同语义——不受
// 仓库 use/disable 约束）；声明重复的成员名只保留首个。返回成员状态与同序占位
// outcomes（未注册成员的 SkippedReason 已填）。单成员告警（无互看对象，仍按共识
// 流程跑——行为正确）；零成员时 outcomes 即最终结果。
func (d *Dispatcher) resolveConsensusMembers(sq SquadDecl) ([]*consensusMember, []Outcome) {
	var members []*consensusMember
	var outcomes []Outcome
	seen := map[string]bool{}
	for _, n := range sq.Members {
		if seen[n] {
			continue
		}
		seen[n] = true
		reg, ok := d.reg.Get(n)
		o := Outcome{Name: n}
		if ok {
			o.Manifest = reg.Manifest
		}
		if !ok || !reg.Enabled {
			o.SkippedReason = "未注册或未启用"
			outcomes = append(outcomes, o)
			continue
		}
		members = append(members, &consensusMember{reg: reg})
		// Outcome 顺序与成员声明序一致（占位，收尾回填）
		outcomes = append(outcomes, o)
	}
	if len(members) == 1 {
		// 单成员无互看价值：降级注明，退回独立执行语义由调用方决定——这里
		// 直接按共识流程跑一轮（行为正确，只是没有"互看"对象）。
		d.log.Warn("argus.squad.consensus_single_member", "squad", sq.Name)
	}
	return members, outcomes
}

// consensusAct 单成员单轮黑板行动：预算/熔断门 → 注入黑板增量视图 → 调用插件 →
// 结果指纹差分写回黑板。返回 true = 本轮有新贡献。成员失败转黑板 note 不中止整轮。
func (d *Dispatcher) consensusAct(env *consensusEnv, actCtx context.Context,
	b *blackboard.Board, since int64) (bool, error) {
	st, name := env.st, env.name
	// 预算门（锁定插件豁免语义不适用——编队成员无 lock 概念）
	if env.budget != nil && env.budget.Remaining() <= 0 {
		st.skipped = "任务 token 预算耗尽"
		b.Write(name, boardKindNote, "预算耗尽，本轮跳过")
		return false, nil
	}
	// 熔断门（Allow 后必然配对一次 Success/Failure/Abandon）
	if !d.breakers.Allow(name) {
		st.skipped = "熔断中"
		b.Write(name, boardKindNote, "熔断中，本轮跳过")
		return false, nil
	}

	// 逐轮重建任务副本：黑板增量视图注入 Artifacts（map 必须拷贝，
	// 防成员间/跨轮互相污染外层 task）
	t := env.task
	t.Budget = env.budget
	t.Artifacts = copyArtifacts(env.task.Artifacts)
	if t.Artifacts == nil {
		t.Artifacts = map[string]string{}
	}
	t.Artifacts[ArtifactSquadBoard] = renderBoardView(b.EntriesAfter(since))

	timeout := st.reg.Manifest.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	cctx, cancel := context.WithTimeout(actCtx, timeout)
	defer cancel()

	invStart := d.Now()
	res, err := safeInvoke(cctx, st.reg.Plugin, t)
	dur := d.Now().Sub(invStart)
	obsPluginDuration(name, dur)
	if err != nil {
		st.lastErr = err
		if teardown(err, env.taskCtx) {
			// 任务级取消/终止不是插件故障：不计熔断连续失败（与
			// Dispatcher.Run 同语义）——否则优雅停机打断共识时每个
			// 成员各记一次 Failure，连续 3 次滚动发布即误熔断
			// security 类成员（v3.9.1）。结果未知也不计成功——
			// Abandon 释放半开探测位（v0.10.4 配对契约）。
			d.breakers.Abandon(name)
			d.log.Warn("argus.squad.consensus_member_canceled", "squad", env.squad,
				"member", name, "error", err.Error())
		} else {
			d.breakers.Failure(name)
			ReportPluginFailure(name)
			d.log.Error("argus.squad.consensus_member_failed", "squad", env.squad,
				"member", name, "error", logredact.Redact(err.Error()))
		}
		// 黑板条目会渲染进其他成员的提示词（ArtifactSquadBoard）——
		// acpx 类错误内嵌 stderr 原文，必须整条脱敏（同凭证泄漏 P0 教训）。
		b.Write(name, boardKindNote, "执行失败: "+logredact.Redact(err.Error()))
		return false, nil
	}
	d.breakers.Success(name)
	st.ran = true

	// 指纹差分：只把新发现写上黑板（成员自述的重复报告在此拦下）
	added := 0
	for _, f := range res.Findings {
		fp := Fingerprint(f.File, f.Comment, f.Line)
		if env.boardFP[fp] {
			continue
		}
		env.boardFP[fp] = true
		b.Write(name, boardKindFinding, findingBoardLine(f))
		st.collected = append(st.collected, f)
		added++
	}
	d.log.Info("argus.squad.consensus_member_round", "squad", env.squad,
		"member", name, "new_findings", added, "duration_ms", dur.Milliseconds())
	return added > 0, nil
}

// fillConsensusOutcomes 收尾回填 Outcome：有发现 → Result；执行失败 → Err（Notes
// 渲染降级注明）；全程未执行 → SkippedReason；跑了但零新发现 → 空 Result（参与过，
// 计入 PluginsRun）。
func fillConsensusOutcomes(members []*consensusMember, outcomes []Outcome) {
	// 名字 → outcomes 下标（收尾回填）
	idxByName := map[string]int{}
	for i := range outcomes {
		idxByName[outcomes[i].Name] = i
	}
	for _, m := range members {
		i := idxByName[m.reg.Manifest.Name]
		o := &outcomes[i]
		switch {
		case len(m.collected) > 0:
			o.Result = &Result{Findings: m.collected}
		case m.lastErr != nil:
			// 跑过但后续轮次失败同样按失败终态回填——此前该分支静默落成
			// 空 Result（"参与了但没新发现"），失败被吞、Degraded 无注明（深挖修复）
			o.Err = m.lastErr
		case !m.ran && m.skipped != "":
			o.SkippedReason = m.skipped
		default:
			o.Result = &Result{}
		}
	}
}

// anyConsensusContribution 是否有成员真正贡献过发现（区分真收敛与全员无法贡献）。
func anyConsensusContribution(members []*consensusMember) bool {
	for _, m := range members {
		if len(m.collected) > 0 {
			return true
		}
	}
	return false
}

// squadSeed 黑板种子：任务素材摘要（变更摘要 + 文件清单，截断防黑板膨胀）。
func squadSeed(task Task, sq SquadDecl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "编队 %s 共识审查任务（%s）：%s#%s\n", sq.Name, task.Kind,
		task.PR.Owner, task.PR.Number)
	if task.Diff != nil && len(task.Diff.Files) > 0 {
		b.WriteString("变更文件：")
		paths := make([]string, 0, len(task.Diff.Files))
		for _, f := range task.Diff.Files {
			paths = append(paths, f.Path)
		}
		b.WriteString(strings.Join(paths, ", "))
		b.WriteString("\n")
	}
	if s := task.Artifacts[ArtifactChangeSummary]; s != "" {
		b.WriteString("变更摘要：\n")
		t, _ := textutil.TruncRunes(s, seedMaxRunes)
		b.WriteString(t)
	}
	return b.String()
}

// findingBoardLine 发现 → 黑板文本行（成员是 LLM，纯文本比 JSON 稳）。
func findingBoardLine(f Finding) string {
	sev := f.Severity
	if sev == "" {
		sev = "low"
	}
	line := f.Line
	if line <= 0 {
		return fmt.Sprintf("%s (%s) %s", f.File, sev, f.Comment)
	}
	return fmt.Sprintf("%s:%d (%s) %s", f.File, line, sev, f.Comment)
}

// renderBoardView 黑板条目 → 成员可见的增量视图（adapter 用 DataFence 包裹后注入）。
func renderBoardView(entries []blackboard.Entry) string {
	if len(entries) == 0 {
		return "（尚无任何发现）"
	}
	var b strings.Builder
	for _, e := range entries {
		switch e.Kind {
		case boardKindFinding, boardKindNote:
			fmt.Fprintf(&b, "- [%s] %s\n", e.Author, e.Content)
		default:
			// task 种子：成员本就持有完整任务上下文，只留一行锚点
			anchor, _, _ := strings.Cut(e.Content, "\n")
			fmt.Fprintf(&b, "- [任务] %s\n", anchor)
		}
	}
	return strings.TrimSpace(b.String())
}

// copyArtifacts 拷贝 Artifacts map（nil 安全）。
func copyArtifacts(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
