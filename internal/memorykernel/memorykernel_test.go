package memorykernel

import (
	"context"
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
