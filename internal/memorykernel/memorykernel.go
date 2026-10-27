// Package memorykernel agent-memory 内核适配层（v3.151.0，ARGUS_MEMORY_KERNEL=1 opt-in；
// v5.0.0 转正：旧文件面 internal/memory 已退役，本层是记忆唯一路径）。
//
// 内核收益（agent-memory v0.4.2+）：
//   - 落库前强制脱敏：capture 管道 Redactor 为必需依赖，凭据形态兵底 +
//     guardx/logredact 规则表并集，掩码定长不泄露原值长度；
//   - 注入防伪：召回结果经 MemoryBand 转义全部「<」——跨会话留存内容是
//     不可信面，旧文件面把索引原文拼进 R3 prompt，伪造闭合标签可穿透；
//   - 证据链闭合：AtomCard.QuoteEventID 必填（不变量：事实必须有逐字引用出处），
//     dream 事实先落 capture 事件再落事实卡，审计链可从事实回溯到事件；
//   - 生命周期：原始事件按龄回收（DeleteRawBefore），清桶走软退役
//     （DeprecateAtom），召回立即不再命中而数据仍可审计。
//
// 确定性哲学保留：dream 的确定性信号（regret/suppress 计数）走「显式手工写入」
// 路径（Explicit=true + EventManual/OriginOperator），不引入 LLM 提炼；
// 无模型时内核照常工作（提炼面才报 ErrNoLLM，本适配层不触提炼面）。
package memorykernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/agent-memory/lifecycle"
	"github.com/yi-nology/agent-memory/pipeline/capture"
	qanalyze "github.com/yi-nology/agent-memory/pipeline/query"
	"github.com/yi-nology/agent-memory/pipeline/recall"
	"github.com/yi-nology/agent-memory/ports"
	"github.com/yi-nology/agent-memory/storage/sqlite"
	"github.com/yi-nology/agent-memory/store"
	"github.com/yi-nology/agent-memory/types"
)

// IndexBudgetRunes 记忆索引注入的 rune 预算缺省（超限确定性降级：截断+尾注）。
const IndexBudgetRunes = 2000

// Ref 记忆归属仓库（Platform/Owner/Repo 三段）。
type Ref struct {
	Platform, Owner, Repo string
}

// orgDirName 组织级记忆桶标识：跨仓共享的模式/教训（dream 周期聚合）。
const orgDirName = "_org"

// OrgRef 组织桶归属（Owner=orgDirName 由本常量统一，Repo 恒空）。
var OrgRef = Ref{Owner: orgDirName}

// SingleLine 压平换行（描述进单行的消毒件；dream 侧共用单源）。
func SingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// Kernel agent-memory 内核持有面（存储 + 捕获 + 召回 + 可选语义轨）。
type Kernel struct {
	store *sqlite.Store
	cap   *capture.Capturer
	// emb 语义轨向量化器（nil = 只走词面召回）。sqlite.Store 自带持久向量索引
	//（atom_vectors 表，ports.VectorIndex 断言在库内核实），注入 Embedder 即接通
	// recall 语义路（RRF 与词面融合）。rag 批量口的适配在 bootstrap 装配侧做，
	// 本层保持 L1 纯净不引 rag。
	emb ports.Embedder
	log logx.Logger
}

// Open 打开（必要时创建）内核库并装配捕获/召回面。path 为 sqlite 文件路径；
// 父目录自动创建（sqlite.OpenFile 要求父目录已存在——215 实弹暴露 kernel.db
// 建在新目录下时 "unable to open database file"，目录创建归本层，调用方不背）。
// emb 非 nil 时接通语义召回轨（embedding 端点未配置传 nil = 词面照旧，零行为变化）。
// capture.New 缺 Redactor 直接失败（内核纪律：记忆是留存资产，脱敏必需）；
// recall 注入面 MemoryBand 必需（防伪包裹不缺省）。
func Open(path string, emb ports.Embedder, log logx.Logger) (*Kernel, error) {
	if path == "" {
		return nil, fmt.Errorf("memorykernel: 库路径不可空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("memorykernel: 建库目录: %w", err)
	}
	st, err := sqlite.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("memorykernel: 打开库: %w", err)
	}
	k, err := openWithStore(st, emb, log)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	return k, nil
}

// openWithStore 从既有存储装配捕获/召回面（Open 与测试共用装配单源）。
func openWithStore(st *sqlite.Store, emb ports.Embedder, log logx.Logger) (*Kernel, error) {
	cap, err := capture.New(st, ports.PatternRedactor{})
	if err != nil {
		return nil, fmt.Errorf("memorykernel: 装配捕获面: %w", err)
	}
	// 召回面不在此预建（v3.178.0 死代码清理）：recallOne 按预算逐次建 pipeline，
	// 预建实例只存不读；recall.New 的配置校验（分桶归一）对常量配置不可能失败，
	// 首次召回的失败也已在 recallOne 走 Warn 降级。
	return &Kernel{store: st, cap: cap, emb: emb, log: log}, nil
}

// Close 关闭底层库。
func (k *Kernel) Close() error {
	if k == nil || k.store == nil {
		return nil
	}
	return k.store.Close()
}

// NamespaceOf 仓库归属 → 记忆库命名空间（platform-owner-repo 三段相连；
// 组织桶固定 _org）。平台/owner/repo 段在平台侧已不含路径分隔符
//（config 装载期校验），消毒规则与旧文件面目录命名同形。
func NamespaceOf(r Ref) store.Namespace {
	if r.Owner == OrgRef.Owner && r.Repo == "" {
		return store.Namespace(orgDirName)
	}
	return store.Namespace(fmt.Sprintf("%s-%s-%s", r.Platform, r.Owner, r.Repo))
}

// OrgNamespace 组织桶命名空间（跨仓共享模式/教训）。
const OrgNamespace = store.Namespace("_org")

// SaveFact dream 确定性事实写入：先经 capture 落显式事件（落库前打码在此发生），
// 再以该事件为证据落事实卡（QuoteEventID 闭合审计链）。slug 即事实卡 ID，
// 同 slug 覆写是 upsert（dream 周期重导的幂等语义）。
func (k *Kernel) SaveFact(ctx context.Context, ns store.Namespace, slug, factType, description, body string, at time.Time) error {
	if k == nil {
		return nil
	}
	out, err := k.cap.Capture(ctx, capture.Input{
		Namespace: ns,
		EventID:   fmt.Sprintf("argus-dream/%s/%d", slug, at.UnixNano()),
		Host:      "argus",
		SessionID: "dream",
		Type:      types.EventManual,
		Content:   description + "\n" + body,
		Explicit:  true, // dream 明确说「记下这条」——不受自动捕获判据约束
		Provenance: types.Provenance{
			Origin: types.OriginOperator,
			Host:   "argus",
		},
		At: at,
	})
	if err != nil {
		return fmt.Errorf("memorykernel: 捕获事实事件: %w", err)
	}
	if !out.Accepted {
		return fmt.Errorf("memorykernel: 事实事件被拒收: %s", out.SkipReason)
	}
	// 事实卡正文 = 脱敏后事件正文（capture 已处置，此处不二次打码——
	// 双跑打码是幂等的，但审计位只能挂一次，挂在事件侧）。
	card := &types.AtomCard{
		ID:           slug,
		Assertion:    out.Event.Content,
		QuoteEventID: out.Event.ID,
		SearchTerms:  dreamSearchTerms(factType, description),
		Confidence:   types.ConfidenceMed,
		Importance:   types.ImportanceMed,
		// 时效类别 = lesson（v3.177.0 memory v0.8 FactKind）：dream 模式是
		// 长期经验（「命中类似表述请提高举证标准」）——不因时间失效、
		// 不被事件召回窗口挡掉，只在被推翻时失效。
		Kind:       types.FactLesson,
		OccurredAt: at,
		CreatedAt:  at,
		UpdatedAt:  at,
		Provenance: out.Event.Provenance,
	}
	if err := k.store.SaveAtom(ctx, ns, card); err != nil {
		return fmt.Errorf("memorykernel: 落事实卡: %w", err)
	}
	return nil
}

// ListAtoms 命名空间事实卡清单（运维/测试取上面，不进召回热路径）。nil 安全。
func (k *Kernel) ListAtoms(ctx context.Context, ns store.Namespace) ([]*types.AtomCard, error) {
	if k == nil {
		return nil, nil
	}
	return k.store.ListAtoms(ctx, ns, store.AtomFilter{})
}

// ClearRepo 清桶：软退役全部事实卡 + 清原始事件。两段都要做——召回面同时吃
// atom 与 raw 两路来源，只退役事实卡会让同一份正文从 raw 引用桶漏回来
// （测试 TestClearRepoRemovesFromRecall 钉住）。事实卡走软退役（留审计），
// 原始事件走按龄删除（DeleteRawBefore 至 now——它们的价值锚在事实卡证据链上，
// 卡退役后事件只剩召回噪声）。
func (k *Kernel) ClearRepo(ctx context.Context, ns store.Namespace) error {
	if k == nil {
		return nil
	}
	atoms, err := k.store.ListAtoms(ctx, ns, store.AtomFilter{})
	if err != nil {
		return fmt.Errorf("memorykernel: 列事实卡: %w", err)
	}
	now := time.Now().UTC()
	for _, a := range atoms {
		if !a.DeprecatedAt.IsZero() || a.SupersededBy != "" {
			continue
		}
		if err := k.store.DeprecateAtom(ctx, ns, a.ID, now); err != nil {
			return fmt.Errorf("memorykernel: 退役事实卡 %s: %w", a.ID, err)
		}
	}
	if _, err := k.store.DeleteRawBefore(ctx, ns, now.Add(time.Second)); err != nil {
		return fmt.Errorf("memorykernel: 清原始事件: %w", err)
	}
	return nil
}

// GC 原始事件按龄回收（lifecycle.GC 委派；顺带退役证据全没的事实卡）。
// 命名空间清单由调用方供给（dream 写侧域：_org + ListPatternRepos 派生，
// 与 dream 供给面同源——内核不攒「dream 不管的库」）。ns 用 string 形态
// （OrgNamespace/NamespaceOf 转字符串），不向调用方泄漏内核类型。
func (k *Kernel) GC(ctx context.Context, ns string, before time.Time) (int, error) {
	if k == nil {
		return 0, nil
	}
	res, err := lifecycle.GC(ctx, k.store, store.Namespace(ns), before)
	if err != nil {
		return 0, fmt.Errorf("memorykernel: GC %s: %w", ns, err)
	}
	return res.RawDeleted, nil
}

// Vacuum 物理整理（sqlite VACUUM；低频，建议周级）。
func (k *Kernel) Vacuum(ctx context.Context) error {
	if k == nil {
		return nil
	}
	return lifecycle.Vacuum(ctx, k.store)
}

// RecallIndex R3 注入面：组织桶 + 仓桶两级召回，预算分工与旧面
// LoadIndexWithOrg 一致（组织段上限 40%，防跨仓噪声挤占本仓记忆）。
// query 为空时走高价值兜底取数（召回两路：查询命中 ∪ 高价值）——
// 评审场景没有「用户提问」，查询词缺省为空串是合法形态。
// sessionID 防回声：本次任务自己刚写入的事件不进注入面。
// 返回串已经 MemoryBand 包裹（内核侧 Render 的 Snippet 正文在 gather 即转义）。
func (k *Kernel) RecallIndex(ctx context.Context, repoNS, orgNS store.Namespace, sessionID, query string, budgetRunes int) string {
	if k == nil {
		return ""
	}
	if budgetRunes <= 0 {
		budgetRunes = IndexBudgetRunes
	}
	// rune 预算 → token 预算（StaticEstimator 口径约 2 rune/token，按半折算取整）。
	budgetTokens := budgetRunes / 2
	var orgText string
	if orgNS != "" {
		orgText = k.recallOne(ctx, orgNS, sessionID, query, budgetTokens*2/5)
	}
	repoText := k.recallOne(ctx, repoNS, sessionID, query, budgetTokens-budgetTokens*2/5)
	switch {
	case orgText != "" && repoText != "":
		return "## 跨仓常见模式（组织级）\n" + orgText + "\n## 本仓记忆\n" + repoText
	case orgText != "":
		return "## 跨仓常见模式（组织级）\n" + orgText
	default:
		return repoText
	}
}

// sharedAnalyzer 查询理解器单例（v3.178.0）：query.Analyzer 是纯只读配置态
// （now/loc/weekStart 三字段 New 后不变），包级复用省每次召回的重复分配。
var sharedAnalyzer = qanalyze.New(qanalyze.Config{})

// recallCfg 召回配置单源（v3.177.0）：缺省 + 逐调用预算 + 查询理解器。
// Analyzer（memory v0.8）：代词消解/时间窗解析——R3 注入的空查询场景补
// 画像线索，与宿主显式 Request 字段并行生效（宿主优先语义在内核侧）。
func recallCfg(budgetTokens int) recall.Config {
	cfg := recall.DefaultConfig()
	cfg.TotalBudget = budgetTokens
	cfg.Analyzer = sharedAnalyzer
	return cfg
}

// recallOne 单命名空间召回（失败降级空串：记忆是增强面，不许拖垮主流程）。
// emb 非 nil 时追加语义路（VectorSemanticReader 组合 sqlite 内置向量索引，
// RRF 与词面融合；embedding 调用失败按内核纪律降级词面）。
func (k *Kernel) recallOne(ctx context.Context, ns store.Namespace, sessionID, q string, budgetTokens int) string {
	if budgetTokens <= 0 {
		return ""
	}
	cfg := recallCfg(budgetTokens)
	if k.emb != nil {
		cfg.Semantic = ports.VectorSemanticReader{Embedder: k.emb, Index: k.store}
	}
	rec, err := recall.New(cfg, k.store, ports.StaticEstimator{}, ports.MemoryBand{})
	if err != nil {
		k.log.Warn("argus.memorykernel.recall_build_failed", "ns", string(ns), "error", err.Error())
		return ""
	}
	res, err := rec.Recall(ctx, recall.Request{
		Namespace: ns,
		Query:     q,
		SessionID: sessionID,
	})
	if err != nil {
		k.log.Warn("argus.memorykernel.recall_failed", "ns", string(ns), "error", err.Error())
		return ""
	}
	// 丢弃可观测（agent-memory v0.6.0 契约件）：分路计数各自指向不同处置
	//（budget=调总量/quota=调分桶占比/duplicate=提炼层在造重复条目），
	// 只在真发生丢弃时留痕——静默丢弃是「以为带上了记忆、实际没有」的最难发现失效。
	if res.Dropped > 0 {
		reasons := make([]string, 0, len(res.Drops))
		for r, n := range res.Drops {
			reasons = append(reasons, fmt.Sprintf("%s=%d", r, n))
		}
		sort.Strings(reasons)
		k.log.Info("argus.memorykernel.recall_dropped", "ns", string(ns),
			"dropped", res.Dropped, "kept", len(res.Snippets), "reasons", strings.Join(reasons, ","))
	}
	return res.Render()
}

// dreamSearchTerms 检索词面（类型 + 描述切词；预抽好避免召回时每次分词）。
func dreamSearchTerms(factType, description string) []string {
	terms := []string{factType}
	word := ""
	for _, r := range description {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			word += string(r)
		default:
			if len(word) >= 3 {
				terms = append(terms, word)
			}
			word = ""
		}
	}
	if len(word) >= 3 {
		terms = append(terms, word)
	}
	if len(terms) > 16 {
		terms = terms[:16]
	}
	return terms
}
