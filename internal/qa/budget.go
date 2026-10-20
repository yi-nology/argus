// budget.go —— QA 会话历史的 token 预算压实（v3.152.0，agent-harness/budget 接入）。
//
// 既有窗口是轮数制（maxTurns=6 verbatim + 滚动摘要）：轮次长短方差大，
// 6 轮长问答可吹爆上下文而 20 轮短问答仍富余。本文件在不动既有轮数路径的
// 前提下加 token 压力门：历史估算总量到 Critical（≥90% 预算）才切
// budget.BuildPlan+Apply 压实路径——摘要生成归宿主（复用既有滚动摘要模型
// 调用），归档为会话内 no-op（原文本来就在 QASession.HistoryJSON，Locator
// 只给对账句柄）。budget.Item 只携带 ID+Tokens（内核不碰内容），正文由
// 摘要器装配时按 ID 对齐捕获——两侧必须同源切片，错位即编不出正确摘要。
package qa

import (
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/yi-nology/agent-harness/budget"

	"argus/internal/storage"
)

// defaultHistoryBudgetTokens 历史注入 token 预算缺省（ARGUS_QA_HISTORY_BUDGET 可配）。
// 口径：本地估算 ceil(runes/3)（与 textx/compact.EstimateTokens 同口径），
// 非 provider 报数——分段估算永不回填 provider 报数（budget 包纪律）。
const defaultHistoryBudgetTokens = 6000

// summaryItemID 摘要作为段内首条目的固定 ID（防「摘要条目混进轮次序号」）。
const summaryItemID = "summary"

// historyBudget 引擎历史预算（0 = 缺省）。
func (e *Engine) historyBudget() int {
	if e.HistoryBudgetTokens > 0 {
		return e.HistoryBudgetTokens
	}
	return defaultHistoryBudgetTokens
}

// estimateTurnTokens 单轮（问+答）本地 token 估算：ceil(runes/3)。
func estimateTurnTokens(q, a string) int {
	return (utf8.RuneCountInString(q) + utf8.RuneCountInString(a) + 2) / 3
}

// historyItems 会话段条目化：摘要（非空时）为第 0 条，其后逐轮。
// contents 与返回条目按下标一一同源（summary 条目的正文即摘要文本）；
// total 为全段 token 合计（含摘要）。
func historyItems(history []storage.QATurn, summary string) (items []budget.Item, contents []string, total int) {
	if summary != "" {
		t := (utf8.RuneCountInString(summary) + 2) / 3
		items = append(items, budget.Item{ID: summaryItemID, Tokens: t})
		contents = append(contents, "【已有摘要】\n"+summary)
		total += t
	}
	for i, turn := range history {
		t := estimateTurnTokens(turn.Question, turn.Answer)
		items = append(items, budget.Item{ID: strconv.Itoa(i), Tokens: t})
		contents = append(contents, "问："+turn.Question+"\n答："+turn.Answer)
		total += t
	}
	return items, contents, total
}

// compactHistory 预算压实路径：返回新摘要 + 逐字保留尾部轮次。
// 仅在 Critical 压力档由 Answer 调用；计划守卫拒绝（条目不足/截断过低）或
// 摘要失败时 ok=false，调用方回落既有轮数窗口路径（行为与此前逐字一致）。
func (e *Engine) compactHistory(ctx context.Context, prRef string, history []storage.QATurn, summary string) (newSummary string, tail []storage.QATurn, ok bool) {
	items, contents, _ := historyItems(history, summary)
	plan, err := budget.BuildPlan(budget.PlanInput{Items: len(items), KeepTail: maxTurns})
	if err != nil {
		return "", nil, false
	}
	leading, tailItems := items[:plan.Cutoff], items[plan.Cutoff:]
	sum := &qaSummarizer{e: e, prRef: prRef, leading: contents[:plan.Cutoff]}
	res, err := budget.Apply(ctx, plan, leading, tailItems, qaArchive{}, sum)
	if err != nil {
		e.log().Warn("argus.qa.budget_compact_failed", "pr", prRef, "error", err.Error())
		return "", nil, false
	}
	// 尾部条目 ID → 原始轮次（historyItems 保证轮次条目 ID=history 下标）
	tail = make([]storage.QATurn, 0, len(tailItems))
	for _, it := range tailItems {
		idx, err := strconv.Atoi(it.ID)
		if err != nil || idx < 0 || idx >= len(history) {
			continue // summaryItemID 不会出现在尾部（BuildPlan 语义），防御性跳过
		}
		tail = append(tail, history[idx])
	}
	return res.Summary.Text, tail, true
}

// qaArchive 压实归档：原文已在 QASession.HistoryJSON 持久化（存储面既有），
// 此处只产出对账句柄，不做二次 I/O。
type qaArchive struct{}

func (qaArchive) Archive(_ context.Context, items []budget.Item) (budget.Locator, error) {
	return budget.Locator(fmt.Sprintf("qa-session://history#items=%d", len(items))), nil
}

// qaSummarizer budget.Summarizer 宿主实现：正文在装配时按 ID 同源捕获
// （Item 无内容面），摘要走既有 QA 模型（与 rollSummary 同一模型选择）。
type qaSummarizer struct {
	e       *Engine
	prRef   string
	leading []string
}

func (s *qaSummarizer) Summarize(ctx context.Context, _ []budget.Item) (budget.Summary, error) {
	cm := s.e.chatModel()
	if cm == nil {
		return budget.Summary{}, fmt.Errorf("qa: 摘要模型不可用")
	}
	text, err := s.e.summarizeTurns(ctx, s.prRef, s.leading)
	if err != nil || text == "" {
		return budget.Summary{}, fmt.Errorf("qa: 摘要生成失败: %w", err)
	}
	return budget.Summary{Text: text, Tokens: (utf8.RuneCountInString(text) + 2) / 3}, nil
}
