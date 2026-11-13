package plugin

import (
	"github.com/oagentkit/textx/reportutil"

	"github.com/yi-nology/argus/internal/domain"
)

// 仓库级评审行为过滤（v3.15.0 P0-2：.reviewagents.yaml review 段的运行时消费）。
// 全部确定性 Go 代码，不经 LLM——与"路由/裁决/合并不走 LLM"同一纪律。

// RepoSettingsFilter 返回仓库配置的路径过滤器（true = 排除该文件，不进 diff 文本）。
// 语义：IncludePaths 非空 = 白名单模式（未命中者排除），ExcludePaths 在其上再排除；
// 两表皆空（含 nil rc）= 永不排除，维持服务级默认行为。
func RepoSettingsFilter(rc *RepoConfig) func(path string) bool {
	if rc == nil {
		return func(string) bool { return false }
	}
	exclude, include := rc.Review.ExcludePaths, rc.Review.IncludePaths
	if len(exclude) == 0 && len(include) == 0 {
		return func(string) bool { return false }
	}
	return func(path string) bool {
		for _, p := range exclude {
			if GlobMatch(p, path) {
				return true
			}
		}
		if len(include) > 0 {
			for _, p := range include {
				if GlobMatch(p, path) {
					return false
				}
			}
			return true
		}
		return false
	}
}

// FilterByMinSeverity 按仓库阈值过滤 findings（v3.15.0 review.min_severity）。
// 语义：Rank 低于阈值者剔除；空阈值原样返回。high 意见任何合法阈值下都保留
// （词表只有 low|medium|high，阈值取 high 时仅保留 high）。
// 返回保留切片（保序）与剔除数，供降级注明。
func FilterByMinSeverity(fs []domain.Finding, min string) (kept []domain.Finding, dropped int) {
	if min == "" || len(fs) == 0 {
		return fs, 0
	}
	floor := reportutil.Rank(min)
	out := make([]domain.Finding, 0, len(fs))
	for _, f := range fs {
		if reportutil.Rank(f.Severity) < floor {
			dropped++
			continue
		}
		out = append(out, f)
	}
	return out, dropped
}
