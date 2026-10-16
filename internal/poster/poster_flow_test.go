package poster

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/reportview"
	"argus/internal/storage"
)

// ---------- 测试替身 ----------

// testNopLogger logx.Logger 空实现（poster 深路径需要非 nil Log）。
type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any)                      {}
func (testNopLogger) Info(string, ...any)                       {}
func (testNopLogger) Warn(string, ...any)                       {}
func (testNopLogger) Error(string, ...any)                      {}
func (testNopLogger) Debugf(string, ...any)                     {}
func (testNopLogger) Infof(string, ...any)                      {}
func (testNopLogger) Warnf(string, ...any)                      {}
func (testNopLogger) Errorf(string, ...any)                     {}
func (testNopLogger) CtxDebug(context.Context, string, ...any)  {}
func (testNopLogger) CtxInfo(context.Context, string, ...any)   {}
func (testNopLogger) CtxWarn(context.Context, string, ...any)   {}
func (testNopLogger) CtxError(context.Context, string, ...any)  {}
func (testNopLogger) CtxDebugf(context.Context, string, ...any) {}
func (testNopLogger) CtxInfof(context.Context, string, ...any)  {}
func (testNopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (testNopLogger) CtxErrorf(context.Context, string, ...any) {}
func (testNopLogger) WithCtx(context.Context) logx.Logger       { return testNopLogger{} }
func (testNopLogger) WithService(string) logx.Logger            { return testNopLogger{} }
func (testNopLogger) WithAttrs(...logx.Attr) logx.Logger        { return testNopLogger{} }
func (testNopLogger) WithGroup(string) logx.Logger              { return testNopLogger{} }
func (testNopLogger) SetLevel(logx.Level)                       {}

// fakePlatform 只实现 poster 触达的平台面；未覆盖方法借嵌入接口 panic 兜底。
type fakePlatform struct {
	provider.Provider
	provider.IssueManager
	provider.ReviewManager

	mu sync.Mutex

	// CreateReview 每次调用：err 非 nil 即失败（驱动三级降级）；连续失败用 failReviews 计数。
	createdReviews []provider.CreateReviewOptions
	reviewErrs     []error // 按次出队；耗尽后走 reviewDefaultErr（nil=成功）

	reviews []provider.Review // ListReviews 返回

	comments    []*provider.IssueComment
	updateErrs  map[int64]error // UpdateIssueComment 按 ID 注入错误
	updated     map[int64]string
	nextID      int64
	createNoteN int
	createNote  bool // true = 走 DiffManager.CreateNote；false = 落 IssueManager
	noteErr     error
	listErr     error
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		nextID:     100,
		updateErrs: map[int64]error{},
		updated:    map[int64]string{},
		createNote: true,
	}
}

func (f *fakePlatform) Platform() provider.Platform { return "gitea" }
func (f *fakePlatform) Capabilities() provider.CapabilitySet {
	return provider.CapabilitySet{Issues: true, Reviews: true}
}

func (f *fakePlatform) CreateReview(_ context.Context, _, _, _ string, opts provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdReviews = append(f.createdReviews, opts)
	if len(f.reviewErrs) > 0 {
		err := f.reviewErrs[0]
		f.reviewErrs = f.reviewErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &provider.ReviewResult{ID: "r-1"}, nil
}

func (f *fakePlatform) ListReviews(context.Context, string, string, string) ([]provider.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.Review(nil), f.reviews...), nil
}

func (f *fakePlatform) ListIssueComments(context.Context, string, string, string) ([]*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]*provider.IssueComment(nil), f.comments...), nil
}

func (f *fakePlatform) CreateNote(_ context.Context, _, _, _, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.createNote {
		return "", fmt.Errorf("note unsupported")
	}
	if f.noteErr != nil {
		return "", f.noteErr
	}
	f.createNoteN++
	f.nextID++
	c := &provider.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return fmt.Sprintf("%d", c.ID), nil
}

func (f *fakePlatform) CreateIssueComment(_ context.Context, _, _, _, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	c := &provider.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return c, nil
}

// DeleteNote v3.91.2：sdkctx UpdateIssueComment 的 GitLab 换新降级
// （CreateNote+DeleteNote）会触达本方法——未实现会踩嵌入接口 panic 兜底。
func (f *fakePlatform) DeleteNote(_ context.Context, _, _, _ string, noteID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, err := strconv.ParseInt(noteID, 10, 64)
	if err != nil {
		return err
	}
	for i, c := range f.comments {
		if c.ID == id {
			f.comments = append(f.comments[:i], f.comments[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("note %d not found", id)
}

func (f *fakePlatform) UpdateIssueComment(_ context.Context, _, _, _ string, id int64, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.updateErrs[id]; ok {
		if err != nil {
			return nil, err
		}
	}
	f.updated[id] = body
	return &provider.IssueComment{ID: id, Body: body}, nil
}

func (f *fakePlatform) reviewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.createdReviews)
}

func (f *fakePlatform) lastReview() provider.CreateReviewOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createdReviews) == 0 {
		return provider.CreateReviewOptions{}
	}
	return f.createdReviews[len(f.createdReviews)-1]
}

// ---------- 装配 ----------

func newTestPoster(t *testing.T, fp *fakePlatform) (*Poster, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sdk := argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{"gitea": fp})
	return &Poster{
		SDK: sdk, Store: store, Version: "v-test",
		Log: testNopLogger{}, BotUsername: "",
	}, store
}

func mkTask(id string) *storage.Task {
	return &storage.Task{
		ID: id, Kind: "code_review", Platform: "gitea",
		Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: "sha-1", Status: storage.StatusRunning,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
}

func mkPR() plugin.PRContext {
	return plugin.PRContext{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
		HeadSHA: "sha-1",
	}
}

func mkRep(taskID string) reportview.Report {
	return reportview.Report{
		TaskID: taskID, HeadSHA: "sha-1", Version: "v-test",
		VerdictText: "💬 无阻塞问题",
	}
}

func mkDiff() *plugin.DiffContext {
	return &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "pkg/a.go", Included: true},
		{Path: "pkg/b.go", Included: true},
		{Path: "docs/README.md", Included: false},
	}}
}

// ---------- prepareInline ----------

// 行内候选过滤 + 路径归一化 + 严重度排序 + max_comments 钳制。
func TestPrepareInline_FilterNormalizeSortClamp(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.Findings = []plugin.Finding{
		{File: "a.go", Line: 0, Severity: "high", Comment: "无行号"},            // 剔除
		{File: "", Line: 3, Severity: "high", Comment: "无文件"},                // 剔除
		{File: "pkg/a.go", Line: 10, Severity: "medium", Comment: "精确命中"},    // 保留
		{File: "b.go", Line: 20, Severity: "high", Comment: "尾段归一化"},         // → pkg/b.go (high)
		{File: "README.md", Line: 1, Severity: "low", Comment: "非 Included"}, // uniqueSuffix 只在 Included 里找 → 无命中剔除
		{File: "ghost.go", Line: 5, Severity: "high", Comment: "幻觉路径"},       // 剔除
	}
	comments := p.prepareInline(mkTask("t-1"), mkDiff(), &rep)

	if len(comments) != 2 {
		t.Fatalf("应保留 2 条，实际 %d: %+v", len(comments), comments)
	}
	// 严重度降序：high（尾段归一）先于 medium（Rank 只认 high/medium/low 词表）
	if comments[0].Path != "pkg/b.go" || comments[0].Line != 20 {
		t.Fatalf("首条应是归一化后的 high: %+v", comments[0])
	}
	if comments[1].Path != "pkg/a.go" {
		t.Fatalf("次条应是精确命中: %+v", comments[1])
	}
	// 路径已归一
	if !strings.Contains(comments[0].Body, "尾段归一化") {
		t.Fatalf("评论正文应含原意见: %q", comments[0].Body)
	}
	// 剔除 2 条（README/ghost 在 diff 校验阶段）→ Degraded 注明；
	// 无行号/无文件在更早的候选过滤已丢，不计入路径剔除数。
	if len(rep.Degraded) == 0 || !strings.Contains(rep.Degraded[0], "2 条") {
		t.Fatalf("剔除应进 Degraded: %v", rep.Degraded)
	}
}

// 唯一尾段匹配：多命中 / 段边界防误配（pcap.go 不配 pcapDownload.go）。
func TestPrepareInline_SuffixAmbiguityKeptOut(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.Findings = []plugin.Finding{
		{File: "a.go", Line: 1, Severity: "high", Comment: "多义"},
	}
	diff := &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "x/a.go", Included: true},
		{Path: "y/a.go", Included: true},
	}}
	comments := p.prepareInline(mkTask("t-1"), diff, &rep)
	if len(comments) != 0 {
		t.Fatalf("多命中应剔除（错发位置比不发更糟）: %+v", comments)
	}
	// 段边界：a.go 不应误配 ba.go
	rep2 := mkRep("t-1")
	rep2.Findings = []plugin.Finding{{File: "a.go", Line: 1, Severity: "high", Comment: "段边界"}}
	diff2 := &plugin.DiffContext{Files: []plugin.DiffFile{
		{Path: "pkg/ba.go", Included: true},
	}}
	if cs := p.prepareInline(mkTask("t-1"), diff2, &rep2); len(cs) != 0 {
		t.Fatalf("段边界外后缀不得匹配: %+v", cs)
	}
}

// max_comments 钳制：只保留严重度 top N，其余进 Degraded。
func TestPrepareInline_MaxCommentsClamp(t *testing.T) {
	p, _ := newTestPoster(t, newFakePlatform())
	rep := mkRep("t-1")
	rep.MaxInlineComments = 1
	rep.Findings = []plugin.Finding{
		{File: "pkg/a.go", Line: 1, Severity: "low", Comment: "low"},
		{File: "pkg/b.go", Line: 2, Severity: "high", Comment: "high"},
	}
	comments := p.prepareInline(mkTask("t-1"), mkDiff(), &rep)
	if len(comments) != 1 {
		t.Fatalf("应钳到 1 条: %+v", comments)
	}
	if !strings.Contains(comments[0].Body, "high") {
		t.Fatalf("应保留 high: %q", comments[0].Body)
	}
	clamped := false
	for _, d := range rep.Degraded {
		if strings.Contains(d, "max_comments=1") {
			clamped = true
		}
	}
	if !clamped {
		t.Fatalf("钳制应进 Degraded: %v", rep.Degraded)
	}
}

// ---------- sendReview 三级降级 ----------

// verdict → 评审事件映射；成功返回行内条数并落 review_posted。
func TestSendReview_VerdictMappingAndMark(t *testing.T) {
	for verdict, wantEvent := range map[string]string{
		"request_changes": "REQUEST_CHANGES",
		"approve":         "APPROVE",
		"comment":         "COMMENT",
		"":                "COMMENT",
	} {
		fp := newFakePlatform()
		p, store := newTestPoster(t, fp)
		task := mkTask("t-" + wantEvent)
		if err := store.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		rep := mkRep(task.ID)
		rep.VerdictText = "结论"
		n := p.sendReview(context.Background(), mkPR(), rep, verdict,
			[]provider.ReviewComment{{Path: "pkg/a.go", Line: 1, Body: "x"}})
		if n != 1 {
			t.Fatalf("verdict=%q 成功应返回 1 条行内: %d", verdict, n)
		}
		if got := fp.lastReview().Event; got != wantEvent {
			t.Fatalf("verdict=%q → event=%s want %s", verdict, got, wantEvent)
		}
		got, err := store.GetTask(context.Background(), task.ID)
		if err != nil || got == nil || !got.ReviewPosted {
			t.Fatalf("应落 review_posted: %v %+v", err, got)
		}
	}
}

// review_posted 库标记短路：不再 CreateReview。
func TestSendReview_ShortCircuitByFlag(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	task := mkTask("t-flag")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReviewPosted(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("短路应返回 0: %d", n)
	}
	if fp.reviewCount() != 0 {
		t.Fatalf("短路后不应 CreateReview: %d", fp.reviewCount())
	}
}

// 平台锚点查询回退：ListReviews 已有同 task_id 评审 → 跳过。
func TestSendReview_ShortCircuitByAnchor(t *testing.T) {
	fp := newFakePlatform()
	fp.reviews = []provider.Review{
		{ID: 1, Body: "无关"},
		{ID: 2, Body: "x <!-- argus:review task_id=t-anc head=sha-1 verdict=comment -->"},
	}
	p, store := newTestPoster(t, fp)
	task := mkTask("t-anc")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("锚点命中应跳过: %d", n)
	}
	if fp.reviewCount() != 0 {
		t.Fatalf("锚点命中后不应 CreateReview: %d", fp.reviewCount())
	}
}

// 三级降级：blocking 拒绝 → COMMENT → 无行内纯评论。
func TestSendReview_ThreeLevelFallback(t *testing.T) {
	fp := newFakePlatform()
	blocking := fmt.Errorf("reject your own pull is not allowed")
	inlineBad := fmt.Errorf("422: comment is invalid")
	fp.reviewErrs = []error{blocking, inlineBad, nil} // ①②败 ③成
	p, store := newTestPoster(t, fp)
	task := mkTask("t-fb")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	rep.VerdictText = "⛔ 阻塞"
	comments := []provider.ReviewComment{{Path: "pkg/a.go", Line: 1, Body: "x"}}

	n := p.sendReview(context.Background(), mkPR(), rep, "request_changes", comments)
	if n != 0 {
		t.Fatalf("降级路径不计行内成功数（整单含 comments 的调用失败）: %d", n)
	}
	if fp.reviewCount() != 3 {
		t.Fatalf("应尝试 3 次 CreateReview: %d", fp.reviewCount())
	}
	last := fp.lastReview()
	if last.Event != "COMMENT" {
		t.Fatalf("最终应为 COMMENT: %s", last.Event)
	}
	if len(last.Comments) != 0 {
		t.Fatalf("兜底应去掉行内 comments: %+v", last.Comments)
	}
	if !strings.Contains(last.Body, "平台限制") {
		t.Fatalf("降级说明应保留: %q", last.Body)
	}
}

// 全部降级失败 → 0 条且不 panic（report 面已计失败）。
func TestSendReview_AllFallbacksFail(t *testing.T) {
	fp := newFakePlatform()
	always := fmt.Errorf("boom")
	fp.reviewErrs = []error{always, always, always}
	p, store := newTestPoster(t, fp)
	task := mkTask("t-all-fail")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep(task.ID)
	if n := p.sendReview(context.Background(), mkPR(), rep, "comment", nil); n != 0 {
		t.Fatalf("全失败应返回 0: %d", n)
	}
	if fp.reviewCount() != 3 {
		t.Fatalf("仍应尝试三次: %d", fp.reviewCount())
	}
}

// ---------- postSummary 四级降级 ----------

func TestPostSummary_UpdateByCachedID(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	id, err := p.postSummary(context.Background(), mkPR(), "## 报告")
	if err != nil || id != 42 {
		t.Fatalf("应回缓存 ID: id=%d err=%v", id, err)
	}
	if fp.updated[42] != "## 报告" {
		t.Fatalf("应更新评论 42: %v", fp.updated)
	}
	if fp.createNoteN != 0 {
		t.Fatalf("不应新建: %d", fp.createNoteN)
	}
}

// 404（评论被删）清缓存后走新建。
func TestPostSummary_CachedGoneClearsAndCreates(t *testing.T) {
	fp := newFakePlatform()
	fp.updateErrs[42] = fmt.Errorf("404: %w", provider.ErrNotFound)
	p, store := newTestPoster(t, fp)
	if err := store.UpsertReviewTarget(context.Background(), "gitea", "acme", "app", "7", 42, "sha-1"); err != nil {
		t.Fatal(err)
	}
	id, err := p.postSummary(context.Background(), mkPR(), "## 新报告")
	if err != nil {
		t.Fatal(err)
	}
	if id == 42 {
		t.Fatalf("缓存已失效，不应再返回 42")
	}
	target, _ := store.GetReviewTarget(context.Background(), "gitea", "acme", "app", "7")
	if target != nil && target.LastSummaryCommentID == 42 {
		t.Fatalf("404 后应清除缓存: %+v", target)
	}
	// v3.91.2 GitLab 换新降级：Update 404 → CreateNote+DeleteNote——旧评论应被
	// 删除（消灭重复报告），定位回写到新 ID。
	for _, c := range fp.comments {
		if c.ID == 42 {
			t.Fatalf("旧评论 42 应被换新删除，仍存在: %+v", fp.comments)
		}
	}
	if target == nil || target.LastSummaryCommentID != id {
		t.Fatalf("定位应回写新 ID %d: %+v", id, target)
	}
}

// 扫描兜底：严格报告头 + 最新匹配胜出；第三方同前缀评论不劫持。
func TestPostSummary_ScanStrictHeader(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*provider.IssueComment{
		{ID: 10, Body: "<!-- argus:report task_id=other head=x ver=v1 -->\n旧", Author: &provider.CRUser{Username: "bot"}},
		{ID: 11, Body: "普通评论 <!-- argus:report task_id=t head=s ver=v1 -->", Author: &provider.CRUser{Username: "bot"}}, // 不独占一行
		{ID: 12, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n好", Author: &provider.CRUser{Username: "bot"}},
		{ID: 13, Body: "<!-- argus:report task_id=t2 head=s ver=v1 -->\n更新", Author: &provider.CRUser{Username: "bot"}},
	}
	p, _ := newTestPoster(t, fp)
	id, err := p.postSummary(context.Background(), mkPR(), "## 扫描更新")
	if err != nil {
		t.Fatal(err)
	}
	if id != 13 {
		t.Fatalf("应取最后一条严格头匹配: id=%d", id)
	}
	if fp.updated[13] != "## 扫描更新" {
		t.Fatalf("应更新 13: %v", fp.updated)
	}
}

// BotUsername 作者校验：他人同头评论不劫持。
func TestPostSummary_ScanBotAuthorFilter(t *testing.T) {
	fp := newFakePlatform()
	fp.comments = []*provider.IssueComment{
		{ID: 20, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n劫持", Author: &provider.CRUser{Username: "attacker"}},
		{ID: 21, Body: "<!-- argus:report task_id=t head=s ver=v1 -->\n本尊", Author: &provider.CRUser{Username: "argus-bot"}},
	}
	p, _ := newTestPoster(t, fp)
	p.BotUsername = "argus-bot"
	id, err := p.postSummary(context.Background(), mkPR(), "## 作者过滤")
	if err != nil {
		t.Fatal(err)
	}
	if id != 21 {
		t.Fatalf("应命中 bot 自己的评论: id=%d", id)
	}
}

// 历史缓存兜底：KnownCommentIDs 直连更新（①②不中时），新→旧遍历、失败跳过。
func TestPostSummary_KnownCommentsFallback(t *testing.T) {
	fp := newFakePlatform()
	// ① 无 last_summary_comment_id；② 扫描不中（评论不在第一页/无严格头）
	p, store := newTestPoster(t, fp)
	for _, id := range []int64{30, 31, 32} {
		if err := store.AppendReviewTargetComment(context.Background(), "gitea", "acme", "app", "7", id); err != nil {
			t.Fatal(err)
		}
	}
	// 缓存新→旧：先试 32（失败→v3.91.2 换新成功）——换新直接命中，31/30 不再触达
	fp.updateErrs[32] = fmt.Errorf("500")
	id, err := p.postSummary(context.Background(), mkPR(), "## 缓存兜底")
	if err != nil {
		t.Fatal(err)
	}
	if id == 32 || id == 0 {
		t.Fatalf("32 换新成功应返回新 ID: %d", id)
	}
	if _, tried := fp.updated[31]; tried {
		t.Fatal("32 换新成功后不应再试 31")
	}
	if _, tried := fp.updated[30]; tried {
		t.Fatal("32 换新成功后不应再试更旧的 30")
	}
	for _, c := range fp.comments {
		if c.ID == 32 {
			t.Fatalf("旧评论 32 应被换新删除: %+v", fp.comments)
		}
	}
}

// 扫描失败（SDK 报错）→ 降级新建，不挂死。
func TestPostSummary_ScanErrorCreates(t *testing.T) {
	fp := newFakePlatform()
	fp.listErr = fmt.Errorf("list boom")
	p, _ := newTestPoster(t, fp)
	id, err := p.postSummary(context.Background(), mkPR(), "## 新建")
	if err != nil || id == 0 {
		t.Fatalf("扫描失败应新建: id=%d err=%v", id, err)
	}
	if fp.createNoteN != 1 {
		t.Fatalf("应新建一条: %d", fp.createNoteN)
	}
}

// ---------- Post 主路径 ----------

// staleness 门控：存在更晚非 failed 任务 → 不回帖。
func TestPost_StaleSkip(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	old := mkTask("t-old")
	if err := store.CreateTask(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	newer := mkTask("t-new")
	newer.CreatedAt = time.Now().UTC().Add(time.Minute)
	newer.HeadSHA = "sha-2"
	if err := store.CreateTask(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	res, err := p.Post(context.Background(), old, mkPR(), mkDiff(), "comment", mkRep("t-old"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stale {
		t.Fatal("应命中 staleness")
	}
	if fp.reviewCount() != 0 || fp.createNoteN != 0 {
		t.Fatalf("stale 不应回帖: reviews=%d notes=%d", fp.reviewCount(), fp.createNoteN)
	}
}

// 完整路径：评审 + 总结 + review_targets 落账。
func TestPost_HappyPath(t *testing.T) {
	fp := newFakePlatform()
	p, store := newTestPoster(t, fp)
	task := mkTask("t-ok")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rep := mkRep("t-ok")
	rep.Findings = []plugin.Finding{
		{File: "pkg/a.go", Line: 3, Severity: "high", Comment: "问题", By: "correctness"},
	}
	res, err := p.Post(context.Background(), task, mkPR(), mkDiff(), "comment", rep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stale {
		t.Fatal("不应 stale")
	}
	if res.InlinePosted != 1 {
		t.Fatalf("行内 1 条: %d", res.InlinePosted)
	}
	if res.SummaryCommentID == 0 {
		t.Fatal("应返回总结评论 ID")
	}
	target, err := store.GetReviewTarget(context.Background(), "gitea", "acme", "app", "7")
	if err != nil || target == nil {
		t.Fatalf("应落 review_targets: %v", err)
	}
	if target.LastSummaryCommentID != res.SummaryCommentID {
		t.Fatalf("target ID 不一致: %+v vs %d", target, res.SummaryCommentID)
	}
	if target.LastHeadSHA != "sha-1" {
		t.Fatalf("head 应落账: %s", target.LastHeadSHA)
	}
	// 二次 Post（同任务）应短路评审、原地更新总结
	res2, err := p.Post(context.Background(), task, mkPR(), mkDiff(), "comment", rep)
	if err != nil {
		t.Fatal(err)
	}
	if fp.reviewCount() != 1 {
		t.Fatalf("幂等：评审只发一次: %d", fp.reviewCount())
	}
	if res2.SummaryCommentID != res.SummaryCommentID {
		t.Fatalf("总结应原地更新: %d vs %d", res2.SummaryCommentID, res.SummaryCommentID)
	}
}
