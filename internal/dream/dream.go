// Package dream 长期记忆定期整合（docs/review-closed-loop-design.md §8）：低置信模式去重合并
// 与知识导出，R3 search_knowledge 的数据供给方。
//
// OFC 的 dream 对应到这里：
//  1. 去重合并：同 plugin 且 pattern_text 前缀高度重合的行，计数并入样本更多的一行；
//  2. 知识导出：周期把「低置信 + 样本充足」的模式写成结构化知识，供 R3 search_knowledge
//     检索——语义检索（Milvus）与符号信号（pattern_signals）双轨。
//
// 全部确定性、无 LLM；失败只告警，不影响审查主流程。
package dream

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oagentkit/guardx/fence"
	"github.com/oagentkit/rag"
	"github.com/oagentkit/textx/textutil"
	"github.com/oekit/logx"

	"argus/internal/learning"
	"argus/internal/memorykernel"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// DedupPrefixRunes 去重键：pattern_text 前 N 个 rune（normalize 后已无路径/hex 噪声）。
const DedupPrefixRunes = 80

// Store dream 需要的最小存储面。RepoRef 已归 learning（v3.54.0）；
// PatternSignal/MergePatternInto 是去重语义的固有部分（按 DB 行身份合并），
// 与 storage 的这一耦合如实保留。
// Store dream 需要的最小存储面。
type Store interface {
	ListPatternRepos(ctx context.Context) ([]learning.RepoRef, error)
	ListRepoPatterns(ctx context.Context, platform, owner, repo string) ([]storage.PatternSignal, error)
	MergePatternInto(ctx context.Context, platform, owner, repo string, keepID, dupID uint, at time.Time) error
	ListLowConfidencePatterns(ctx context.Context, platform, owner, repo string,
		minSamples int, maxConfidence float64, limit int) ([]learning.PatternState, error)
}

// Dream 周期整合器。
type Dream struct {
	Store     Store
	Knowledge rag.KnowledgeService // nil/Noop = 只写本地 markdown
	// KnowledgeDir 本地知识目录（Milvus 不可用时落盘，下次启动索引）。
	KnowledgeDir string
	// Kernel agent-memory 内核（v5.0.0 起是记忆唯一路径，旧 markdown 文件面已
	// 退役）：nil = 记忆面整体哑掉（知识检索面照常）。写入失败只告警，不阻断
	// 去重与知识导出主流程。
	Kernel *memorykernel.Kernel
	// Learning 学习环阈值（复用 config 派生）。
	Learning learning.Config
	// ExportTopN 每仓库导出条数上限。
	ExportTopN int
	Log        logx.Logger
}

// RunOnce 执行一轮 dream：去重 → 导出。返回便于测试/日志的计数。
func (d *Dream) RunOnce(ctx context.Context) (merged int, exportedRepos int, err error) {
	if d == nil || d.Store == nil {
		return 0, 0, nil
	}
	at := time.Now().UTC()
	repos, err := d.Store.ListPatternRepos(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("dream: list repos: %w", err)
	}
	// 跨仓聚合收集：本轮各仓的低置信模式（组织桶写侧输入）
	var allLow []repoPatterns
	for _, r := range repos {
		n, err := d.dedupRepo(ctx, r, at)
		if err != nil {
			d.warn("argus.dream.dedup_failed", "repo", r.Owner+"/"+r.Repo, "error", err.Error())
			continue
		}
		merged += n
		ok, low, err := d.exportRepo(ctx, r, at)
		if err != nil {
			d.warn("argus.dream.export_failed", "repo", r.Owner+"/"+r.Repo, "error", err.Error())
			continue
		}
		if ok {
			exportedRepos++
			allLow = append(allLow, repoPatterns{ref: r, low: low})
		}
	}
	// 组织桶聚合（v3.74.1）：pattern 跨仓共现 ≥2 → 组织级模式，写内核 _org 桶
	//（R3 注入所有仓）。失败只告警，不阻断仓桶导出语义。
	if d.Kernel != nil && len(allLow) > 0 {
		if n := d.exportOrgMemory(ctx, allLow, at); n > 0 {
			d.info("argus.dream.org_memory", "patterns", n)
		}
	}
	if merged > 0 || exportedRepos > 0 {
		d.info("argus.dream.done", "merged", merged, "exported_repos", exportedRepos)
	}
	return merged, exportedRepos, nil
}

// repoPatterns 单仓导出的低置信模式集（组织桶聚合输入）。
type repoPatterns struct {
	ref learning.RepoRef
	low []learning.PatternState
}

// exportOrgMemory 跨仓共现聚合：归一 pattern 文本（trim）在 ≥2 个仓出现即组织级
// 模式，写内核 _org 桶。幂等=先清桶后写；无共现时清空（陈旧组织模式不留场）。
func (d *Dream) exportOrgMemory(ctx context.Context, all []repoPatterns, at time.Time) int {
	type agg struct {
		repos map[string]bool
	}
	byPattern := map[string]*agg{}
	for _, rp := range all {
		for _, p := range rp.low {
			key := strings.TrimSpace(p.Pattern)
			if key == "" {
				key = strings.TrimSpace(p.PatternText)
			}
			if key == "" {
				continue
			}
			a, ok := byPattern[key]
			if !ok {
				a = &agg{repos: map[string]bool{}}
				byPattern[key] = a
			}
			a.repos[rp.ref.Owner+"/"+rp.ref.Repo] = true
		}
	}
	if err := d.Kernel.ClearRepo(ctx, memorykernel.OrgNamespace); err != nil {
		d.warn("argus.dream.org_kernel_clear_failed", "error", err.Error())
		return 0
	}
	n := 0
	for key, a := range byPattern {
		if len(a.repos) < 2 {
			continue
		}
		repos := make([]string, 0, len(a.repos))
		for r := range a.repos {
			repos = append(repos, r)
		}
		sort.Strings(repos)
		sampleHead, _ := textutil.TruncRunes(key, 80)
		desc := fmt.Sprintf("跨仓模式（%d 仓共现）：%s", len(a.repos), memorykernel.SingleLine(sampleHead))
		body := fmt.Sprintf("该审查模式在以下仓库均呈低置信（样本各自独立，跨仓共现提示组织级噪声源）：\n%s\n\n"+
			"命中类似表述请提高举证标准。来源仓库：%s\n",
			strings.Join(repos, "\n"), strings.Join(repos, ", "))
		slug := fmt.Sprintf("cross-%s", shortHash(key))
		if err := d.Kernel.SaveFact(ctx, memorykernel.OrgNamespace, slug, "pattern", desc, body, at); err != nil {
			d.warn("argus.dream.org_kernel_save_failed", "slug", slug, "error", err.Error())
			continue
		}
		n++
	}
	return n
}

// shortHash pattern 文本的短指纹（slug 稳定化，跨轮幂等覆盖同模式文件）。
func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// dedupRepo 同 plugin + pattern_text 前缀相同 → 计数并入样本更多的一行。
func (d *Dream) dedupRepo(ctx context.Context, r learning.RepoRef, at time.Time) (int, error) {
	rows, err := d.Store.ListRepoPatterns(ctx, r.Platform, r.Owner, r.Repo)
	if err != nil {
		return 0, err
	}
	type groupKey struct {
		plugin string
		prefix string
	}
	groups := map[groupKey][]storage.PatternSignal{}
	for _, row := range rows {
		prefix, _ := textutil.TruncRunes(row.PatternText, DedupPrefixRunes)
		if prefix == "" {
			prefix = row.Pattern
		}
		k := groupKey{plugin: row.Plugin, prefix: prefix}
		groups[k] = append(groups[k], row)
	}
	merged := 0
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		keep := g[0]
		for _, c := range g[1:] {
			if betterKeep(c, keep) {
				keep = c
			}
		}
		for _, c := range g {
			if c.ID == keep.ID {
				continue
			}
			if err := d.Store.MergePatternInto(ctx, r.Platform, r.Owner, r.Repo, keep.ID, c.ID, at); err != nil {
				return merged, err
			}
			merged++
		}
	}
	return merged, nil
}

func betterKeep(a, b storage.PatternSignal) bool {
	as := a.Posted + a.Regret + a.Suppress
	bs := b.Posted + b.Regret + b.Suppress
	if as != bs {
		return as > bs
	}
	return a.ID < b.ID // 稳定：样本相同时保留更早创建的
}

// exportRepo 把低置信模式写成知识条目。无低置信样本时跳过（ok=false）。
func (d *Dream) exportRepo(ctx context.Context, r learning.RepoRef, at time.Time) (bool, []learning.PatternState, error) {
	topN := d.ExportTopN
	if topN <= 0 {
		topN = 20
	}
	low, err := d.Store.ListLowConfidencePatterns(ctx, r.Platform, r.Owner, r.Repo,
		d.Learning.MinSamples, d.Learning.SoftFloor, topN)
	if err != nil {
		return false, nil, err
	}
	if len(low) == 0 {
		// v3.8.0：仓库不再有低置信模式时覆盖旧导出——此前提前 return，
		// 旧的"历史高误报模式"结论永久滞留并持续注入 R3 检索（只增不删）。
		// Milvus 无删除接口，写澄清文案覆盖（幂等，命中时不再误导）。
		clear := fmt.Sprintf("## %s/%s 审查模式状态\n\n生成时间：%s。本仓库当前没有样本充足且低置信的审查模式。\n",
			r.Owner, r.Repo, at.Format(time.RFC3339))
		key := fmt.Sprintf("dream/%s/%s/%s", r.Platform, r.Owner, r.Repo)
		if ms, ok := d.Knowledge.(*rag.MilvusStore); ok && ms != nil {
			if err := ms.Index(ctx, key, clear); err != nil {
				d.warn("argus.dream.milvus_clear_failed", "error", err.Error())
			}
		}
		if d.KnowledgeDir != "" {
			if err := writeLocalKnowledge(d.KnowledgeDir, r, clear); err != nil {
				return false, nil, err
			}
		}
		// 记忆面同步退场（v3.8.0 只增不删教训的记忆侧同款修复）
		ref := memorykernel.Ref{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo}
		if d.Kernel != nil {
			if err := d.Kernel.ClearRepo(ctx, memorykernel.NamespaceOf(ref)); err != nil {
				d.warn("argus.dream.kernel_clear_failed", "error", err.Error())
			}
		}
		return false, nil, nil
	}
	body := renderKnowledge(r, low, at)

	// Milvus 主路径（与 suppress 回流同形）；失败降级本地 markdown。
	// 成功时同样写本地副本（深挖修复）：本地 markdown 是 Milvus 不可用时的降级
	// 载体，长期不写会陈旧误导（幂等覆盖，成本一次文件写）。
	key := fmt.Sprintf("dream/%s/%s/%s", r.Platform, r.Owner, r.Repo)
	if ms, ok := d.Knowledge.(*rag.MilvusStore); ok && ms != nil {
		if err := ms.Index(ctx, key, body); err != nil {
			d.warn("argus.dream.milvus_failed", "error", err.Error())
		}
	}
	// 记忆面（v5.0.0 起内核唯一路径）：低置信模式逐条写内核事实卡（感知面），
	// 与知识聚合文档（search_knowledge，检索面）互补。Kernel nil = 记忆面哑掉
	//（bootstrap 只在 MemKernel 装配后才传），不影响知识导出。
	if d.Kernel != nil {
		d.writeKernelMemory(ctx, r, low, at)
	}
	if d.KnowledgeDir == "" {
		return true, low, nil
	}
	if err := writeLocalKnowledge(d.KnowledgeDir, r, body); err != nil {
		return false, nil, err
	}
	return true, low, nil
}

// writeKernelMemory 内核面写入（v5.0.0 起记忆唯一路径）：先清桶后逐条写（幂等）。
// 失败只告警——记忆是增强面，不得反过来阻断去重与知识导出。
func (d *Dream) writeKernelMemory(ctx context.Context, r learning.RepoRef, low []learning.PatternState, at time.Time) {
	ns := memorykernel.NamespaceOf(memorykernel.Ref{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo})
	if err := d.Kernel.ClearRepo(ctx, ns); err != nil {
		d.warn("argus.dream.kernel_clear_failed", "repo", r.Owner+"/"+r.Repo, "error", err.Error())
	}
	for i, p := range low {
		sample := p.SampleText()
		sampleHead, _ := textutil.TruncRunes(sample, 80)
		desc := fmt.Sprintf("[%s] %s（置信度 %.2f，samples=%d）",
			p.Plugin, memorykernel.SingleLine(sampleHead), p.Confidence, p.Samples)
		body := fmt.Sprintf("命中类似表述请提高举证标准，无具体行与影响则不要输出。\n\n"+
			"样本：%s\n\n来源：plugin=%s regret=%d suppress=%d\n",
			sample, p.Plugin, p.Regret, p.Suppress)
		// 内核写侧自带脱敏（capture 必需 Redactor），不再叠 fence.EscapeUntrusted——
		// 双重转义会让召回正文带着 HTML 实体，检索词面被污染。
		slug := fmt.Sprintf("pattern-%02d", i+1)
		if err := d.Kernel.SaveFact(ctx, ns, slug, "pattern", desc, body, at); err != nil {
			d.warn("argus.dream.kernel_save_failed", "slug", slug, "error", err.Error())
		}
	}
}

// renderKnowledge 渲染知识 markdown（safejson 转义：pattern_text 半可信）。
func renderKnowledge(r learning.RepoRef, low []learning.PatternState, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s/%s 历史高误报审查模式（dream 导出）\n\n", r.Owner, r.Repo)
	fmt.Fprintf(&b, "生成时间：%s。以下模式在本仓库样本充足且置信度偏低；"+
		"审查时命中类似表述请提高举证标准，无具体行与影响则不要输出。\n\n", at.Format(time.RFC3339))
	for i, p := range low {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		fmt.Fprintf(&b, "%d. **[%s]** %s\n   - 置信度 %.2f（samples=%d；regret=%d suppress=%d）\n",
			i+1, p.Plugin, fence.EscapeUntrusted(sample), p.Confidence, p.Samples, p.Regret, p.Suppress)
	}
	return b.String()
}

func writeLocalKnowledge(dir string, r learning.RepoRef, body string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 标识消毒：GitLab 嵌套组 owner 含 "/" 时未消毒文件名会写入失败、
	// exportRepo 每周期告警（v3.9.1）。
	name := plugin.SanitizeFileStem(fmt.Sprintf("dream-%s-%s", r.Owner, r.Repo)) + ".md"
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

func (d *Dream) info(msg string, kv ...any) {
	if d.Log != nil {
		d.Log.Info(msg, kv...)
	}
}

func (d *Dream) warn(msg string, kv ...any) {
	if d.Log != nil {
		d.Log.Warn(msg, kv...)
	}
}
