package memorykernel

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"argus/internal/memory"

	"github.com/yi-nology/agent-memory/storage/sqlite"
	"github.com/yi-nology/agent-memory/store"
)

func testKernel(t *testing.T, name string) *Kernel {
	t.Helper()
	st, err := sqlite.OpenMemory(name)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	k, err := openWithStore(st, logx.NewSlogLogger("memorykernel-test"))
	if err != nil {
		t.Fatalf("openWithStore: %v", err)
	}
	return k
}

func TestNamespaceOf(t *testing.T) {
	if got := NamespaceOf(memory.OrgRef); got != OrgNamespace {
		t.Fatalf("org ref → %q, want %q", got, OrgNamespace)
	}
	repo := NamespaceOf(memory.Ref{Platform: "gitlab", Owner: "g", Repo: "r"})
	if repo != "gitlab-g-r" {
		t.Fatalf("repo ns = %q", repo)
	}
}

func TestSaveFactRecallRoundtrip(t *testing.T) {
	k := testKernel(t, "rt")
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	err := k.SaveFact(ctx, ns, "pattern-01", "pattern", "[security] SQL 拼接低置信", "命中类似表述请提高举证标准。", at)
	if err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	out := k.RecallIndex(ctx, ns, "", "task-other", "举证", 2000)
	if !strings.Contains(out, "举证") {
		t.Fatalf("recall 未命中事实正文：%q", out)
	}
}

func TestSaveFactRedactsBeforePersist(t *testing.T) {
	k := testKernel(t, "redact")
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	// 形态命中 guardx/logredact 规则表的类 token 串，落库前必须打码。
	secret := "glpat-ABCDEFGHIJKLMNOPQRST"
	err := k.SaveFact(ctx, ns, "pattern-02", "pattern", "样本含凭据", "样本：token="+secret, time.Now())
	if err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	out := k.RecallIndex(ctx, ns, "", "", "凭据", 2000)
	if strings.Contains(out, secret) {
		t.Fatalf("召回结果含未打码凭据：%q", out)
	}
}

func TestClearRepoRemovesFromRecall(t *testing.T) {
	k := testKernel(t, "clear")
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	if err := k.SaveFact(ctx, ns, "pattern-03", "pattern", "d", "待清理事实正文", time.Now()); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	if err := k.ClearRepo(ctx, ns); err != nil {
		t.Fatalf("ClearRepo: %v", err)
	}
	out := k.RecallIndex(ctx, ns, "", "", "待清理", 2000)
	if strings.Contains(out, "待清理事实正文") {
		t.Fatalf("清桶后召回仍命中：%q", out)
	}
}

func TestNilKernelSafe(t *testing.T) {
	var k *Kernel
	if err := k.SaveFact(context.Background(), "ns", "s", "t", "d", "b", time.Now()); err != nil {
		t.Fatalf("nil SaveFact 应零开销，got %v", err)
	}
	if err := k.ClearRepo(context.Background(), "ns"); err != nil {
		t.Fatalf("nil ClearRepo 应零开销，got %v", err)
	}
	if got := k.RecallIndex(context.Background(), "ns", "", "", "", 0); got != "" {
		t.Fatalf("nil RecallIndex 应空串，got %q", got)
	}
	if err := k.Close(); err != nil {
		t.Fatalf("nil Close 应 nil，got %v", err)
	}
}

func TestRecallIndexOrgBucketSplit(t *testing.T) {
	k := testKernel(t, "orgsplit")
	ctx := context.Background()
	at := time.Now()
	if err := k.SaveFact(ctx, OrgNamespace, "cross-abc", "pattern", "跨仓模式", "组织级噪声源正文", at); err != nil {
		t.Fatalf("SaveFact org: %v", err)
	}
	repoNS := store.Namespace("gitlab-g-r")
	if err := k.SaveFact(ctx, repoNS, "pattern-01", "pattern", "仓级模式", "本仓记忆正文", at); err != nil {
		t.Fatalf("SaveFact repo: %v", err)
	}
	out := k.RecallIndex(ctx, repoNS, OrgNamespace, "", "噪声", 2000)
	if !strings.Contains(out, "跨仓常见模式（组织级）") {
		t.Fatalf("组织段标题缺失：%q", out)
	}
}

func TestGCAgesRawEvents(t *testing.T) {
	k := testKernel(t, "gc")
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := k.SaveFact(ctx, ns, "old-01", "pattern", "旧模式", "四十天前的事实正文", old); err != nil {
		t.Fatalf("SaveFact old: %v", err)
	}
	if err := k.SaveFact(ctx, ns, "new-01", "pattern", "新模式", "新鲜事实正文", time.Now()); err != nil {
		t.Fatalf("SaveFact new: %v", err)
	}
	deleted, err := k.GC(ctx, string(ns), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("GC 删除 = %d, want 1（仅超龄事件）", deleted)
	}
	// 新鲜事实仍可召回
	out := k.RecallIndex(ctx, ns, "", "", "新鲜", 2000)
	if !strings.Contains(out, "新鲜事实正文") {
		t.Fatalf("GC 后新鲜事实召回失败：%q", out)
	}
}

func TestGCNilSafe(t *testing.T) {
	var k *Kernel
	n, err := k.GC(context.Background(), "ns", time.Now())
	if n != 0 || err != nil {
		t.Fatalf("nil GC 应 (0,nil)，got (%d,%v)", n, err)
	}
	if err := k.Vacuum(context.Background()); err != nil {
		t.Fatalf("nil Vacuum 应 nil，got %v", err)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nested/deeper/kernel.db" // 父目录不存在——Open 必须自建
	k, err := Open(path, logx.NewSlogLogger("memorykernel-test"))
	if err != nil {
		t.Fatalf("Open 新目录库失败（父目录应自建）: %v", err)
	}
	defer k.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("库文件未创建: %v", err)
	}
}
