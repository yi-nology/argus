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

	"github.com/oagentkit/rag"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
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
	if len(items) == 0 {
		return "本仓无匹配的历史意见记录（可换关键词，或该问题此前未被报过）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_findings（repo=%s/%s query=%q）命中 %d 条历史意见，按最近出现倒序：\n", owner, repo, query, len(items))
	for i, it := range items {
		comment, _ := textutil.TruncRunes(it.Comment, historyCommentCap)
		fmt.Fprintf(&b, "\n[%d] %s:%d %s [%s ×%d] fp:%s\n%s\n",
			i+1, it.File, it.Line, it.Severity, it.Status, it.TimesSeen, it.FP, comment)
	}
	return plugin.DataFence("search_findings 检索结果（数据）", b.String()), nil
}

// Trace 两符号间调用链（v5.9.0，MCP get_trace 数据面）：委派 qa.Engine（工作
// 副本目录解析、守卫与渲染单源——SearchSem 同模式）。仓库白名单门同 SearchSem；
// 不可达返回提示文本非 error（正常查询结果），语义细节在 qa.Engine 单源。
func (s *Service) Trace(ctx context.Context, platform, owner, repo, number, from, to string) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().Trace(ctx, platform, owner, repo, number, from, to)
}

// SymContext 单符号图上下文（v5.9.0，MCP get_symbol_context 数据面）：委派
// qa.Engine（工作副本目录解析、守卫与渲染单源——SearchSem 同模式）。
func (s *Service) SymContext(ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return "", errors.New(errRepoDenied)
	}
	return s.qa().SymContext(ctx, platform, owner, repo, number, symbol)
}
