package commentcmd

import (
	"encoding/json"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

// TestGitLabNoteCommentRouting 钉评论事件的路由与解析词表：canonical
// comment.created（SDK v0.72+ 七后端统一）与旧词表 issue_comment/cr.note 都必须
// 进评论链路。词表漏配曾让 GitLab 评论命令（@触发词 ask/describe/suppress/fix）
// 整条链路静默落白名单 skip——webhook 到得了、命令永远不处理，且无任何 ERROR
// （v3.114.0 实测修复 GitLab；v3.115.0 扩全平台 canonical，此测试防复发）。
func TestGitLabNoteCommentRouting(t *testing.T) {
	note := func(body string, system bool, iid int) string {
		b, _ := json.Marshal(map[string]any{
			"object_attributes": map[string]any{"note": body, "system": system},
			"merge_request":     map[string]any{"iid": iid},
		})
		return string(b)
	}
	canonical := provider.EventTypeComment + provider.CommentActionCreated

	// canonical（v3.115.0 全平台主路径）：SDK 归一化结构直取，不重解 RawPayload。
	ev := &provider.NormalizedEvent{
		Type:       canonical,
		Comment:    &provider.IssueComment{Body: "@argus 为什么是 high"},
		CR:         &provider.ChangeRequest{Number: "165"},
		RawPayload: json.RawMessage(note("@argus 为什么是 high", false, 165)),
	}
	if !IsCommentEvent(ev) {
		t.Fatal("canonical comment.created 应识别为评论事件（修复前整条 GitLab 评论链路死于此）")
	}
	meta, ok := ParseCommentMeta(ev)
	if !ok || meta.PRNumber != 165 || meta.Body != "@argus 为什么是 high" {
		t.Fatalf("canonical 结构解析失真: ok=%v meta=%+v", ok, meta)
	}

	// canonical 排除态：普通 issue/commit 评论（无 CR）、非法号。
	if _, ok := ParseCommentMeta(&provider.NormalizedEvent{
		Type: canonical, Comment: &provider.IssueComment{Body: "x"}}); ok {
		t.Fatal("无 CR（普通 issue 评论）不应解析为命令评论")
	}
	if _, ok := ParseCommentMeta(&provider.NormalizedEvent{
		Type: canonical, Comment: &provider.IssueComment{Body: "x"},
		CR: &provider.ChangeRequest{Number: "abc"}}); ok {
		t.Fatal("非数字 CR 号不应解析为命令评论")
	}

	// 旧词表兼容：cr.note 走 GitLab RawPayload 形状。
	evOld := &provider.NormalizedEvent{Type: "cr.note", RawPayload: json.RawMessage(note("旧词表", false, 7))}
	if !IsCommentEvent(evOld) {
		t.Fatal("旧词表 cr.note 应保持兼容")
	}
	if m, ok := ParseCommentMeta(evOld); !ok || m.PRNumber != 7 {
		t.Fatalf("旧词表形状应可解析: ok=%v m=%+v", ok, m)
	}

	// 排除态：system note 与非 MR 评论（iid=0）。
	evSys := &provider.NormalizedEvent{Type: "cr.note", RawPayload: json.RawMessage(note("closed", true, 165))}
	if _, ok := ParseCommentMeta(evSys); ok {
		t.Fatal("system note 不应解析为命令评论")
	}
	evIssue := &provider.NormalizedEvent{Type: "cr.note", RawPayload: json.RawMessage(note("x", false, 0))}
	if _, ok := ParseCommentMeta(evIssue); ok {
		t.Fatal("iid=0（非 MR 评论）不应解析为命令评论")
	}
}
