package service

import (
	"encoding/json"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

// TestGitLabNoteCommentRouting 钉 GitLab note 事件的路由词表：canonical
// comment.created 与旧词表 cr.note 都必须进评论链路。词表漏配曾让 GitLab
// 评论命令（@触发词 ask/describe/suppress/fix）整条链路静默落白名单 skip
// ——webhook 到得了、命令永远不处理，且无任何 ERROR（v3.114.0 部署验证期
// 实测修复，此测试防复发）。
func TestGitLabNoteCommentRouting(t *testing.T) {
	note := func(body string, system bool, iid int) string {
		b, _ := json.Marshal(map[string]any{
			"object_attributes": map[string]any{"note": body, "system": system},
			"merge_request":     map[string]any{"iid": iid},
		})
		return string(b)
	}
	canonical := provider.EventTypeComment + provider.CommentActionCreated

	ev := &provider.NormalizedEvent{Type: canonical, RawPayload: json.RawMessage(note("@argus 为什么是 high", false, 165))}
	if !isCommentEvent(ev) {
		t.Fatal("canonical comment.created 应识别为评论事件（修复前整条 GitLab 评论链路死于此）")
	}
	meta, ok := parseCommentMeta(ev)
	if !ok || meta.PRNumber != 165 || meta.Body != "@argus 为什么是 high" {
		t.Fatalf("canonical 形状解析失真: ok=%v meta=%+v", ok, meta)
	}

	evOld := &provider.NormalizedEvent{Type: "cr.note", RawPayload: json.RawMessage(note("旧词表", false, 7))}
	if !isCommentEvent(evOld) {
		t.Fatal("旧词表 cr.note 应保持兼容")
	}
	if _, ok := parseCommentMeta(evOld); !ok {
		t.Fatal("旧词表形状应可解析")
	}

	// system note（GitLab 系统动作）与非 PR 评论（iid=0）不进命令链路。
	evSys := &provider.NormalizedEvent{Type: canonical, RawPayload: json.RawMessage(note("closed", true, 165))}
	if _, ok := parseCommentMeta(evSys); ok {
		t.Fatal("system note 不应解析为命令评论")
	}
	evIssue := &provider.NormalizedEvent{Type: canonical, RawPayload: json.RawMessage(note("x", false, 0))}
	if _, ok := parseCommentMeta(evIssue); ok {
		t.Fatal("iid=0（非 MR 评论）不应解析为命令评论")
	}
}
