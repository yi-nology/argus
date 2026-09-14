package plugin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/ekit/observability/logx"
)

// PrintStore 指纹持久化的最小接口（由 storage.Store 实现；Merger 不 import storage，
// 避免 plugin ↔ storage 循环依赖）。
type PrintStore interface {
	// GetPrintState 返回既有指纹状态；不存在返回 (nil, nil)。
	GetPrintState(ctx context.Context, platform, owner, repo, number, fingerprint string) (*PrintState, error)
	// UpsertPrint 跨轮次合并入库；返回该指纹当前是否 suppressed（true 时本轮报告剔除该条）。
	UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
		f Finding, taskID string, at time.Time) (suppressed bool, err error)
}

// PatternState 模式信任历史读侧投影（storage.PatternSignalOut 的 plugin 侧镜像）。
type PatternState struct {
	Plugin      string
	Pattern     string
	PatternText string
	Posted      int
	Regret      int
	Suppress    int
	Restore     int
	Confidence  float64
	Samples     int
}

// PatternStore 模式级学习环读写接口（由 storage.Store 实现）。
// LearningEnabled=false 时 Merger/Runner 全部旁路，可为 nil。
type PatternStore interface {
	// ListLowConfidencePatterns 拉取本仓库低于 maxConfidence 且样本 ≥ minSamples 的模式。
	ListLowConfidencePatterns(ctx context.Context, platform, owner, repo string,
		minSamples int, maxConfidence float64, limit int) ([]PatternState, error)
	// PatternPosted 意见进入报告后记账。
	PatternPosted(ctx context.Context, platform, owner, repo, pluginName string, f Finding, at time.Time) error
}

// LearningConfig 后悔学习环消费阈值（config 派生；零值 = 全关）。
type LearningConfig struct {
	Enabled    bool
	MinSamples int
	SoftFloor  float64 // 低于此打「置信度低」标注
	HardFloor  float64 // 低于此 + low severity → 丢弃
	InjectFloor float64
	InjectTopN int
}

// DefaultLearningConfig 缺省阈值（与 design §3.7 一致）。
func DefaultLearningConfig() LearningConfig {
	return LearningConfig{
		Enabled:     false,
		MinSamples:  3,
		SoftFloor:   0.35,
		HardFloor:   0.20,
		InjectFloor: 0.35,
		InjectTopN:  10,
	}
}

// PrintState 既有指纹状态。
type PrintState struct {
	Status      string // open | suppressed
	Line        int
	Severity    string
	TimesSeen   int
	RegretCount int // 历史被 R3.5 反思剔除次数（误报历史）
}

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
	learn    LearningConfig
	log      logx.Logger
}

func NewMerger(store PrintStore, log logx.Logger) *Merger {
	return &Merger{store: store, log: log, learn: DefaultLearningConfig()}
}

// WithLearning 接入模式级后悔学习环（nil store 或 Enabled=false 时旁路）。
func (m *Merger) WithLearning(ps PatternStore, cfg LearningConfig) *Merger {
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
		idx    int
		bys    []string
		line   int
		file   string
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
	disagreed := 0
	for _, a := range recs {
		// 条目自身已聚合多专家（Merge 合并，bys≥2）= 意见一致，无分歧可言
		replicated := len(a.bys) >= 2
		for _, b := range recs {
			if a.idx == b.idx || sharesAny(a.bys, b.bys) || a.file != b.file {
				continue
			}
			if abs(a.line-b.line) <= 5 {
				replicated = true
				break
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
	for i := range mf.Findings {
		f := &mf.Findings[i]
		if strings.Contains(f.Comment, mark) {
			continue
		}
		if hit := matchLowPattern(low, *f, m.learn.MinSamples, m.learn.SoftFloor); hit != nil {
			f.Comment += mark
		}
	}
}

// Merge 执行合并。pluginNameOf 提供 finding 归属（ Outcome 遍历时注入）。
func (m *Merger) Merge(ctx context.Context, pr PRContext, taskID string, outcomes []Outcome, at time.Time) (*MergedFindings, error) {
	defer ReportLayerExec(LayerMerger)
	out := &MergedFindings{}
	type acc struct {
		finding Finding
		fp      string // 该条目的指纹（linesCompatible 分裂后各行有独立 acc）
		bys     map[string]bool
	}
	byFP := map[string]*acc{}    // fp → 首个 acc（轮内指纹去重探测）
	byNC := map[string]*acc{}    // "file|规范化comment" → 首个 acc（v3.7.0：fp v2 含行号后，
	//   同评语 ±2 行的容差合并由此通道承接——行号相容才并，保持 v1 容差语义）
	byLoc := map[string]*acc{}   // "file|line" → 该位置首个 acc（跨措辞合并用）
	var order []*acc

	// mergeInto 把后到的同类发现并入既有条目：severity 冲突取高、行号 ±2 内采纳新行号、
	// by 追加来源插件；意见文本不同且非互相包含时拼接（不同专家措辞各保留）。
	mergeInto := func(a *acc, f Finding) {
		a.finding.Severity = HigherSeverity(a.finding.Severity, f.Severity)
		if abs(a.finding.Line-f.Line) <= 2 && f.Line > 0 {
			a.finding.Line = f.Line
		}
		a.bys[f.By] = true
		if !strings.Contains(a.finding.Comment, f.Comment) && !strings.Contains(f.Comment, a.finding.Comment) {
			a.finding.Comment += "\n\n（" + f.By + " 同位置补充）" + f.Comment
		}
	}

	for _, o := range outcomes {
		if note := o.Notes(); note != "" {
			out.Degraded = append(out.Degraded, note)
		}
		if o.Result == nil {
			continue
		}
		for _, f := range o.Result.Findings {
			sev, ok := NormalizeSeverity(f.Severity)
			if !ok {
				m.log.Warn("argus.merger.severity_unknown",
					"plugin", o.Name, "raw", f.Severity, "mapped", sev)
			}
			f.Severity = sev
			f.By = o.Name
			obsFindings(sev, o.Name, 1)

			fp := Fingerprint(f.File, f.Comment, f.Line)
			if a, dup := byFP[fp]; dup && linesCompatible(a.finding.Line, f.Line) {
				// 轮内去重：severity 冲突取高；行号 ±2 内采纳新行号（匹配辅助）；
				// by 追加来源插件。
				// 行号相容闸门（收尾批修复）：指纹不含行号，同文件两处不同位置的
				// 同模板意见（OCR/规则型插件常见）此前被误并为一条、第二条永久
				// 丢失——行号不相容时保留为独立条目。
				mergeInto(a, f)
				continue
			}
			// 同文件 + 规范化评语相同 + 行号相容 → 同一问题（v1 容差语义的承接）
			ncKey := f.File + "|" + severity.NormalizeComment(f.Comment)
			if a, dup := byNC[ncKey]; dup && linesCompatible(a.finding.Line, f.Line) {
				mergeInto(a, f)
				continue
			}
			a2 := &acc{finding: f, fp: fp, bys: map[string]bool{f.By: true}}
			// 位置合并兜底：不同专家对同一 file:line 用不同措辞描述时（指纹必不同），
			// 按位置归并为一条——「不同专家发现同一问题只报一次」的确定性兜底；
			// 不同行号不并（保守，两个真正不同的问题常落在相邻行）。
			// 注：曾评估加"语义相似才并"的闸门，与 v3.5.x 拍板的归并语义
			//（merger_locmerge_test 固化：同位置不同措辞视为同一问题）冲突，不采用。
			// 措辞各保留在合并文本里，人工可见双方视角。
			if f.Line > 0 {
				loc := f.File + "|" + strconv.Itoa(f.Line)
				if a, seen := byLoc[loc]; seen {
					mergeInto(a, f)
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

	// 跨轮次入库与抑制过滤
	// 学习环：一次性拉本仓库低置信模式（HardFloor），供丢弃与标注共用
	var lowPatterns []PatternState
	if m.learn.Enabled && m.patterns != nil {
		ps, err := m.patterns.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
			m.learn.MinSamples, m.learn.SoftFloor, 0)
		if err != nil {
			m.log.Warn("argus.merger.pattern_list_failed", "error", err.Error())
		} else {
			lowPatterns = ps
		}
	}
	for _, a := range order {
		f := a.finding
		if len(a.bys) > 1 { // 多插件报了同一问题：by 合并展示
			var names []string
			for n := range a.bys {
				names = append(names, n)
			}
			sort.Strings(names)
			f.By = joinNames(names)
		}
		// 跨轮历史查询（GetPrintState 此前只有接口与实现、无调用方——接线于此）：
		// 复现次数与 regret（被 R3.5 反思剔除次数）作为可信度信号。
		var prior *PrintState
		if m.store != nil {
			if ps, err := m.store.GetPrintState(ctx,
				pr.Platform, pr.Owner, pr.Repo, pr.Number, a.fp); err != nil {
				m.log.Warn("argus.merger.print_state_failed", "error", err.Error())
			} else {
				prior = ps
			}
		}
		if m.store != nil {
			suppressed, err := m.store.UpsertPrint(ctx,
				pr.Platform, pr.Owner, pr.Repo, pr.Number, a.fp, f, taskID, at)
			if err != nil {
				m.log.Error("argus.merger.print_upsert_failed", "error", err.Error())
			} else if suppressed {
				out.Suppressed++
				continue
			}
		}
		// 学习环硬闸门：样本充足且置信度低于 HardFloor 的 low 条目直接丢弃。
		// medium/high 不丢——误杀代价不对称（design D4）。
		if m.learn.Enabled && f.Severity == SeverityLow {
			if hit := matchLowPattern(lowPatterns, f, m.learn.MinSamples, m.learn.HardFloor); hit != nil {
				out.Degraded = append(out.Degraded, fmt.Sprintf(
					"学习环抑制：%s/%s（模式置信度 %.2f）", hit.Plugin, truncateText(hit.PatternText, 40), hit.Confidence))
				// 记 posted 以外：被闸门吃掉不记 posted，避免抬高置信度
				continue
			}
		}
		// 展示标注（注释在入库之后改写，不污染指纹与 prints 表存储的原文）：
		// 复现 = 同一问题跨轮再现；高 regret = 历史上多次被终审判为误报嫌疑。
		if prior != nil {
			if prior.TimesSeen >= 2 {
				f.Comment = fmt.Sprintf("%s（跨轮复现 %d 次）", f.Comment, prior.TimesSeen+1)
			}
			if prior.RegretCount >= 2 {
				f.Comment += fmt.Sprintf("（历史被 R3.5 反思剔除 %d 次，可信度低）", prior.RegretCount)
			}
		}
		// 模式级低置信标注（SoftFloor）：medium/high 也标注，人审优先看。
		if m.learn.Enabled {
			if hit := matchLowPattern(lowPatterns, f, m.learn.MinSamples, m.learn.SoftFloor); hit != nil {
				f.Comment += "（本仓库同类意见历史置信度低）"
			}
		}
		// PatternPosted：意见进入报告后记账（失败只告警）
		if m.learn.Enabled && m.patterns != nil {
			by := f.By
			if i := strings.IndexByte(by, ','); i > 0 {
				by = by[:i] // 多插件合并时取首个归属记账，避免一票多记
			}
			if err := m.patterns.PatternPosted(ctx, pr.Platform, pr.Owner, pr.Repo, by, f, at); err != nil {
				m.log.Warn("argus.merger.pattern_posted_failed", "error", err.Error())
			}
		}
		out.Findings = append(out.Findings, f)
	}

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

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// matchLowPattern 用 finding 的 plugin+comment 键在低置信列表中找匹配。
// samples < minSamples 或 confidence ≥ floor 时返回 nil（小样本/未够低不消费）。
func matchLowPattern(low []PatternState, f Finding, minSamples int, floor float64) *PatternState {
	by := f.By
	if i := strings.IndexByte(by, ','); i > 0 {
		by = by[:i]
	}
	key := PatternKey(by, f.Comment)
	for i := range low {
		if low[i].Pattern == key && low[i].Samples >= minSamples && low[i].Confidence < floor {
			return &low[i]
		}
	}
	return nil
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		out := names[0]
		for _, n := range names[1:] {
			out += "," + n
		}
		return out
	}
}
