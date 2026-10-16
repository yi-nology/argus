// Package health 审查健康报告（v3.11.0，G5，对应 REPORT.md「自进化产品可见度」缺口）。
//
// 动机：Argus 的 pattern/knowledge/dream 飞轮偏基础设施，仓库维护者感知弱——
// MiMo 的 dream/distill 优势在「让人看见记忆在长大」。本包周期聚合 pattern_signals
// 与学习环状态，产出人读报告并经 IM 推送：
//   - 噪声处置建议：置信度跌破 HardFloor 的模式（系统已自动降权，此处给人复核入口）；
//   - guidelines 增量建议：反复出现且无误报史的意见（posted 高、零 suppress），
//     建议固化进 .argus/review-guidelines.md——**只生成建议文本，绝不自动改仓库文件**，
//     人工确认后生效（对齐「持久抑制只来自人工」红线）。
//
// 全部确定性、无 LLM；失败只告警。累计口径：连续两期报告差值即周期趋势。
package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/fence"
	"github.com/oekit/logx"

	"argus/internal/learning"
)

// Store 健康报告需要的最小存储面（storage.Store 满足）。v3.54.0 起全面走
// learning 域类型投影，不再感知 storage 行模型。
type Store interface {
	ListPatternRepos(ctx context.Context) ([]learning.RepoRef, error)
	ListRepoPatternStates(ctx context.Context, platform, owner, repo string) ([]learning.PatternState, error)
}

// Tip 单条建议（噪声处置 / guidelines 增量）。
type Tip struct {
	Plugin     string  `json:"plugin"`
	Sample     string  `json:"sample"`
	Posted     int     `json:"posted"`
	Regret     int     `json:"regret"`
	Suppress   int     `json:"suppress"`
	Confidence float64 `json:"confidence"`
}

// RepoHealth 单仓库健康摘要。
type RepoHealth struct {
	Platform string
	Owner    string
	Repo     string
	Posted   int
	Regret   int
	Suppress int
	Patterns int
	// Noisy 置信度跌破 HardFloor 的模式（建议人工处置）。
	Noisy []Tip
	// Guideline 建议固化进 review-guidelines.md 的重复意见模式。
	Guideline []Tip
}

// Report 一期健康报告。
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Repos       []RepoHealth `json:"repos"`
	Empty       bool         `json:"empty"` // 无任何学习环数据（不打扰推送）
}

// Config 阈值（复用学习环 config 派生；零值安全）。
type Config struct {
	Learning learning.Config
	// TopN 每仓库每类建议条数上限（0 = 5）。
	TopN int
	// GuidelineMinPosted guidelines 建议的最低 posted 次数（0 = 3）。
	GuidelineMinPosted int
}

// Build 聚合一期报告（纯查询 + 内存分类，无写操作）。
func Build(ctx context.Context, store Store, cfg Config) (*Report, error) {
	rep := &Report{GeneratedAt: time.Now().UTC()}
	repos, err := store.ListPatternRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("health: list repos: %w", err)
	}
	for _, r := range repos {
		rows, err := store.ListRepoPatternStates(ctx, r.Platform, r.Owner, r.Repo)
		if err != nil {
			return nil, fmt.Errorf("health: list patterns %s/%s: %w", r.Owner, r.Repo, err)
		}
		if len(rows) == 0 {
			continue
		}
		rh := RepoHealth{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo, Patterns: len(rows)}
		for _, row := range rows {
			rh.Posted += row.Posted
			rh.Regret += row.Regret
			rh.Suppress += row.Suppress
			sample := row.SampleText()
			tip := Tip{Plugin: row.Plugin, Sample: sample, Posted: row.Posted,
				Regret: row.Regret, Suppress: row.Suppress,
				Confidence: learning.Confidence(row.Posted, row.Regret, row.Suppress, row.Restore)}
			// 噪声：样本充足且置信度跌破 HardFloor（与学习环 HardFloor 消费同口径）
			if cfg.Learning.Enabled &&
				learning.PatternSamples(row.Posted, row.Regret, row.Suppress) >= cfg.Learning.MinSamples &&
				tip.Confidence < cfg.Learning.HardFloor {
				rh.Noisy = append(rh.Noisy, tip)
				continue
			}
			// guidelines 增量：反复发表（≥ minPosted）、零人工 suppress（无误报史）——
			// 这类意见值得写进仓库审查指引让模型前置注意
			minPosted := cfg.GuidelineMinPosted
			if minPosted <= 0 {
				minPosted = 3
			}
			if row.Posted >= minPosted && row.Suppress == 0 {
				rh.Guideline = append(rh.Guideline, tip)
			}
		}
		topN := cfg.TopN
		if topN <= 0 {
			topN = 5
		}
		rh.Noisy = sortTips(rh.Noisy, topN)
		rh.Guideline = sortTips(rh.Guideline, topN)
		rep.Repos = append(rep.Repos, rh)
	}
	for _, rh := range rep.Repos {
		if rh.Posted > 0 || rh.Suppress > 0 || rh.Regret > 0 || len(rh.Noisy) > 0 || len(rh.Guideline) > 0 {
			rep.Empty = false
			break
		}
	}
	rep.Empty = rep.Empty || len(rep.Repos) == 0
	return rep, nil
}

func sortTips(tips []Tip, topN int) []Tip {
	sort.SliceStable(tips, func(i, j int) bool {
		if tips[i].Posted != tips[j].Posted {
			return tips[i].Posted > tips[j].Posted
		}
		return tips[i].Confidence < tips[j].Confidence
	})
	if len(tips) > topN {
		tips = tips[:topN]
	}
	return tips
}

// Render 渲染 IM/markdown 报告（safejson 转义：pattern_text 半可信）。
func Render(rep *Report, learning learning.Config) string {
	if rep == nil || rep.Empty {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Argus 审查健康报告** %s\n", rep.GeneratedAt.Format("2006-01-02 15:04 MST"))
	for _, rh := range rep.Repos {
		fmt.Fprintf(&b, "\n**%s/%s/%s**　posted %d · regret %d · suppress %d（%d 个模式）\n",
			rh.Platform, rh.Owner, rh.Repo, rh.Posted, rh.Regret, rh.Suppress, rh.Patterns)
		if len(rh.Noisy) > 0 {
			b.WriteString("- 🧹 噪声处置建议（置信度低于 HardFloor，系统已自动降权；请人工复核 suppress/restore）：\n")
			for i, tip := range rh.Noisy {
				fmt.Fprintf(&b, "  %d. [%s] %s（置信度 %.2f，suppress=%d regret=%d）\n",
					i+1, tip.Plugin, fence.EscapeUntrusted(tip.Sample), tip.Confidence, tip.Suppress, tip.Regret)
			}
		}
		if len(rh.Guideline) > 0 {
			b.WriteString("- 📌 guidelines 增量建议（同类意见反复出现且无误报史；确认后人工加入 `.argus/review-guidelines.md`，系统绝不自动改仓库文件）：\n")
			for i, tip := range rh.Guideline {
				fmt.Fprintf(&b, "  %d. [%s] %s（posted %d）\n",
					i+1, tip.Plugin, fence.EscapeUntrusted(tip.Sample), tip.Posted)
			}
		}
	}
	if learning.Enabled {
		fmt.Fprintf(&b, "\n> 趋势口径：本报告为累计值；与上一期差值即本期增量。阈值 soft=%.2f hard=%.2f min_samples=%d。\n",
			learning.SoftFloor, learning.HardFloor, learning.MinSamples)
	}
	return b.String()
}

// Runner 周期驱动器（main.go AddTicker 回调目标；单次构建+推送）。
type Runner struct {
	Store  Store
	Notify interface {
		NotifyRaw(ctx context.Context, content string)
	}
	Cfg Config
	Log logx.Logger
}

// RunOnce 构建并推送一期报告。无数据时不推送（不打扰）。
func (r *Runner) RunOnce(ctx context.Context) error {
	rep, err := Build(ctx, r.Store, r.Cfg)
	if err != nil {
		return err
	}
	if content := Render(rep, r.Cfg.Learning); content != "" {
		r.Notify.NotifyRaw(ctx, content)
		if r.Log != nil {
			r.Log.Info("argus.health.report_sent", "repos", len(rep.Repos))
		}
	} else if r.Log != nil {
		r.Log.Info("argus.health.report_skipped", "reason", "empty")
	}
	return nil
}
