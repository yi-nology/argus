// runner_prctx.go —— PR 上下文构建面：PRContext/diff 增量/仓库配置与审查指引（RepoGuide）。
package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/oagentkit/textx/textutil"
	datacache "github.com/oekit/data/cache"

	argusctx "argus/internal/context"
	"argus/internal/dispatch"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// buildPRContext 从平台 API 组装 PR 上下文（CLI 任务此刻才拿到 head SHA）。
func (r *Runner) buildPRContext(ctx context.Context, task *storage.Task) (*plugin.PRContext, error) {
	cr, err := r.SDK.GetCR(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 PR 详情失败: %w", err)
	}
	repo, err := r.SDK.GetRepo(ctx, task.Platform, task.Owner, task.Repo)
	if err != nil {
		return nil, fmt.Errorf("获取仓库信息失败: %w", err)
	}
	headSHA := cr.HeadSHA
	if headSHA == "" {
		headSHA = task.HeadSHA
	}
	return &plugin.PRContext{
		Platform:      task.Platform,
		Owner:         task.Owner,
		Repo:          task.Repo,
		Number:        task.Number,
		Title:         cr.Title,
		Body:          cr.Description,
		Author:        authorName(cr.Author),
		Labels:        cr.Labels,
		HeadSHA:       headSHA,
		BaseBranch:    cr.TargetBranch,
		HeadBranch:    cr.SourceBranch,
		DefaultBranch: repo.DefaultBranch,
		EventType:     task.EventType,
	}, nil
}

// buildDiffIncremental 拉取 diff 并尝试增量清洗（diff-of-diffs）。
// 仅在 synchronize 事件（PR 更新）且存在前置审查记录时激活增量路径；
// 其余场景退化为全量 Prepare。opts 为仓库级路径过滤（v3.15.0 review 段）。
// totalLimit 由调用方传按仓旋钮生效值（v3.187.0 taskKnobs.maxDiffChars）。
func (r *Runner) buildDiffIncremental(ctx context.Context, pr *plugin.PRContext, task *storage.Task,
	totalLimit int, opts ...argusctx.PrepareOptions) (*plugin.DiffContext, error) {
	md, err := r.SDK.GetCRDiff(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 diff 失败: %w", err)
	}
	// 任一路径只要 Prepare 成功即视为 diffprep 执行（活检）
	defer plugin.ReportLayerExec(plugin.LayerDiffprep)

	// 非 synchronize 事件 → 全量审查（首次审查 / reopen 等）
	if task.EventType != "cr.synchronize" {
		return argusctx.Prepare(md, totalLimit, opts...), nil
	}

	// 查询同 PR 最近一次 done 任务的 diff 文本
	lastDiff, err := r.Store.GetLastReviewedDiff(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		r.Log.Warn("argus.runner.get_last_diff_failed", "task", task.ID, "error", err.Error())
		return argusctx.Prepare(md, totalLimit, opts...), nil // 降级全量
	}

	if lastDiff == "" {
		return argusctx.Prepare(md, totalLimit, opts...), nil // 无前置记录 → 全量
	}

	r.Log.Info("argus.runner.incremental_diff", "task", task.ID,
		"last_diff_chars", len(lastDiff))
	return argusctx.PrepareIncremental(md, lastDiff, totalLimit, opts...), nil
}

// loadRepoConfig 仓库主权配置（runner 侧单次加载，diff 过滤与 workflow 共用；
// v3.15.0 前该加载只在 workflow.loadRepoConfig，为传 diffprep 选项在此提前）。
// 失败按无配置 + 告警（与 workflow 侧同纪律）。
func (r *Runner) loadRepoConfig(ctx context.Context, pr *plugin.PRContext) *plugin.RepoConfig {
	branch, err := r.SDK.ResolveBranch(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		r.Log.Warn("argus.runner.repo_fetch_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	pr.DefaultBranch = branch
	rc, err := r.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, branch)
	if err != nil {
		r.Log.Warn("argus.runner.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := dispatch.ResolveRepoConfig(rc, r.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			r.Log.Warn("argus.runner.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

// repoGuideMaxBytes 仓库评审规范单文件上限（ZCode AGENTS.md 同款 100KB）。
const repoGuideMaxBytes = 100_000

// repoGuideAgentMaxBytes 业界标准 agent 规范文件的单文件上限（AGENTS.md 等多为
// 项目级通用指引而非审查专属，注入预算给小一些，防多源叠加挤占 R3/R4 窗口）。
const repoGuideAgentMaxBytes = 32_000

// repoGuideAgentFiles 业界标准 agent 规范文件（2026 事实标准，兼容读第一个存在的）：
// AGENTS.md（跨工具通用）/ CLAUDE.md（Claude Code）/ .cursorrules（Cursor）。
// 全部命中时仍只取第一个——同一仓库多份 agent 规范的内容通常高度重叠，
// 全拼只会稀释注入预算。
var repoGuideAgentFiles = []string{"AGENTS.md", "CLAUDE.md", ".cursorrules"}

// repoGuideCache 评审规范缓存（repo@sha → 内容；同 sha 多任务/重试复用，
// 负缓存空串防同 sha 反复打平台 API）。v3.82.0 引入 oekit/data/cache：
// MaxSize LRU 精确淘汰（键含 sha 天然有限），取代手写 mutex+map+
// 「满 64 整体清空」换防——热条目不再被无差别逐出。
var repoGuideCache = datacache.NewLocalCache(datacache.LocalConfig[string]{MaxSize: 64})

// RepoGuideFor 读取被评审仓库的评审规范 @ head（v3.38.0，v3.71.0 多源）：
// .argus/REVIEW.md（argus 专属）+ 业界标准 agent 规范（AGENTS.md/CLAUDE.md/
// .cursorrules，第一个存在的）。两源独立截断后拼接，REVIEW.md 在前。
// 经平台 API 取 blob——tree 作用域天然无本地 symlink 逃逸面；头截断 + 提醒
// 标签转义（半可信输入）；缺失/失败返回空段（常态，debug 留痕）。
func (r *Runner) RepoGuideFor(ctx context.Context, platform, owner, repo, headSHA string) string {
	if r.SDK == nil || r.Cfg.RepoGuideOff || headSHA == "" {
		return ""
	}
	key := platform + "/" + owner + "/" + repo + "@" + headSHA
	if cached, _ := repoGuideCache.Get(ctx, key); cached != nil {
		return *cached
	}
	var sections []string
	content := r.repoGuideFile(ctx, platform, owner, repo, headSHA, ".argus/REVIEW.md", repoGuideMaxBytes)
	if content != "" {
		sections = append(sections, content)
	} else {
		r.Log.Debug("argus.runner.repo_guide_absent", "repo", owner+"/"+repo)
	}
	for _, path := range repoGuideAgentFiles {
		agent := r.repoGuideFile(ctx, platform, owner, repo, headSHA, path, repoGuideAgentMaxBytes)
		if agent != "" {
			r.Log.Info("argus.runner.agent_guide_loaded", "repo", owner+"/"+repo, "file", path)
			sections = append(sections, "## Agent 通用规范（来自 "+path+"）\n\n"+agent)
			break
		}
	}
	merged := strings.Join(sections, "\n\n")
	_ = repoGuideCache.Set(ctx, key, &merged)
	return merged
}

// repoGuideFile 单文件拉取 + 截断 + 提醒标签转义；缺失/失败返回空串。
func (r *Runner) repoGuideFile(ctx context.Context, platform, owner, repo, headSHA, path string, maxBytes int) string {
	raw, err := r.SDK.GetFileContent(ctx, platform, owner, repo, path, headSHA)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	if len(raw) > maxBytes {
		// rune 安全截断：字节切分会腰斩多字节 UTF-8 出非法序列进 R3/R4 prompt。
		var trunc string
		trunc, _ = textutil.TruncRunes(raw, maxBytes/3) // rune≈3 字节，与旧字节预算同量级
		raw = trunc + "\n…（截断：" + path + " 超上限）"
	}
	r.Log.Info("argus.runner.repo_guide_loaded", "repo", owner+"/"+repo, "file", path, "bytes", len(raw))
	return plugin.EscapeReminderTags(raw)
}
