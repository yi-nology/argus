package argusctx

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

// branchFake 只实现 GetRepo（计数）；未覆盖方法借嵌入接口 panic 兜底。
type branchFake struct {
	provider.Provider
	getRepoN atomic.Int64
}

func (f *branchFake) GetRepo(_ context.Context, _, _ string) (*provider.PlatformRepo, error) {
	f.getRepoN.Add(1)
	return &provider.PlatformRepo{DefaultBranch: "main"}, nil
}

// TestResolveBranchCache 默认分支 5min TTL 缓存：同仓第二次解析不再打 GetRepo；
// 调用方显式给分支时零 API 调用（v5.3.3）。
func TestResolveBranchCache(t *testing.T) {
	f := &branchFake{}
	s := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"github": f})

	b, err := s.ResolveBranch(context.Background(), "github", "o", "r", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b != "main" {
		t.Fatalf("got %q, want main", b)
	}
	if f.getRepoN.Load() != 1 {
		t.Fatalf("首次解析应打 1 次 GetRepo，got %d", f.getRepoN.Load())
	}
	// 第二次走缓存
	if b2, _ := s.ResolveBranch(context.Background(), "github", "o", "r", ""); b2 != "main" {
		t.Fatalf("缓存读取失败: %q", b2)
	}
	if f.getRepoN.Load() != 1 {
		t.Fatalf("缓存命中不应再打 GetRepo，got %d", f.getRepoN.Load())
	}
	// 显式分支短路
	if b3, _ := s.ResolveBranch(context.Background(), "github", "o", "r", "dev"); b3 != "dev" {
		t.Fatalf("显式分支应原样返回: %q", b3)
	}
	if f.getRepoN.Load() != 1 {
		t.Fatalf("显式分支不应打 GetRepo，got %d", f.getRepoN.Load())
	}
}

// getRepoFail GetRepo 恒失败（模拟仓库不可达）。
type getRepoFail struct {
	provider.Provider
}

func (getRepoFail) GetRepo(context.Context, string, string) (*provider.PlatformRepo, error) {
	return nil, context.DeadlineExceeded
}

// TestLoadRepoConfigAuto 分支解析失败时上抛错误（配置读取不发生）。
func TestLoadRepoConfigAuto(t *testing.T) {
	s := NewSDKCtxStubbed(nopLogx{}, map[string]provider.Provider{"github": getRepoFail{}})
	if _, err := s.LoadRepoConfigAuto(context.Background(), "github", "o", "r", ""); err == nil {
		t.Fatal("分支解析失败应上抛")
	}
}
