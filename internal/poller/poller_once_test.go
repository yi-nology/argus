package poller

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// ---------- pollOnce 行为套件（v3.112.0）：此前 pollOnce 零直接锚点，
// 而 poller 是 215 试点常开的生产任务源（golden 只覆盖 webhook 源）。 ----------

// listFakePR 打桩平台：按页返回预设 CR 集，记录收到的查询参数。
type listFakePR struct {
	provider.Provider

	pages [][]*provider.ChangeRequest
	got   []provider.ListCROptions
}

func (f *listFakePR) ListCRs(_ context.Context, opts provider.ListCROptions) ([]*provider.ChangeRequest, *int, error) {
	f.got = append(f.got, opts)
	total := len(f.pages)
	if opts.Page < 1 || opts.Page > len(f.pages) {
		return nil, &total, nil
	}
	return f.pages[opts.Page-1], &total, nil
}

func cr(num, sha string, state provider.CRState, draft bool) *provider.ChangeRequest {
	return &provider.ChangeRequest{Number: num, HeadSHA: sha, State: state, Draft: draft}
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "poller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newPoller(t *testing.T, plat *listFakePR, store *storage.Store, bus *progress.Bus, skipDraft bool) *Poller {
	t.Helper()
	return &Poller{
		SDK:       argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{"gitea": plat}),
		Store:     store,
		Targets:   []Target{{Platform: "gitea", Owner: "o", Repo: "r"}},
		Interval:  time.Hour, // 不真正起 loop；测试直调 pollOnce
		SkipDraft: skipDraft,
		Log:       logx.Nop(),
		Bus:       bus,
	}
}

// subscribeEvents 先订阅（Bus 只投递发布时已在册的消费者，发布后订阅收不到）。
func subscribeEvents(t *testing.T, bus *progress.Bus) (<-chan progress.Event, func()) {
	t.Helper()
	ch, stop := bus.Subscribe(context.Background())
	return ch, stop
}

// collectEvents 非阻塞排空事件通道。
func collectEvents(ch <-chan progress.Event) []progress.Event {
	var out []progress.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func pendingTasks(t *testing.T, store *storage.Store) []storage.Task {
	t.Helper()
	tasks, err := store.ListTasks(context.Background(), storage.StatusPending, 200)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

// TestPollOnce_DiscoverAndDedup 发现 → 落库 pending + 广播；同 SHA 二扫幂等跳过。
func TestPollOnce_DiscoverAndDedup(t *testing.T) {
	plat := &listFakePR{pages: [][]*provider.ChangeRequest{
		{cr("7", "sha-7", provider.CRStateOpened, false)},
	}}
	store := newTestStore(t)
	bus := progress.NewBus()
	evCh, stopEv := subscribeEvents(t, bus)
	defer stopEv()
	p := newPoller(t, plat, store, bus, false)
	ctx := context.Background()

	p.pollOnce(ctx)
	tasks := pendingTasks(t, store)
	if len(tasks) != 1 {
		t.Fatalf("应建 1 个任务, got %d", len(tasks))
	}
	tk := tasks[0]
	if tk.Kind != "requirement_compliance" || tk.Status != storage.StatusPending ||
		tk.HeadSHA != "sha-7" || tk.Number != "7" || tk.EventType != "" {
		t.Errorf("任务字段失真: %+v", tk)
	}
	evs := collectEvents(evCh)
	if len(evs) != 1 || evs[0].Type != progress.EventTaskCreated || evs[0].TaskID != tk.ID {
		t.Fatalf("应广播一条 task.created 且指向新任务: %+v", evs)
	}

	// 二扫：同 SHA 在飞 → 幂等，不叠加任务、不重复广播。
	p.pollOnce(ctx)
	if tasks = pendingTasks(t, store); len(tasks) != 1 {
		t.Fatalf("同 SHA 二扫不应叠加任务, got %d", len(tasks))
	}
	if evs := collectEvents(evCh); len(evs) != 0 {
		t.Errorf("幂等跳过不应广播: %+v", evs)
	}
}

// TestPollOnce_Filters state 过滤防线/draft 跳过/空 SHA 跳过三道闸。
func TestPollOnce_Filters(t *testing.T) {
	plat := &listFakePR{pages: [][]*provider.ChangeRequest{
		{
			cr("1", "sha-1", provider.CRStateMerged, false), // 非 open：state 过滤失灵防线
			cr("2", "sha-2", provider.CRStateOpened, true),  // draft
			cr("3", "", provider.CRStateOpened, false),      // 空 SHA
			cr("4", "sha-4", provider.CRStateOpened, false), // 唯一应建任务
		},
	}}
	store := newTestStore(t)
	p := newPoller(t, plat, store, progress.NewBus(), true)
	p.pollOnce(context.Background())

	tasks := pendingTasks(t, store)
	if len(tasks) != 1 || tasks[0].Number != "4" {
		t.Fatalf("过滤后应只剩 4 号任务: %+v", tasks)
	}
}

// TestPollOnce_PagingTruncated 三页打满：截断告警路径不炸，任务照建。
func TestPollOnce_PagingTruncated(t *testing.T) {
	full := func(tag string) []*provider.ChangeRequest {
		out := make([]*provider.ChangeRequest, 0, 30)
		for i := 0; i < 30; i++ {
			out = append(out, cr(fmt.Sprintf("%s-%d", tag, i), fmt.Sprintf("sha-%s-%d", tag, i),
				provider.CRStateOpened, false))
		}
		return out
	}
	plat := &listFakePR{pages: [][]*provider.ChangeRequest{full("a"), full("b"), full("c")}}
	store := newTestStore(t)
	p := newPoller(t, plat, store, progress.NewBus(), false)
	p.pollOnce(context.Background())

	if tasks := pendingTasks(t, store); len(tasks) != 90 {
		t.Fatalf("三页 90 个 PR 应全部建任务, got %d", len(tasks))
	}
	if len(plat.got) != 3 {
		t.Errorf("应恰好翻 3 页, got %d", len(plat.got))
	}
}

// TestPollOnce_ListFailed 平台查询失败（未配置的 Provider）：不建任务不 panic。
func TestPollOnce_ListFailed(t *testing.T) {
	store := newTestStore(t)
	p := newPoller(t, &listFakePR{}, store, progress.NewBus(), false)
	p.Targets = []Target{{Platform: "nope", Owner: "o", Repo: "r"}}
	p.pollOnce(context.Background())
	if tasks := pendingTasks(t, store); len(tasks) != 0 {
		t.Errorf("列表失败不应建任务: %+v", tasks)
	}
}

// TestParseTargets 解析器：合法/非法混合、大小写归一。
func TestParseTargets(t *testing.T) {
	targets, errs := ParseTargets("Gitea:o/r, bad-item, , gitlab:grp/sub/proj, x:y")
	if len(targets) != 2 {
		t.Fatalf("应解析出 2 个合法目标: %+v", targets)
	}
	if targets[0].Platform != "gitea" || targets[0].Owner != "o" || targets[0].Repo != "r" {
		t.Errorf("大小写归一失真: %+v", targets[0])
	}
	if len(errs) != 2 {
		t.Fatalf("应记 2 个非法项: %v", errs)
	}
}
