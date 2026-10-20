package plugin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"
	"github.com/oekit/logx"

	"argus/internal/domain"
	"argus/internal/learning"
)

// PrintStore / PatternStore 持久化契约住在 internal/domain（storage 实现、Merger 消费），
// 此处别名保持既有 API，避免 39 个消费方一次性改引用。
type PrintStore = domain.PrintStore
type PatternStore = domain.PatternStore
type PrintState = domain.PrintState

// MergedFindings Merger 产出。
type MergedFindings struct {
	Findings    []Finding // 归一化 + 轮内去重 + 抑制剔除后，severity 降序
	Suppressed  int       // 因误报反馈被抑制的条数（报告脚注提示）
	Degraded    []string  // 降级注明（插件失败/超时/裁剪）
	CrossReview string    // 交叉审查编队注记（"claude+kimi，分歧 1 处"；空 = 无编队）
}

// Merger 合并器（design §3.3，全部确定性）：
//  1. severity 入口归一化（越界归 low + 告警）；
//  2. 轮内精确指纹去重（规范化评论文本哈希 + file；冲突 severity 取高）；
//  3. 跨轮次指纹入库/更新，suppressed 的不进报告；
//  4. 插件失败/跳过 → 降级注明，不拖垮整体；
//  5. 学习环开启时：低置信模式标注 + 仅 low 条目按 HardFloor 丢弃。
type Merger struct {
	store    PrintStore
	patterns PatternStore
	learn    learning.Config
	log      logx.Logger
}

func NewMerger(store PrintStore, log logx.Logger) *Merger {
	return &Merger{store: store, log: log, learn: learning.DefaultConfig()}
}

// WithLearning 接入模式级后悔学习环（nil store 或 Enabled=false 时旁路）。
func (m *Merger) WithLearning(ps PatternStore, cfg learning.Config) *Merger {
	m.patterns = ps
	m.learn = cfg
	return m
}

// AnnotateCrossReview 交叉审查分歧标注（v3.3）：编队成员（≥2）对同一文件
// ±5 行内各自报告了不同指纹的意见时，给"仅一方报告"的意见追加分歧标注——
// 人审时优先看专家意见不一致的区域。幂等：重复调用不叠加标注。
func (m *Merger) AnnotateCrossReview(mf *MergedFindings, members []string) {
	if mf == nil || len(members) < 2 {
		return
	}
	memberSet := map[string]bool{}
	for _, n := range members {
		memberSet[n] = true
	}
	m.annotateCross(mf, memberSet)
}

// linesCompatible 指纹同但位置不相容（同文件两处不同行、且相差 >2）时，
// 轮内按指纹合并会把第二处问题吞掉——视为不同条目。任一方无行号（0）按相容处理。
func linesCompatible(a, b int) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	return abs(a-b) <= 2
}

// annotateCross 实际标注（AnnotateCrossReview 的实现体，独立便于测试）。
func (m *Merger) annotateCross(mf *MergedFindings, memberSet map[string]bool) {
	const crossMark = "⚠️ 交叉审查分歧"
	// 先剥离旧标注（幂等）
	for i := range mf.Findings {
		f := &mf.Findings[i]
		if idx := strings.Index(f.Comment, "（"+crossMark); idx >= 0 {
			f.Comment = strings.TrimSpace(f.Comment[:idx])
		}
	}
	type rec struct {
		idx  int
		bys  []string
		line int
		file string
	}
	// byMembers 拆分 By：Merge 阶段多插件命中同题会把 By 拼成 "a,b"——不拆开
	// 的话合并条目（恰是专家们意见一致的证据）整体不进 recs，反而被误标
	// 「编队另一专家未复现」（深挖修复：标注方向此前反了）。
	byMembers := func(by string) []string {
		var out []string
		for _, n := range strings.Split(by, ",") {
			if n = strings.TrimSpace(n); memberSet[n] {
				out = append(out, n)
			}
		}
		return out
	}
	sharesAny := func(a, b []string) bool {
		for _, x := range a {
			for _, y := range b {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	var recs []rec
	for i, f := range mf.Findings {
		bys := byMembers(f.By)
		if len(bys) == 0 {
			continue
		}
		recs = append(recs, rec{idx: i, bys: bys, line: f.Line, file: f.File})
	}
	if len(recs) < 2 {
		return
	}
	// 按文件分桶：分歧判定只在同文件内两两比对，免全量 O(n²)（大编队多意见时）。
	byFile := map[string][]rec{}
	for _, r := range recs {
		byFile[r.file] = append(byFile[r.file], r)
	}
	disagreed := 0
	for _, a := range recs {
		// 条目自身已聚合多专家（Merge 合并，bys≥2）= 意见一致，无分歧可言
		replicated := len(a.bys) >= 2
		if !replicated {
			for _, b := range byFile[a.file] {
				if a.idx == b.idx || sharesAny(a.bys, b.bys) {
					continue
				}
				if abs(a.line-b.line) <= 5 {
					replicated = true
					break
				}
			}
		}
		if !replicated {
			f := &mf.Findings[a.idx]
			f.Comment += fmt.Sprintf("（%s：编队另一专家未复现此问题，请优先人工复核）", crossMark)
			disagreed++
		}
	}
	if disagreed > 0 {
		var names []string
		for n := range memberSet {
			names = append(names, n)
		}
		sort.Strings(names)
		mf.CrossReview = fmt.Sprintf("%s 编队交叉审查，分歧 %d 处已标注", strings.Join(names, "+"), disagreed)
	}
}

// AnnotateLowConfidence 在 R3.5 之后重放学习环低置信标注。
// 原因：R3.5 按 id 回写 comment 时会用模型修订稿覆盖 Merger 阶段追加的后缀，
// 「历史置信度低」提示在终审后丢失。幂等：已含标注则跳过。
func (m *Merger) AnnotateLowConfidence(ctx context.Context, pr PRContext, mf *MergedFindings) {
	if mf == nil || !m.learn.Enabled || m.patterns == nil {
		return
	}
	low, err := m.patterns.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
		m.learn.MinSamples, m.learn.SoftFloor, 0)
	if err != nil || len(low) == 0 {
		return
	}
	const mark = "（本仓库同类意见历史置信度低）"
	lowIdx := patternIndex(low, m.learn.MinSamples)
	for i := range mf.Findings {
		f := &mf.Findings[i]
		if strings.Contains(f.Comment, mark) {
			continue
		}
		pkey := learning.PatternKey(learning.FirstBy(f.By), f.Comment)
		if hit := lookupLow(lowIdx, pkey, m.learn.SoftFloor); hit != nil {
			f.Comment += mark
		}
	}
}

// mergeAcc 轮内归并 accumulator：同一问题多插件/多措辞命中时共享一个 acc。
type mergeAcc struct {
	finding Finding
	fp      string // 该条目的指纹（linesCompatible 分裂后各行有独立 acc）
	bys     map[string]bool
	byOrder []string // 来源插件按合并发生序（首见者=评语作者，学习环记账键依赖此序）
}

// mergePending 待入库条目及其入库结果（跨轮历史/抑制判定在此回填）。
type mergePending struct {
	f   Finding
	res domain.PrintUpsertResult
}

// mergeInto 把后到的同类发现并入既有条目：severity 冲突取高、行号 ±2 内采纳新行号、
// by 追加来源插件；意见文本不同且非互相包含时拼接（不同专家措辞各保留）。
func (a *mergeAcc) mergeInto(f Finding) {
	a.finding.Severity = HigherSeverity(a.finding.Severity, f.Severity)
	if abs(a.finding.Line-f.Line) <= 2 && f.Line > 0 {
		a.finding.Line = f.Line
	}
	// best-of-N 采样复现计数随条目透传（多插件命中同题时保留首个非空，
	// 数据在结构化字段——入 Comment 会污染指纹/模式键，v3.10.0）
	if a.finding.Samples == "" {
		a.finding.Samples = f.Samples
	}
	if !a.bys[f.By] { // 首次出现的插件才进 byOrder（与 bys 去重语义一致）
		a.byOrder = append(a.byOrder, f.By)
	}
	a.bys[f.By] = true
	if !strings.Contains(a.finding.Comment, f.Comment) && !strings.Contains(f.Comment, a.finding.Comment) {
		a.finding.Comment += "\n\n（" + f.By + " 同位置补充）" + f.Comment
	}
}

// mergeInRound 轮内归并：三通道探测（指纹 / file+规范化评语 / file+line），
// 命中既有条目即并入，否则新建。返回按首见序排列的 acc 清单，同时把插件
// 失败/降级注记写进 out.Degraded。
func (m *Merger) mergeInRound(outcomes []Outcome, out *MergedFindings) []*mergeAcc {
	byFP := map[string]*mergeAcc{} // fp → 首个 acc（轮内指纹去重探测）
	byNC := map[string]*mergeAcc{} // "file|规范化comment" → 首个 acc（v3.7.0：fp v2 含行号后，
	//   同评语 ±2 行的容差合并由此通道承接——行号相容才并，保持 v1 容差语义）
	byLoc := map[string]*mergeAcc{} // "file|line" → 该位置首个 acc（跨措辞合并用）
	var order []*mergeAcc

	for _, o := range outcomes {
		if note := o.Notes(); note != "" {
			out.Degraded = append(out.Degraded, note)
		}
		if o.Result == nil {
			continue
		}
		// 插件自报的审查降级（如 builtin ReAct 失败落回单轮）透出为 Degraded——
		// 此前同一 "by xxx" 标签掩盖两种审查强度，报告消费者无法区分（v3.8.0）。
		for _, line := range strings.Split(o.Result.Notes, "\n") {
			if strings.Contains(line, DegradationMarker) {
				out.Degraded = append(out.Degraded, o.Name+"："+strings.TrimSpace(line))
			}
		}
		for _, f := range o.Result.Findings {
			sev, ok := NormalizeSeverity(f.Severity)
			if !ok {
				m.log.Warn("argus.merger.severity_unknown",
					"plugin", o.Name, "raw", f.Severity, "mapped", sev)
				// 词表外严重度整批落 low 是"外部插件词表失配"的信号——此前只有
				// 日志，报告零痕迹（v3.8.0 补 Degraded 注记）
				out.Degraded = append(out.Degraded,
					fmt.Sprintf("%s 报告了词表外严重度 %q（已按 %s 处理）", o.Name, f.Severity, sev))
			}
			f.Severity = sev
			f.By = o.Name
			obsFindings(sev, o.Name, 1)

			// 规范指纹写定于此（原始 comment/行号，先于任何标注与 R3.5 修订），
			// 之后只读贯穿到渲染/反馈层——身份唯一化（v3.8.0，勿在下游重算）。
			f.FP = Fingerprint(f.File, f.Comment, f.Line)
			fp := f.FP
			if a, dup := byFP[fp]; dup && linesCompatible(a.finding.Line, f.Line) {
				// 轮内去重：severity 冲突取高；行号 ±2 内采纳新行号（匹配辅助）；
				// by 追加来源插件。
				// 行号相容闸门（收尾批修复）：指纹不含行号，同文件两处不同位置的
				// 同模板意见（OCR/规则型插件常见）此前被误并为一条、第二条永久
				// 丢失——行号不相容时保留为独立条目。
				a.mergeInto(f)
				continue
			}
			// 同文件 + 规范化评语相同 + 行号相容 → 同一问题（v1 容差语义的承接）
			ncKey := f.File + "|" + reportutil.NormalizeComment(f.Comment)
			if a, dup := byNC[ncKey]; dup && linesCompatible(a.finding.Line, f.Line) {
				a.mergeInto(f)
				continue
			}
			a2 := &mergeAcc{finding: f, fp: fp, bys: map[string]bool{f.By: true}, byOrder: []string{f.By}}
			// 位置合并兜底：不同专家对同一 file:line 用不同措辞描述时（指纹必不同），
			// 按位置归并为一条——「不同专家发现同一问题只报一次」的确定性兜底；
			// 不同行号不并（保守，两个真正不同的问题常落在相邻行）。
			// 注：曾评估加"语义相似才并"的闸门，与 v3.5.x 拍板的归并语义
			//（merger_locmerge_test 固化：同位置不同措辞视为同一问题）冲突，不采用。
			// 措辞各保留在合并文本里，人工可见双方视角。
			if f.Line > 0 {
				loc := f.File + "|" + strconv.Itoa(f.Line)
				if a, seen := byLoc[loc]; seen {
					a.mergeInto(f)
					continue
				}
				byLoc[loc] = a2
			}
			if _, seen := byFP[fp]; !seen {
				byFP[fp] = a2
			}
			if _, seen := byNC[ncKey]; !seen {
				byNC[ncKey] = a2
			}
			order = append(order, a2)
		}
	}
	return order
}

// persistPrints 先归一化 By 并批量入库：整批共用一次 writeMu（SQLite 多意见 PR
// 显著降锁竞争），返回逐条 Prior/suppressed 待消费清单。
func (m *Merger) persistPrints(ctx context.Context, pr PRContext, taskID string, at time.Time,
	order []*mergeAcc) []mergePending {
	pendings := make([]mergePending, 0, len(order))
	batch := make([]domain.PrintUpsert, 0, len(order))
	for _, a := range order {
		f := a.finding
		if len(a.bys) > 1 { // 多插件报了同一问题：by 按合并发生序拼接展示
			// v3.8.0：不再 sort.Strings——展示序决定记账键（PatternPosted/matchLowPattern
			// 取 By 首段=评语作者）。字母序会让 a-max 持续消费 z-review 写的评语的
			// 学习历史（误报模式跨插件串门）。
			f.By = strings.Join(a.byOrder, ",")
		}
		batch = append(batch, domain.PrintUpsert{FP: a.fp, Finding: f})
		pendings = append(pendings, mergePending{f: f})
	}
	if m.store != nil && len(batch) > 0 {
		results := m.store.UpsertPrints(ctx,
			pr.Platform, pr.Owner, pr.Repo, pr.Number, taskID, at, batch)
		for i := range pendings {
			if i < len(results) {
				pendings[i].res = results[i]
			}
		}
	}
	return pendings
}

// applyLearning 学习环闸门与标注（顺序不变量：HardFloor 丢弃 → PatternPosted 记账
// → SoftFloor 标注 → 展示标注，错账史见 v3.9.0/v3.9.1）。
func (m *Merger) applyLearning(ctx context.Context, pr PRContext, at time.Time,
	out *MergedFindings, pendings []mergePending) {
	// 学习环：一次性拉本仓库低置信模式（HardFloor），供丢弃与标注共用；
	// 建 key 索引后循环内 O(1) 查表（原 matchLowPattern 为 O(n·m) 线扫 + 每条
	// finding 重算 PatternKey，意见多的 PR 在热路径上）。
	var lowIdx map[string]*learning.PatternState
	if m.learn.Enabled && m.patterns != nil {
		ps, err := m.patterns.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
			m.learn.MinSamples, m.learn.SoftFloor, 0)
		if err != nil {
			m.log.Warn("argus.merger.pattern_list_failed", "error", err.Error())
		} else {
			lowIdx = patternIndex(ps, m.learn.MinSamples)
		}
	}
	for _, p := range pendings {
		f, res := p.f, p.res
		if res.Err != nil {
			m.log.Error("argus.merger.print_upsert_failed", "error", res.Err.Error())
		} else if res.Suppressed {
			out.Suppressed++
			continue
		}
		// 跨轮历史（批量入库返回的 Prior）：复现次数与 regret 作可信度信号。
		prior := res.Prior
		// 学习环记账/匹配的权威评语（v3.9.1）：跨轮既有条目以 prints 行原文为
		// 准——轮内合并拼接（"（X 同位置补充）…"）发生在 acc.finding 上，而条目
		// 指纹=首见评语的指纹，内存文本会偏离历史模式行的键；新条目用本轮文本
		//（UpsertPrint 首建即存该文本，regret 回查同源，两键天然一致）。
		patternComment := f.Comment
		if prior != nil && prior.Comment != "" {
			patternComment = prior.Comment
		}
		probe := f
		probe.Comment = patternComment
		// PatternKey 整轮只算一次（规范化+哈希是热路径），Hard/Soft 两闸共用。
		var pkey string
		if m.learn.Enabled {
			pkey = learning.PatternKey(learning.FirstBy(f.By), patternComment)
		}

		// 学习环硬闸门：样本充足且置信度低于 HardFloor 的 low 条目直接丢弃。
		// medium/high 不丢——误杀代价不对称（design D4）。
		if m.learn.Enabled && f.Severity == SeverityLow {
			if hit := lookupLow(lowIdx, pkey, m.learn.HardFloor); hit != nil {
				out.Degraded = append(out.Degraded, fmt.Sprintf(
					"学习环抑制：%s/%s（模式置信度 %.2f）", hit.Plugin, textutil.TruncEllipsis(hit.PatternText, 40), hit.Confidence))
				// 记 posted 以外：被闸门吃掉不记 posted，避免抬高置信度
				continue
			}
		}
		// PatternPosted：意见进入报告后记账（失败只告警）。必须先于 SoftFloor/
		// 展示两类标注——PatternKey 由评语派生（pattern.go），标注文本进键会把
		// posted 记进"原文+标注"的影子行，真实模式行只累积 regret，置信度单调
		// 下跌后 HardFloor 永久吞掉本应正确的条目（v3.9.0 修复只挪过了展示标注，
		// v3.9.1 补齐 SoftFloor 标注先于记账的漏网层）。
		if m.learn.Enabled && m.patterns != nil {
			if err := m.patterns.PatternPosted(ctx, pr.Platform, pr.Owner, pr.Repo, learning.FirstBy(f.By), probe, at); err != nil {
				m.log.Warn("argus.merger.pattern_posted_failed", "error", err.Error())
			}
		}
		// 模式级低置信标注（SoftFloor）：medium/high 也标注，人审优先看。
		// 匹配同样基于权威评语 probe；标注恒在记账/匹配之后（错账见上，
		// v3.9.0 审查修复确立的不变量）。
		if m.learn.Enabled {
			if hit := lookupLow(lowIdx, pkey, m.learn.SoftFloor); hit != nil {
				f.Comment += "（本仓库同类意见历史置信度低）"
			}
		}
		// 展示标注（注释在入库与模式记账之后改写，不污染指纹/prints 原文/
		// 模式键）：复现 = 同一问题跨轮再现；高 regret = 历史上多次被终审判为
		// 误报嫌疑。所以必须放在本循环最后。
		if prior != nil {
			if prior.TimesSeen >= 2 {
				f.Comment = fmt.Sprintf("%s（跨轮复现 %d 次）", f.Comment, prior.TimesSeen+1)
			}
			if prior.RegretCount >= 2 {
				f.Comment += fmt.Sprintf("（历史被 R3.5 反思剔除 %d 次，可信度低）", prior.RegretCount)
			}
		}
		out.Findings = append(out.Findings, f)
	}
}

// Merge 执行合并。pluginNameOf 提供 finding 归属（ Outcome 遍历时注入）。
func (m *Merger) Merge(ctx context.Context, pr PRContext, taskID string, outcomes []Outcome, at time.Time) (*MergedFindings, error) {
	defer ReportLayerExec(LayerMerger)
	out := &MergedFindings{}
	order := m.mergeInRound(outcomes, out)
	pendings := m.persistPrints(ctx, pr, taskID, at, order)
	m.applyLearning(ctx, pr, at, out, pendings)

	sort.SliceStable(out.Findings, func(i, j int) bool {
		if SeverityRank(out.Findings[i].Severity) != SeverityRank(out.Findings[j].Severity) {
			return SeverityRank(out.Findings[i].Severity) > SeverityRank(out.Findings[j].Severity)
		}
		if out.Findings[i].File != out.Findings[j].File {
			return out.Findings[i].File < out.Findings[j].File
		}
		return out.Findings[i].Line < out.Findings[j].Line
	})
	return out, nil
}

func abs(n int) int { return max(n, -n) }

// patternIndex 把低置信模式列表建成 key → *PatternState 索引（仅保留够样本的项）。
func patternIndex(low []learning.PatternState, minSamples int) map[string]*learning.PatternState {
	if len(low) == 0 {
		return nil
	}
	idx := make(map[string]*learning.PatternState, len(low))
	for i := range low {
		if low[i].Samples >= minSamples {
			idx[low[i].Pattern] = &low[i]
		}
	}
	return idx
}

// lookupLow 按 Pattern 键查低置信索引；confidence ≥ floor 时返回 nil（未够低不消费）。
func lookupLow(idx map[string]*learning.PatternState, key string, floor float64) *learning.PatternState {
	if idx == nil || key == "" {
		return nil
	}
	if hit := idx[key]; hit != nil && hit.Confidence < floor {
		return hit
	}
	return nil
}
