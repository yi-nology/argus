// Package semsearch 工作副本语义检索底座（v3.126.0，ARGUS_SEMSEARCH opt-in）：
// 把工作副本的 Go 顶层声明/文本文件抽成结构化摘要 chunk → embedding → 进程内
// 余弦检索，补 search_code（词法正则）答不了的「按意图找代码」面（哪里处理
// 认证/哪里刷新 session token——代码里未必有查询词）。v5.9.0 增图面：search
// 命中自动附调用方/被调邻接（SymRef），另提供 Path 两符号最短调用链与
// SymbolContext 单符号上下文两个图查询——调用边自产自 chunk 同一次 AST 解析
// （见 chunks.go/graph.go）。v5.10.0：节点键迁移 NodeKey 契约（dir 限定消歧
// 跨包同名），EdgeAugmenter 精确边注入钩子（档一接口，实现方经 WithEdges
// 注入，成功应用后精确边替换词法邻接、Manager.Precise 透出模式）。v5.11.0：
// embedder 允许 nil（纯图模式）——图查询（Path/SymbolContext/命中邻接）零
// embedding 依赖，Search/Manager.Search 在 Index.Search 单源拦截报错；审查
// 管线以 NewIndex(nil) 接图，不被 ARGUS_EMBEDDING_* 绑架。
//
// 行业坐标（2026-10 调研）：审查是异步、准确率优先、延迟不敏感的任务，预索引
// 在审查场景的价值高于编码助手场景（Greptile 全库图/Ellipsis 向量+历史 PR 索引
// 均为护城河）；「embedding 已死」是编码助手侧叙事，Cursor +12.5% 实证与
// JetBrains 反向建 RAG 都指向「grep+语义双工具组合最佳」。本包取混合形态的
// 低成本近似：embedding 文本用「路径+文档注释+签名」结构化摘要而非裸代码
// （Greptile NL 化做法的零 LLM 版），向量只驻进程内存、不落外部向量库。
//
// 层级：L0 纯底座（零 internal 依赖）。Embedder 由装配侧注入（rag.OpenAIEmbedder
// 天然满足 Embed+Dim 两方法）；进程内向量、余弦、指纹增量刷新均在此单源。
package semsearch

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Embedder 文本向量化接口（*rag.OpenAIEmbedder 天然满足）。
type Embedder interface {
	// Embed 批量向量化；返回向量数须与输入数一致。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim 向量维度。
	Dim() int
}

// Chunk 检索片段：一段供 embedding 的结构化摘要 + 命中展示元数据。
type Chunk struct {
	Path   string // 仓库相对路径
	Line   int    // 起始行（1-based）
	Symbol string // Go 顶层声明节点键（NodeKey 契约；文件级 chunk 为空）
	Text   string // embedding 输入（path:line + 源码片段）
}

// Hit 单条语义命中。
type Hit struct {
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Symbol    string   `json:"symbol,omitempty"`
	Score     float32  `json:"score"`
	Text      string   `json:"text"`
	Neighbors []SymRef `json:"neighbors,omitempty"` // 图邻接（v5.9.0，Symbol 非空时自动附）
}

// fileIndex 单文件的 chunk、调用边与向量（指纹不变即整文件复用，增量刷新的粒度）。
type fileIndex struct {
	chunks []Chunk
	edges  []symEdge // 调用边（v5.9.0，与 chunks 同一次 AST 解析的副产品）
	vecs   [][]float32
	fp     fileFP
}

// fileFP 文件指纹（size+mtime；变更即整文件重抽重嵌，增量刷新的判定粒度）。
type fileFP struct {
	size  int64
	mtime int64
}

// Index 一个工作副本目录的语义索引（进程内存驻留；工作副本生命周期由池侧
// TTL 管理，本包不做目录清理）。embedder 为 nil 即纯图模式（v5.11.0）：图面
// 可用，Search 拦截报错。
type Index struct {
	embedder Embedder

	mu             sync.Mutex            // 护 files/paths/sym/aug*/precise/truncated（Refresh 与 Search 并发）
	files          map[string]*fileIndex // key: 仓库相对路径
	paths          []string              // 排序路径快照（Search 确定序遍历）
	sym            *symGraph             // 符号邻接图（Refresh 收尾重建，nil=未构建；v5.9.0 图面）
	maxFiles       int                   // 文件数上限（默认 maxDefaultFiles，测试可调低）
	maxTotalChunks int                   // 全库 chunk 上限（embedding 成本闸）
	truncated      bool                  // 触达任一上限即置位（调用方可透出告警）

	// 精确边钩子（v5.10.0）：aug 由 Manager 经 WithEdges 注入（直建 Index 留空
	// =纯词法）；augSig/augEdges 是成功应用的签名与边缓存；augFailed 失败闩——
	// 本 Index 生命周期内不再重试（工作副本目录 per head-SHA，新 MR 新 Index
	// 自动重试）；precise=最近一次 Refresh 成功应用精确边（Manager.Precise 透出）。
	aug       EdgeAugmenter
	augSig    uint64
	augEdges  [][2]string
	augFailed bool
	precise   bool
}

// NewIndex 构建空索引（Refresh 前检索返回空）。e 允许 nil（v5.11.0 纯图模式）：
// Refresh 照常扫文件抽 chunk/边/建图，仅跳过向量化（vecs 置 nil）；Search
// （含 Manager.Search——经 Index.Search 到达同一拦截，单源）返回「语义检索
// 未启用」error，图查询 Path/SymbolContext/Size/Truncated 不受影响。
func NewIndex(e Embedder) *Index {
	return &Index{
		embedder:       e,
		files:          map[string]*fileIndex{},
		maxFiles:       maxDefaultFiles,
		maxTotalChunks: maxDefaultTotalChunks,
	}
}

// Manager 按目录缓存的索引管理器（search_sem 数据面入口）：目录首见时构建
// 索引，此后每次检索前按文件指纹增量刷新——只重嵌变更文件的 chunk。
type Manager struct {
	embedder Embedder
	aug      EdgeAugmenter // 精确边注入钩子（WithEdges 注入，透传给所建 Index）
	mu       sync.Mutex
	idx      map[string]*Index
}

// Option Manager 构建选项（装配侧按需注入可选能力）。
type Option func(*Manager)

// WithEdges 注入精确边 augmenter（v5.10.0 档一钩子，档二实现方注入）：所建
// Index 的 Refresh 尾部按文件集签名增量拉取精确调用边，成功应用即替换词法
// 邻接（Precise(dir)=true）；失败降级词法并置失败闩不再重试。
func WithEdges(a EdgeAugmenter) Option {
	return func(m *Manager) { m.aug = a }
}

// NewManager 构建管理器（opts 按序应用）。e 允许 nil（v5.11.0）=纯图 Manager：
// 所建 Index 同为纯图模式（图查询可用、Search 报错）。
func NewManager(e Embedder, opts ...Option) *Manager {
	m := &Manager{embedder: e, idx: map[string]*Index{}}
	for _, o := range opts {
		o(m)
	}
	return m
}

// indexFor 目录索引（缺则冷建并透传 aug；调用方先解锁再 Refresh——Refresh
// 自持 ix.mu）。Search/Trace/SymbolContext 三入口共用。
func (m *Manager) indexFor(dir string) *Index {
	m.mu.Lock()
	ix, ok := m.idx[dir]
	if !ok {
		ix = NewIndex(m.embedder)
		ix.aug = m.aug
		m.idx[dir] = ix
	}
	m.mu.Unlock()
	return ix
}

// Precise 目录索引是否处于精确边模式（v5.10.0：该目录索引最近一次 Refresh
// 成功应用 EdgeAugmenter 精确边；false=纯词法/augmenter 未配置/失败闩降级）。
// 目录未建索引返回 false。
func (m *Manager) Precise(dir string) bool {
	m.mu.Lock()
	ix, ok := m.idx[dir]
	m.mu.Unlock()
	if !ok {
		return false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.precise
}

// Search 目录语义检索：增量刷新索引后按 query 向量余弦取 topK（minScore 过滤，
// ≤0 表示不过滤）。检索失败返回 error；无命中返回空切片非 error。
func (m *Manager) Search(ctx context.Context, dir, query string, topK int, minScore float32) ([]Hit, error) {
	ix := m.indexFor(dir)
	if err := ix.Refresh(ctx, dir); err != nil {
		return nil, err
	}
	return ix.Search(ctx, query, topK, minScore)
}

// Trace 两符号间最短调用链（v5.9.0，get_trace 数据面底座）：增量刷新后在符号
// 邻接图上 BFS（无向，≤graphHopsDefault 跳）。首次对未建索引目录调用会付冷建
// 索引成本（embedding 一次性，增量零）；不可达或任一端不在符号表返回 ok=false。
func (m *Manager) Trace(ctx context.Context, dir, from, to string) ([]SymRef, bool, error) {
	ix := m.indexFor(dir)
	if err := ix.Refresh(ctx, dir); err != nil {
		return nil, false, err
	}
	chain, found := ix.Path(from, to, graphHopsDefault)
	return chain, found, nil
}

// SymbolContext 单符号图上下文（v5.9.0）：定义位置 + 调用方 + 被调 + 同名候选
// 数（cands，v5.10.0；多候选合并邻接）。索引模式与 Search/Trace 相同：目录缺
// 则冷建，Refresh 失败原样上抛。
func (m *Manager) SymbolContext(ctx context.Context, dir, symbol string) (self SymRef, in, out []SymRef, cands int, ok bool, err error) {
	ix := m.indexFor(dir)
	if err = ix.Refresh(ctx, dir); err != nil {
		return
	}
	self, in, out, cands, ok = ix.SymbolContext(symbol)
	return
}

// Size 索引内 chunk 总数（观测面）。
func (ix *Index) Size() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	n := 0
	for _, fi := range ix.files {
		n += len(fi.chunks)
	}
	return n
}

// Truncated 建索引时是否触达文件数/chunk 数上限。
func (ix *Index) Truncated() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.truncated
}

// Precise 索引是否处于精确边模式（v5.11.0 导出直建 Index 的读面；Manager.Precise
// 是同判的目录入口）：最近一次 Refresh 成功应用 EdgeAugmenter 精确边为 true；
// false=纯词法/augmenter 未配置/失败闩降级。图工具据此注明「词法级边」或
// 「类型解析级边（go/types）」。
func (ix *Index) Precise() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.precise
}

// Refresh 扫描目录并增量刷新索引：新文件/指纹变更的文件重新抽 chunk 并批量
// 向量化，消失的文件整组剔除。单文件扫描失败跳过（与 codesearch 同宽容度）；
// embedding 失败原样上抛（调用方决定降级文案）。embedder 为 nil（纯图模式）
// 时跳过向量化——chunk/边照常落地（vecs 置 nil），图面与 Size/Truncated 语义
// 不变。收尾顺带跑精确边钩子（applyAugLocked：签名变了才拉边，成功应用即
// 精确模式）。
func (ix *Index) Refresh(ctx context.Context, dir string) error {
	found, err := scanDir(dir)
	if err != nil {
		return err
	}
	modPath := modulePathOf(dir) // go.mod module 声明（import 限定消歧输入；缺失空串降级）
	scanned := len(found)
	if scanned > ix.maxFiles {
		found = found[:ix.maxFiles]
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.truncated = ix.truncated || scanned > ix.maxFiles

	// 全量先标 live（预算触顶的文件仍在盘上，只是本轮不嵌——先标后算，杜绝
	// 「预算 break 提前退出把未处理文件误当已删除剔除」）。
	live := make(map[string]bool, len(found))
	for _, f := range found {
		live[f.rel] = true
	}
	type pending struct {
		rel    string
		fp     fileFP
		chunks []Chunk
		edges  []symEdge // 与 chunks 同一次解析的调用边（v5.9.0 图面）
	}
	var todo []pending
	total := 0
	for _, fi := range ix.files {
		total += len(fi.chunks)
	}
	for _, f := range found {
		if old, ok := ix.files[f.rel]; ok && old.fp == f.fp {
			continue
		}
		chunks, edges := extractChunks(f.rel, f.content, modPath)
		if len(chunks) > maxFileChunks {
			chunks = chunks[:maxFileChunks]
		}
		budget := ix.maxTotalChunks - total
		if budget <= 0 {
			ix.truncated = true
			continue // 本轮不嵌；live 已标，不会误删既有条目
		}
		if len(chunks) > budget {
			chunks = chunks[:budget]
			ix.truncated = true
		}
		total += len(chunks)
		todo = append(todo, pending{rel: f.rel, fp: f.fp, chunks: chunks, edges: edges})
	}
	for rel := range ix.files {
		if !live[rel] {
			delete(ix.files, rel)
		}
	}
	ix.rebuildPathsLocked()            // 删除/预算截断都可能改变文件集，快照恒重建（此前仅在重嵌路径重建，todo 空时删除残留）
	ix.applyAugLocked(ctx, dir, found) // v5.10.0：精确边钩子（签名门控+失败闩，见 graph.go）
	ix.rebuildGraphLocked()            // v5.9.0：图面与符号集同源，删除/预算截断同样要重建
	if len(todo) == 0 {
		return nil
	}
	// 纯图模式（v5.11.0）：embedder 未注入——跳过向量化，chunk/边照常落地
	//（vecs 置 nil），图随 rebuildGraphLocked 照建；Search 面在 Index.Search
	// 单源拦截（Manager.Search 复用该判断）。
	if ix.embedder == nil {
		for _, p := range todo {
			ix.files[p.rel] = &fileIndex{fp: p.fp, chunks: p.chunks, edges: p.edges}
		}
		ix.rebuildPathsLocked()
		ix.applyAugLocked(ctx, dir, found)
		ix.rebuildGraphLocked()
		return nil
	}
	// 批量向量化（一次 Embed 调用摊薄 HTTP 往返）。
	var texts []string
	for _, p := range todo {
		for _, c := range p.chunks {
			texts = append(texts, c.Text)
		}
	}
	vecs, err := ix.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("chunk 向量化失败（%d 段）: %w", len(texts), err)
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("向量化返回数不符: got %d, want %d", len(vecs), len(texts))
	}
	pos := 0
	for _, p := range todo {
		fi := &fileIndex{fp: p.fp, edges: p.edges}
		for _, c := range p.chunks {
			fi.chunks = append(fi.chunks, c)
			fi.vecs = append(fi.vecs, vecs[pos])
			pos++
		}
		ix.files[p.rel] = fi
	}
	ix.rebuildPathsLocked()
	ix.applyAugLocked(ctx, dir, found) // v5.10.0：签名未变即零成本沿用既有精确边
	ix.rebuildGraphLocked()            // v5.9.0：重嵌落地后按新符号集重建图面
	return nil
}

// rebuildPathsLocked 重建排序路径快照（调用方持锁）。
func (ix *Index) rebuildPathsLocked() {
	ix.paths = ix.paths[:0]
	for rel := range ix.files {
		ix.paths = append(ix.paths, rel)
	}
	sort.Strings(ix.paths)
}

// Search 按 query 向量对全库 chunk 余弦打分，取相似度降序 topK（minScore>0 时
// 过滤低分命中）。纯图模式（embedder nil，v5.11.0）返回「语义检索未启用」
// error——Manager.Search 经本方法到达同一判断，拦截单源在此（Manager 层不重复）。
func (ix *Index) Search(ctx context.Context, query string, topK int, minScore float32) ([]Hit, error) {
	if ix.embedder == nil {
		return nil, fmt.Errorf("语义检索未启用（未配置 embedding）")
	}
	qv, err := ix.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("query 向量化失败: %w", err)
	}
	if len(qv) != 1 || len(qv[0]) == 0 {
		return nil, fmt.Errorf("query 向量化返回空")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	type scored struct {
		h Hit
	}
	hits := make([]scored, 0, 64)
	for _, rel := range ix.paths {
		fi := ix.files[rel]
		if fi == nil {
			continue // 快照与文件集的瞬时不一致防御
		}
		for i, c := range fi.chunks {
			s := cosine(qv[0], fi.vecs[i])
			if minScore > 0 && s < minScore {
				continue
			}
			hits = append(hits, scored{h: Hit{
				Path: c.Path, Line: c.Line, Symbol: c.Symbol, Score: s, Text: c.Text,
			}})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].h.Score > hits[j].h.Score })
	if topK <= 0 {
		topK = 5
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		// v5.9.0 图面：Symbol 非空的命中附调用方/被调邻接（sym 为 nil 跳过——
		// 图随 Refresh 重建，Refresh 成功后不会为 nil，防御留底）。
		if h.h.Symbol != "" && ix.sym != nil {
			h.h.Neighbors = ix.sym.neighborsOf(h.h.Symbol)
		}
		out = append(out, h.h)
	}
	return out, nil
}

// cosine 余弦相似度（零向量回 0；f64 中转避免 f32 累加误差）。
func cosine(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
