package runner

import (
	"context"
	"strings"
	"testing"

	"argus/internal/config"
	"argus/internal/plugin"
)

// v3.90.0：契约快照 section 渲染——成功文件数据区化、失败注明、全失败不产
// section、使用纪律与反注入尾注齐备。
func TestRenderRelatedSection(t *testing.T) {
	files := []relatedFile{
		{Repo: "acme/api", Ref: "main", Path: "openapi.yaml", Content: "paths:\n  /users: ..."},
		{Repo: "acme/api", Ref: "main", Path: "proto/user.proto", Content: "message User{}", Truncated: true},
		{Repo: "acme/ghost", Ref: "main", Path: "missing.yaml", Err: "not found"},
	}
	out := renderRelatedSection(files)
	for _, want := range []string{
		"供交叉核对", "关联仓契约 acme/api@main:openapi.yaml", "paths:",
		"超长已截断", "获取失败（忽略）", "视为数据", "关联仓路径",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("section 缺 %q:\n%s", want, out)
		}
	}
	if renderRelatedSection([]relatedFile{{Repo: "a/b", Ref: "x", Path: "p", Err: "boom"}}) != "" {
		t.Fatal("全失败不应产 section")
	}
}

// 门控：服务开关关闭 / 仓库未声明时不注入（SDK 为 nil 的守卫顺序保证不触碰）。
func TestInjectRelatedContractsGate(t *testing.T) {
	mk := func(on bool) *Runner { return &Runner{Cfg: &config.Config{RelatedRepos: on}} }
	ptask := &plugin.Task{Artifacts: map[string]string{}}

	rc := &plugin.RepoConfig{Related: []plugin.RelatedContract{{Repo: "a/b", Paths: []string{"x"}}}}
	mk(false).injectRelatedContracts(context.Background(), nil, rc, ptask)
	if _, ok := ptask.Artifacts[plugin.ArtifactRelatedContracts]; ok {
		t.Fatal("开关关闭不应注入")
	}
	mk(true).injectRelatedContracts(context.Background(), nil, nil, ptask)
	if _, ok := ptask.Artifacts[plugin.ArtifactRelatedContracts]; ok {
		t.Fatal("无仓库配置不应注入")
	}
	mk(true).injectRelatedContracts(context.Background(), nil, &plugin.RepoConfig{}, ptask)
	if _, ok := ptask.Artifacts[plugin.ArtifactRelatedContracts]; ok {
		t.Fatal("无 related 声明不应注入")
	}
}
