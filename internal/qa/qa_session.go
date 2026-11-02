// qa_session.go —— QA 会话历史的加载与乐观锁落库（v3.158.1 自 Answer 抽出，
// 逻辑逐字保留：读失败留痕/坏 JSON 降级/CAS 冲突重放/摘要滞后一轮语义）。
package qa

import (
	"context"
	"encoding/json"
	"time"

	"argus/internal/storage"
)

// qaSessionHistory 会话历史加载结果。
type qaSessionHistory struct {
	sess    *storage.QASession // nil = 无会话（新话题或读失败）
	turns   []storage.QATurn
	summary string
}

// loadQASession 读会话并解码历史轮次。读失败/坏 JSON 都降级为无历史并留痕——
// DB 抖动表现为「失忆」必须可排查（v3.8.0 深挖修复语义原样保留）。
func (e *Engine) loadQASession(ctx context.Context, sessionKey, prRef string) qaSessionHistory {
	sess, err := e.Store.GetQASession(ctx, sessionKey, sessionTTL)
	if err != nil {
		e.log().Warn("argus.qa.session_load_failed", "pr", prRef, "error", err.Error())
		return qaSessionHistory{}
	}
	if sess == nil {
		return qaSessionHistory{}
	}
	var turns []storage.QATurn
	if err := json.Unmarshal([]byte(sess.HistoryJSON), &turns); err != nil {
		e.log().Warn("argus.qa.history_corrupt", "pr", prRef, "error", err.Error())
		turns = nil
	}
	return qaSessionHistory{sess: sess, turns: turns, summary: sess.Summary}
}

// appendTurn 追加封顶 rawTurnsCap（超出部分已由摘要承接，允许 FIFO 淘汰）。
func appendTurn(h []storage.QATurn, question, answer string) []storage.QATurn {
	h = append(h, storage.QATurn{Question: question, Answer: answer, At: time.Now().UTC().Format(time.RFC3339)})
	if len(h) > rawTurnsCap {
		h = h[len(h)-rawTurnsCap:]
	}
	return h
}

// persistTurn 乐观锁落库（深挖修复语义原样保留）：读→追加→覆盖写在并发评论下
// 互相丢轮次；CAS 冲突时重读最新历史把本轮追加其后重放一次（至少不丢本轮），
// 重放路径不重算摘要（冲突罕见，摘要允许滞后一轮）。
func (e *Engine) persistTurn(ctx context.Context, sessionKey, prRef string, prev qaSessionHistory,
	question, answer string, newTurns []storage.QATurn, sum string) {
	var save func(h []storage.QATurn, s string, expect time.Time, check bool)
	save = func(h []storage.QATurn, s string, expect time.Time, check bool) {
		hj, err := json.Marshal(h)
		if err != nil {
			e.log().Warn("argus.qa.session_marshal_failed", "pr", prRef, "error", err.Error())
			return
		}
		written, err := e.Store.SaveQASessionCas(ctx, &storage.QASession{
			SessionKey: sessionKey, HistoryJSON: string(hj), Summary: s,
		}, expect, check)
		if err != nil {
			e.log().Warn("argus.qa.session_save_failed", "pr", prRef, "error", err.Error())
		} else if !written && check {
			if fresh, ferr := e.Store.GetQASession(ctx, sessionKey, sessionTTL); ferr == nil && fresh != nil {
				var fh []storage.QATurn
				if uerr := json.Unmarshal([]byte(fresh.HistoryJSON), &fh); uerr != nil {
					// CAS 重读路径的静默点（v5.3.2 补 Warn）：unmarshal 失败 fh=nil，
					// 落库会把整个会话历史截断为本轮——留痕让损坏可见可排查。
					e.log().Warn("argus.qa.session_reload_malformed", "pr", prRef, "error", uerr.Error(),
						"action", "以本轮为起点覆盖落库（历史已损坏）")
				}
				save(appendTurn(fh, question, answer), s, time.Time{}, false)
				return
			}
			e.log().Warn("argus.qa.session_conflict", "pr", prRef, "action", "重读失败，放弃本轮落库")
		}
	}
	var expect time.Time
	if prev.sess != nil {
		expect = prev.sess.UpdatedAt
	}
	save(newTurns, sum, expect, prev.sess != nil)
}
