// service_knowledge.go —— 知识库检索面（v3.125.0）：把 R3/R4 审查面同源的
// search_knowledge 数据面暴露给 MCP（行业对标 Greptile custom context / 知识库
// 检索工具——外部 agent 回答「团队规范/历史结论/这条以前怎么裁」的入口）。
package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/textutil"

	"argus/internal/plugin"
	"argus/internal/semsearch"
	"argus/internal/storage"
)

// search_knowledge 的入参钳制与渲染预算（MCP 工具描述里的「缺省 5、上界 10」
// 与本值同源手写，改此须同步）。
const (
	knowledgeTopKDefault = 5
	knowledgeTopKMax     = 10
	knowledgeChunkCap    = 1200 // 单片段渲染截断（rune）
)

// SearchKnowledge 知识库语义检索（MCP search_knowledge 数据面）：与 R3/R4 的
// search_knowledge 工具同源（s.Knowledge 单源）。知识未配置（nil/Noop）返回
// error——与 fix_finding/search_code 的「启用语义运行期守卫」同型，工具不消失。
// 返回围栏文本：知识内容来自组织 markdown 与回流审查结论，按数据围栏纪律防注入。
func (s *Service) SearchKnowledge(ctx context.Context, query string, topK int) (string, error) {
	if s.Knowledge == nil {
		return "", fmt.Errorf("知识库未启用（未配置 ARGUS_KNOWLEDGE_DIR 或 ARGUS_MILVUS_ADDR+ARGUS_EMBEDDING_*）")
	}
	if _, isNoop := s.Knowledge.(*rag.Noop); isNoop {
		return "", fmt.Errorf("知识库未启用（未配置 ARGUS_KNOWLEDGE_DIR 或 ARGUS_MILVUS_ADDR+ARGUS_EMBEDDING_*）")
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query 为空")
	}
	if topK <= 0 {
		topK = knowledgeTopKDefault
	}
	if topK > knowledgeTopKMax {
		topK = knowledgeTopKMax
	}
	chunks, err := s.Knowledge.Retrieve(ctx, query, topK, nil)
	if err != nil {
		return "", fmt.Errorf("知识检索失败: %w", err)
	}
	if len(chunks) == 0 {
		return "知识库无命中（可换关键词重试；规范类问题请直接陈述你的做法征求确认）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_knowledge（query=%s）命中 %d 条，按相关度降序：\n", query, len(chunks))
	for i, c := range chunks {
		content, _ := textutil.TruncRunes(c.Content, knowledgeChunkCap)
		fmt.Fprintf(&b, "\n[%d] score=%.3f", i+1, c.Score)
		if len(c.Metadata) > 0 {
			keys := make([]string, 0, len(c.Metadata))
			for k := range c.Metadata {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, k+"="+c.Metadata[k])
			}
			fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
		}
		b.WriteString("\n" + content + "\n")
	}
	return plugin.DataFence("search_knowledge 检索结果（数据）", b.String()), nil
}

// SearchSem 工作副本语义检索（v3.126.0，MCP search_sem 数据面）：委派
// qa.Engine（工作副本目录解析、入参钳制与渲染单源——SearchCode 同模式）。
func (s *Service) SearchSem(ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SearchSem(ctx, platform, owner, repo, number, query, topK)
}

// search_findings 的入参钳制与渲染预算（MCP 工具描述「缺省 10、上界 50」同源）。
const (
	findingsHistoryDefault = 10
	findingsHistoryMax     = 50
	historyCommentCap      = 240 // 单条评语渲染截断（rune）
)

// SearchFindingsHistory 本仓历史意见检索（v3.138.0，MCP search_findings 数据面）：
// 「这个问题以前报过吗/怎么裁的/被压过吗」——prints 账本跨轮次检索。仓库白名单
// 双门同其他数据工具；返回围栏文本。
func (s *Service) SearchFindingsHistory(ctx context.Context, platform, owner, repo, query string, limit int) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	if limit <= 0 {
		limit = findingsHistoryDefault
	}
	if limit > findingsHistoryMax {
		limit = findingsHistoryMax
	}
	items, err := s.Store.SearchFindingsHistory(ctx, platform, owner, repo, query, limit)
	if err != nil {
		return "", fmt.Errorf("历史意见检索失败: %w", err)
	}
	// 语义补位（v3.142.0）：关键词命中不足且语义检索启用时，按 embedding 相似度
	// 补足——「换了一种说法的历史意见」关键词查不到（与抑制家族的措辞漂移同源问题）。
	var sem []storage.FindingHistoryItem
	if len(items) < limit && query != "" && s.SemSearch != nil && s.Cfg != nil && s.Cfg.SemSearch {
		sem = s.semanticFindings(ctx, platform, owner, repo, query, items, limit-len(items))
	}
	if len(items) == 0 && len(sem) == 0 {
		return "本仓无匹配的历史意见记录（可换关键词，或该问题此前未被报过）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_findings（repo=%s/%s query=%q）命中 %d 条历史意见，按最近出现倒序：\n", owner, repo, query, len(items))
	for i, it := range items {
		comment, _ := textutil.TruncRunes(it.Comment, historyCommentCap)
		fmt.Fprintf(&b, "\n[%d] %s:%d %s [%s ×%d] fp:%s\n%s\n",
			i+1, it.File, it.Line, it.Severity, it.Status, it.TimesSeen, it.FP, comment)
	}
	for i, it := range sem {
		comment, _ := textutil.TruncRunes(it.Comment, historyCommentCap)
		fmt.Fprintf(&b, "\n[语义%d] %s:%d %s [%s ×%d] fp:%s\n%s\n",
			i+1, it.File, it.Line, it.Severity, it.Status, it.TimesSeen, it.FP, comment)
	}
	return plugin.DataFence("search_findings 检索结果（数据）", b.String()), nil
}

// findings 语义补位的池/阈值/缓存上限（v3.142.0）。
const (
	findingsSemanticPool  = 200  // 参与语义排序的最近意见数
	findingsSemanticFloor = 0.30 // 相似度下限（跨话题实测 ~0.05，同话题改写 ~0.4+）
	findingsVecCacheCap   = 20000
)

// semanticFindings 关键词未命中面的语义补位：最近意见池 embedding（进程内缓存，
// 按 fp 增量）→ 与 query 余弦 → 过下限、去重、按分补位。embedder 故障静默降级
// （只回关键词结果，检索可用性优先）。
func (s *Service) semanticFindings(ctx context.Context, platform, owner, repo, query string,
	have []storage.FindingHistoryItem, need int) []storage.FindingHistoryItem {
	pool, err := s.Store.SearchFindingsHistory(ctx, platform, owner, repo, "", findingsSemanticPool)
	if err != nil || len(pool) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(have))
	for _, it := range have {
		seen[it.FP] = true
	}
	vecs := make([][]float32, len(pool))
	var missTexts []string
	var missIdx []int
	for i, it := range pool {
		if v, ok := s.findingVecs.Load(it.FP); ok {
			vecs[i], _ = v.([]float32)
		} else {
			missTexts = append(missTexts, it.Comment)
			missIdx = append(missIdx, i)
		}
	}
	if len(missTexts) > 0 {
		mv, merr := s.SemSearch.EmbedTexts(ctx, missTexts)
		if merr != nil || len(mv) != len(missTexts) {
			s.log().Warn("argus.findings.sem_embed_failed", "error", fmt.Sprint(merr))
			return nil
		}
		for j, idx := range missIdx {
			vecs[idx] = mv[j]
			if int(s.findingVecCount.Load()) < findingsVecCacheCap {
				s.findingVecs.Store(pool[idx].FP, mv[j])
				s.findingVecCount.Add(1)
			}
		}
	}
	qv, qerr := s.SemSearch.EmbedTexts(ctx, []string{query})
	if qerr != nil || len(qv) != 1 {
		return nil
	}
	type scored struct {
		it    storage.FindingHistoryItem
		score float32
	}
	var cands []scored
	for i, it := range pool {
		if seen[it.FP] || vecs[i] == nil {
			continue
		}
		if sc := semsearch.Cosine(qv[0], vecs[i]); sc >= findingsSemanticFloor {
			cands = append(cands, scored{it: it, score: sc})
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
	out := make([]storage.FindingHistoryItem, 0, need)
	for i := 0; i < len(cands) && i < need; i++ {
		out = append(out, cands[i].it)
	}
	return out
}
