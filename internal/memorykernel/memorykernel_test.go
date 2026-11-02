package memorykernel

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/agent-memory/ports"
	"github.com/yi-nology/agent-memory/storage/sqlite"
	"github.com/yi-nology/agent-memory/store"
	"github.com/yi-nology/agent-memory/types"
)

func testKernel(t *testing.T, name string) *Kernel {
	t.Helper()
	st, err := sqlite.OpenMemory(name)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	k, err := openWithStore(st, nil, logx.Nop())
	if err != nil {
		t.Fatalf("openWithStore: %v", err)
	}
	return k
}

func TestNamespaceOf(t *testing.T) {
	if got := NamespaceOf(OrgRef); got != OrgNamespace {
		t.Fatalf("org ref → %q, want %q", got, OrgNamespace)
	}
	repo := NamespaceOf(Ref{Platform: "gitlab", Owner: "g", Repo: "r"})
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
	k, err := Open(path, nil, logx.Nop())
	if err != nil {
		t.Fatalf("Open 新目录库失败（父目录应自建）: %v", err)
	}
	defer k.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("库文件未创建: %v", err)
	}
}

// TestSaveFactLessonKind 时效语义钉（v3.177.0 memory v0.8 FactKind）：dream 模式
// 是长期经验——落库 Kind 必须是 FactLesson（不因时间失效、不被事件窗口挡掉）。
func TestSaveFactLessonKind(t *testing.T) {
	k := testKernel(t, "lesson-kind")
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	if err := k.SaveFact(ctx, ns, "pattern-01", "pattern", "教训描述", "教训正文", time.Now()); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	card, err := k.store.GetAtom(ctx, ns, "pattern-01")
	if err != nil {
		t.Fatalf("GetAtom: %v", err)
	}
	if card.Kind != types.FactLesson {
		t.Fatalf("dream 事实时效类别 = %q, want %q（教训不因时间失效）", card.Kind, types.FactLesson)
	}
}

// TestRecallCfgAnalyzerWired 查询理解接线钉（v3.177.0）：recallCfg 的 Analyzer
// 非 nil 且为包级单例——query.Analyzer 纯只读，复用安全。
func TestRecallCfgAnalyzerWired(t *testing.T) {
	cfg := recallCfg(100)
	if cfg.Analyzer == nil {
		t.Fatal("recallCfg 未接查询理解器")
	}
	if cfg.Analyzer != sharedAnalyzer {
		t.Fatal("Analyzer 应为包级单例（纯只读复用）")
	}
}

// TestSemanticTrackWired 语义轨接线钉（v5.0.0）：注入 Embedder 后 recall 走
// VectorSemanticReader（sqlite 内置向量索引），主链路照常工作且向量行落库。
// HashEmbedder 是词面哈希（语义强度弱），足够验证「接通且不炸」的接线事实；
// 真语义强度由部署侧注入 rag.OpenAIEmbedder（ARGUS_EMBEDDING_*）承载。
func TestSemanticTrackWired(t *testing.T) {
	st, err := sqlite.OpenMemory("semtrack")
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	k, err := openWithStore(st, ports.HashEmbedder{}, logx.Nop())
	if err != nil {
		t.Fatalf("openWithStore: %v", err)
	}
	ctx := context.Background()
	ns := store.Namespace("gitlab-g-r")
	if err := k.SaveFact(ctx, ns, "pattern-01", "pattern", "SQL 拼接低置信", "命中类似表述请提高举证标准。", time.Now()); err != nil {
		t.Fatalf("SaveFact: %v", err)
	}
	out := k.RecallIndex(ctx, ns, "", "task-x", "举证", 2000)
	if !strings.Contains(out, "举证") {
		t.Fatalf("语义轨开启后召回失效：%q", out)
	}
}
