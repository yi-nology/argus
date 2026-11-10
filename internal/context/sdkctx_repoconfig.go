package context

import (
	"context"
	"fmt"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

// ResolveBranch 解析仓库默认分支：branch 非空原样返回（零 API 调用）；空则查
// GetRepo 并落 5min TTL 缓存（v5.3.3）。默认分支变更是低频事件、本值只读不写
// ——describe/agents 等评论/HTTP 请求路径此前每请求真打一次 GetRepo 纯浪费。
func (s *SDKCtx) ResolveBranch(ctx context.Context, platform, owner, repo, branch string) (string, error) {
	if branch != "" {
		return branch, nil
	}
	key := platform + "/" + owner + "/" + repo
	if v, err := s.branchCache().Get(ctx, key); err == nil && v != nil {
		return *v, nil
	}
	info, err := s.GetRepo(ctx, platform, owner, repo)
	if err != nil {
		return "", err
	}
	if info.DefaultBranch == "" {
		return "", fmt.Errorf("仓库 %s/%s/%s 默认分支为空", platform, owner, repo)
	}
	_ = s.branchCache().Set(ctx, key, &info.DefaultBranch)
	return info.DefaultBranch, nil
}

// LoadRepoConfigAuto 仓库主权配置单步入口（v5.3.3 收口 runner/workflow/describe/
// agents 四处「取默认分支 → 读 .reviewagents.yaml」两步模式）：branch 为空时先
// ResolveBranch。配置不存在（404）仍按 LoadRepoConfig 语义返回 EmptyRepoConfig；
// 分支解析失败返回错误，调用方按各自纪律降级（任务路径 Empty+Warn / 请求路径 nil）。
func (s *SDKCtx) LoadRepoConfigAuto(ctx context.Context, platform, owner, repo, branch string) (*plugin.RepoConfig, error) {
	b, err := s.ResolveBranch(ctx, platform, owner, repo, branch)
	if err != nil {
		return nil, err
	}
	return s.LoadRepoConfig(ctx, platform, owner, repo, b)
}

// branchCache 默认分支缓存（per-SDKCtx 懒建，TTL 5 分钟；与 service 侧 collabCache
// 同款 datacache 口径）。
func (s *SDKCtx) branchCache() *datacache.LocalCache[string] {
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	if s.branchCch == nil {
		s.branchCch = datacache.NewLocalCache(datacache.LocalConfig[string]{
			TTL:     5 * time.Minute,
			MaxSize: 200,
		})
	}
	return s.branchCch
}

// LoadRepoConfig 从默认分支读 .reviewagents.yaml（§3.4 红线 1：绝不读 PR head / 工作副本）。
// 读不到（404）返回 EmptyRepoConfig 而非错误。
func (s *SDKCtx) LoadRepoConfig(ctx context.Context, platform, owner, repo, defaultBranch string) (*plugin.RepoConfig, error) {
	const path = ".reviewagents.yaml"
	content, err := s.GetFileContent(ctx, platform, owner, repo, path, defaultBranch)
	if err != nil {
		if provider.IsNotFound(err) {
			return plugin.EmptyRepoConfig(), nil
		}
		return nil, err
	}
	rc, err := plugin.ParseRepoConfig([]byte(content))
	if err != nil {
		// 仓库配置是敌对输入：解析失败按无配置处理 + 告警，不炸任务
		s.log.Warn("argus.sdkctx.repo_config_invalid", "repo", owner+"/"+repo, "error", err.Error())
		return plugin.EmptyRepoConfig(), nil
	}
	return rc, nil
}
