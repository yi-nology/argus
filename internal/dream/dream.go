// dream.go —— 长期记忆定期整合（docs/review-closed-loop-design.md §8）。
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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/fence"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/learning"
	"argus/internal/memory"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// DedupPrefixRunes 去重键：pattern_text 前 N 个 rune（normalize 后已无路径/hex 噪声）。
const DedupPrefixRunes = 80

// Store dream 需要的最小存储面。
type Store interface {
	ListPatternRepos(ctx context.Context) ([]storage.RepoRef, error)
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
	// MemoryDir repo 评审记忆目录（v3.35.0；空 = 不写记忆面）。低置信模式
	// 同源双写事实文件（一文件一事实），R3 提示词注入索引。
	MemoryDir string
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
	for _, r := range repos {
		n, err := d.dedupRepo(ctx, r, at)
		if err != nil {
			d.warn("argus.dream.dedup_failed", "repo", r.Owner+"/"+r.Repo, "error", err.Error())
			continue
		}
		merged += n
		ok, err := d.exportRepo(ctx, r, at)
		if err != nil {
			d.warn("argus.dream.export_failed", "repo", r.Owner+"/"+r.Repo, "error", err.Error())
			continue
		}
		if ok {
			exportedRepos++
		}
	}
	if merged > 0 || exportedRepos > 0 {
		d.info("argus.dream.done", "merged", merged, "exported_repos", exportedRepos)
	}
	return merged, exportedRepos, nil
}

// dedupRepo 同 plugin + pattern_text 前缀相同 → 计数并入样本更多的一行。
func (d *Dream) dedupRepo(ctx context.Context, r storage.RepoRef, at time.Time) (int, error) {
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
		prefix := truncateRunes(row.PatternText, DedupPrefixRunes)
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
func (d *Dream) exportRepo(ctx context.Context, r storage.RepoRef, at time.Time) (bool, error) {
	topN := d.ExportTopN
	if topN <= 0 {
		topN = 20
	}
	low, err := d.Store.ListLowConfidencePatterns(ctx, r.Platform, r.Owner, r.Repo,
		d.Learning.MinSamples, d.Learning.SoftFloor, topN)
	if err != nil {
		return false, err
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
				return false, err
			}
		}
		// 记忆面同步退场（v3.8.0 只增不删教训的记忆侧同款修复）
		if d.MemoryDir != "" {
			if err := memory.ClearRepo(d.MemoryDir, memory.Ref{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo}); err != nil {
				d.warn("argus.dream.memory_clear_failed", "error", err.Error())
			}
		}
		return false, nil
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
	if d.KnowledgeDir == "" {
		return false, nil
	}
	if err := writeLocalKnowledge(d.KnowledgeDir, r, body); err != nil {
		return false, err
	}
	// 记忆面双写（v3.35.0）：逐事实落 repo 记忆目录（索引注入 R3，感知面）；
	// 与知识聚合文档（search_knowledge，检索面）互补。
	if d.MemoryDir != "" {
		if err := writeMemory(d.MemoryDir, r, low, at); err != nil {
			d.warn("argus.dream.memory_failed", "error", err.Error())
		}
	}
	return true, nil
}

// writeMemory 逐事实写记忆文件（幂等：先清后写——模式集变化后旧文件不留场）。
func writeMemory(dir string, r storage.RepoRef, low []learning.PatternState, at time.Time) error {
	ref := memory.Ref{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo}
	if err := memory.ClearRepo(dir, ref); err != nil {
		return err
	}
	for i, p := range low {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		desc := fmt.Sprintf("[%s] %s（置信度 %.2f，samples=%d）",
			p.Plugin, singleLine(truncateRunes(sample, 80)), p.Confidence, p.Samples)
		body := fmt.Sprintf("命中类似表述请提高举证标准，无具体行与影响则不要输出。\n\n"+
			"样本（已消毒）：%s\n\n来源：plugin=%s regret=%d suppress=%d\n",
			fence.EscapeUntrusted(sample), p.Plugin, p.Regret, p.Suppress)
		if err := memory.Save(dir, ref, fmt.Sprintf("pattern-%02d", i+1), "pattern", desc, body, at); err != nil {
			return err
		}
	}
	return nil
}

// singleLine 压平换行（description 进单行 frontmatter）。
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// renderKnowledge 渲染知识 markdown（safejson 转义：pattern_text 半可信）。
func renderKnowledge(r storage.RepoRef, low []learning.PatternState, at time.Time) string {
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

func writeLocalKnowledge(dir string, r storage.RepoRef, body string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 标识消毒：GitLab 嵌套组 owner 含 "/" 时未消毒文件名会写入失败、
	// exportRepo 每周期告警（v3.9.1）。
	name := plugin.SanitizeFileStem(fmt.Sprintf("dream-%s-%s", r.Owner, r.Repo)) + ".md"
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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
