package context

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"
)

// v3.92.0：NoteManager（MR Notes API）优先路由——GitLab 的 IssueManager 评论
// 方法对 MR 必 404；实现了 NoteManager 的平台（当前仅 gitlab）Update/List 都
// 必须走它，且原生成功时不再触碰 IssueManager / DiffManager 换新兜底。

// noteFake 只实现路由验证触达的面；未覆盖方法借嵌入接口 panic 兜底。
type noteFake struct {
	provider.Provider
	provider.IssueManager // 嵌入 nil：被误调用即 panic（金丝雀）

	mu           sync.Mutex
	notes        map[int64]string // noteID -> body（UpdateNote 落点）
	updatedIssue bool             // IssueManager.UpdateIssueComment 被调用过
	listNotesN   int
}

func (f *noteFake) UpdateNote(_ context.Context, _, _, _ string, noteID, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, err := strconv.ParseInt(noteID, 10, 64)
	if err != nil {
		return nil, err
	}
	f.notes[id] = body
	return &provider.IssueComment{ID: id, Body: body}, nil
}

func (f *noteFake) ListNotes(_ context.Context, _, _, _ string) ([]*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listNotesN++
	return []*provider.IssueComment{{ID: 1, Body: "mr-note"}}, nil
}

func (f *noteFake) UpdateIssueComment(_ context.Context, _, _, _ string, _ int64, _ string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updatedIssue = true
	return nil, errors.New("issue notes api should not be reached")
}

func (f *noteFake) ListIssueComments(_ context.Context, _, _, _ string) ([]*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, errors.New("issue notes api should not be reached")
}

// 回退面回归：无 NoteManager 的平台走 IssueManager 原路——由 poster 流程
// 测试（fakePlatform 无 NoteManager）覆盖，此处不重复。

func TestUpdateIssueComment_PrefersNoteManager(t *testing.T) {
	nf := &noteFake{notes: map[int64]string{}}
	sdk := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"gitlab": nf})
	c, err := sdk.UpdateIssueComment(context.Background(), "gitlab", "obs", "repo", "234", 77, "原地更新")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != 77 || c.Body != "原地更新" {
		t.Fatalf("应返回原 ID（真·原地更新）: %+v", c)
	}
	if nf.notes[77] != "原地更新" {
		t.Fatalf("UpdateNote 未落点: %+v", nf.notes)
	}
	if nf.updatedIssue {
		t.Fatal("NoteManager 命中时不得再走 IssueManager")
	}
}

func TestListIssueComments_PrefersNoteManager(t *testing.T) {
	nf := &noteFake{notes: map[int64]string{}}
	sdk := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"gitlab": nf})
	cs, err := sdk.ListIssueComments(context.Background(), "gitlab", "obs", "repo", "234")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Body != "mr-note" || nf.listNotesN != 1 {
		t.Fatalf("应走 ListNotes: %+v n=%d", cs, nf.listNotesN)
	}
}

// nopLogx 静默日志（NewSDKCtxStubbed 需要 logx.Logger 完整接口）。
type nopLogx struct{}

func (nopLogx) Debug(string, ...any)                      {}
func (nopLogx) Info(string, ...any)                       {}
func (nopLogx) Warn(string, ...any)                       {}
func (nopLogx) Error(string, ...any)                      {}
func (nopLogx) Debugf(string, ...any)                     {}
func (nopLogx) Infof(string, ...any)                      {}
func (nopLogx) Warnf(string, ...any)                      {}
func (nopLogx) Errorf(string, ...any)                     {}
func (nopLogx) CtxDebug(context.Context, string, ...any)  {}
func (nopLogx) CtxInfo(context.Context, string, ...any)   {}
func (nopLogx) CtxWarn(context.Context, string, ...any)   {}
func (nopLogx) CtxError(context.Context, string, ...any)  {}
func (nopLogx) CtxDebugf(context.Context, string, ...any) {}
func (nopLogx) CtxInfof(context.Context, string, ...any)  {}
func (nopLogx) CtxWarnf(context.Context, string, ...any)  {}
func (nopLogx) CtxErrorf(context.Context, string, ...any) {}
func (nopLogx) WithCtx(context.Context) logx.Logger       { return nopLogx{} }
func (nopLogx) WithService(string) logx.Logger            { return nopLogx{} }
func (nopLogx) WithAttrs(...logx.Attr) logx.Logger        { return nopLogx{} }
func (nopLogx) WithGroup(string) logx.Logger              { return nopLogx{} }
func (nopLogx) SetLevel(logx.Level)                       {}
