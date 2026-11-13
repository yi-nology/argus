// patterns.go —— 模式级后悔学习环（docs/review-closed-loop-design.md §3）。
// 信号只记账；消费（标注/闸门/注入）由 Merger 与 runner 按置信度阈值执行。
// 写失败由调用方告警，不阻塞主流程。

package storage

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
)

// PatternSignal 仓库级审查模式信任历史：repo × plugin × pattern_key。
// 置信度由计数读时计算（learning.Confidence），表内不持久化 confidence 列。
type PatternSignal struct {
	ID uint `gorm:"column:id;primaryKey"`

	Platform string `gorm:"column:platform;size:16;not null;uniqueIndex:idx_pattern,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;uniqueIndex:idx_pattern,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;uniqueIndex:idx_pattern,priority:3"`
	Plugin   string `gorm:"column:plugin;size:64;not null;uniqueIndex:idx_pattern,priority:4"`
	Pattern  string `gorm:"column:pattern_key;size:32;not null;uniqueIndex:idx_pattern,priority:5"`

	PatternText string `gorm:"column:pattern_text;size:255"`

	Posted   int `gorm:"column:posted"`   // 成功进入报告的次数
	Regret   int `gorm:"column:regret"`   // R3.5 / 证据门剔除
	Suppress int `gorm:"column:suppress"` // 人工 suppress
	Restore  int `gorm:"column:restore"`  // 人工 restore

	FirstSeenAt time.Time `gorm:"column:first_seen_at"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (PatternSignal) TableName() string { return "pattern_signals" }

func (p *PatternSignal) project() learning.PatternState {
	return learning.PatternState{
		Plugin: p.Plugin, Pattern: p.Pattern, PatternText: p.PatternText,
		Posted: p.Posted, Regret: p.Regret, Suppress: p.Suppress, Restore: p.Restore,
		Confidence: learning.Confidence(p.Posted, p.Regret, p.Suppress, p.Restore),
		Samples:    learning.PatternSamples(p.Posted, p.Regret, p.Suppress),
	}
}

// bumpPattern 原子累加一列并刷新 last_seen_at；行不存在则以该信号创建。
func (s *Store) bumpPattern(ctx context.Context, platform, owner, repo, pluginName string,
	f domain.Finding, column string, at time.Time) error {
	if platform == "" || owner == "" || repo == "" {
		return nil
	}
	if pluginName == "" {
		pluginName = "unknown"
	}
	key := learning.PatternKey(pluginName, f.Comment)
	text := learning.PatternSample(f.Comment)

	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row PatternSignal
			err := tx.Where("platform = ? AND owner = ? AND repo = ? AND plugin = ? AND pattern_key = ?",
				platform, owner, repo, pluginName, key).First(&row).Error
			if err == gorm.ErrRecordNotFound {
				row = PatternSignal{
					Platform: platform, Owner: owner, Repo: repo,
					Plugin: pluginName, Pattern: key, PatternText: text,
					FirstSeenAt: at, LastSeenAt: at,
				}
				switch column {
				case "posted":
					row.Posted = 1
				case "regret":
					row.Regret = 1
				case "suppress":
					row.Suppress = 1
				case "restore":
					row.Restore = 1
				default:
					return nil
				}
				return tx.Create(&row).Error
			}
			if err != nil {
				return err
			}
			if text != "" && row.PatternText == "" {
				row.PatternText = text
			}
			row.LastSeenAt = at
			return tx.Model(&row).Updates(map[string]any{
				column:       gorm.Expr(column + " + 1"),
				"updated_at": at,
				"pattern_text": func() string {
					if row.PatternText != "" {
						return row.PatternText
					}
					return text
				}(),
				"last_seen_at": at,
			}).Error
		})
	})
}

// PatternPosted 意见进入报告（入库后调用；调用方保证非 suppressed）。
func (s *Store) PatternPosted(ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "posted", at)
}

// PatternRegret R3.5 / 证据门剔除该意见。
func (s *Store) PatternRegret(ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "regret", at)
}

// PatternSuppress 人工判误报。
func (s *Store) PatternSuppress(ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "suppress", at)
}

// PatternRestore 撤销误抑制。
func (s *Store) PatternRestore(ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "restore", at)
}

// ListLowConfidencePatterns 拉取本仓库低于 maxConfidence 且样本量 ≥ minSamples 的模式，
// 按置信度升序（最不可信在前），limit>0 时截断。消费点：R4 注入 + Merger 闸门。
// 实现 domain.PatternStore。
func (s *Store) ListLowConfidencePatterns(ctx context.Context, platform, owner, repo string,
	minSamples int, maxConfidence float64, limit int) ([]learning.PatternState, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND (posted + regret + suppress) >= ?",
			platform, owner, repo, minSamples).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]learning.PatternState, 0, len(rows))
	for i := range rows {
		p := rows[i].project()
		if p.Confidence >= maxConfidence {
			continue
		}
		out = append(out, p)
	}
	// 确定性排序：置信度升序，同分按 pattern
	slices.SortFunc(out, func(a, b learning.PatternState) int {
		if c := cmp.Compare(a.Confidence, b.Confidence); c != 0 {
			return c
		}
		return strings.Compare(a.Pattern, b.Pattern)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetPattern 单模式查询（测试/观测）。
func (s *Store) GetPattern(ctx context.Context, platform, owner, repo, pluginName, comment string) (*learning.PatternState, error) {
	key := learning.PatternKey(pluginName, comment)
	var row PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND plugin = ? AND pattern_key = ?",
			platform, owner, repo, pluginName, key).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := row.project()
	return &p, nil
}

// ListRepoPatterns 拉取某仓库全部模式原始行（dream 整合用；含 ID）。
func (s *Store) ListRepoPatterns(ctx context.Context, platform, owner, repo string) ([]PatternSignal, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo).
		Order("id").Find(&rows).Error
	return rows, err
}

// ListPatternRepos 列出出现过模式信号的仓库（dream 周期遍历）。
func (s *Store) ListPatternRepos(ctx context.Context) ([]learning.RepoRef, error) {
	type row struct {
		Platform string
		Owner    string
		Repo     string
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&PatternSignal{}).
		Distinct("platform", "owner", "repo").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]learning.RepoRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, learning.RepoRef(r))
	}
	return out, nil
}

// MergePatternInto 把 dup 行的计数并入 keep 行并删除 dup（dream 去重）。
// keep 必须存在；事务内完成，失败整体回滚。
func (s *Store) MergePatternInto(ctx context.Context, platform, owner, repo string, keepID, dupID uint, at time.Time) error {
	if keepID == 0 || dupID == 0 || keepID == dupID {
		return nil
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var keep, dup PatternSignal
			if err := tx.Where("id = ? AND platform = ? AND owner = ? AND repo = ?",
				keepID, platform, owner, repo).First(&keep).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ? AND platform = ? AND owner = ? AND repo = ?",
				dupID, platform, owner, repo).First(&dup).Error; err != nil {
				return err
			}
			if err := tx.Model(&keep).Updates(map[string]any{
				"posted":        gorm.Expr("posted + ?", dup.Posted),
				"regret":        gorm.Expr("regret + ?", dup.Regret),
				"suppress":      gorm.Expr("suppress + ?", dup.Suppress),
				"restore":       gorm.Expr("restore + ?", dup.Restore),
				"last_seen_at":  laterTime(keep.LastSeenAt, dup.LastSeenAt),
				"first_seen_at": earlierTime(keep.FirstSeenAt, dup.FirstSeenAt),
				"updated_at":    at,
			}).Error; err != nil {
				return err
			}
			return tx.Delete(&dup).Error
		})
	})
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earlierTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// PruneStalePatterns 模式信号保留策略（v3.9.2：pattern_signals 此前只增不删，
// 每个新 pattern_key（repo×plugin×规范化评语）永久一行，随时间慢速无界）。
// 删除 LastSeenAt 早于 cutoff 的行——语义与 PruneStalePrints 同源：长期未复现
// 的模式遗忘后最坏效果是"同型意见重新从头计数"，不会误杀（MinSamples 门槛
// 保护小样本消费）。分批删除（每批 500，长事务占住 SQLite 单写连接）。
func (s *Store) PruneStalePatterns(ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []uint
			if err := s.db.WithContext(ctx).Model(&PatternSignal{}).
				Where("last_seen_at < ?", cutoff).Limit(500).Pluck("id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("id IN ?", batch).Delete(&PatternSignal{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// 编译期：*Store 实现 domain.PatternStore。
var _ domain.PatternStore = (*Store)(nil)

// ListAllPatternSignals 全量模式信号（G1c 训练数据导出用；只读，无仓库过滤）。
func (s *Store) ListAllPatternSignals(ctx context.Context) ([]PatternSignal, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// ListRepoPatternStates 某 PR 全部模式的学习环读侧投影（health 消费；不暴露
// storage 行模型——v3.54.0 消费侧接口补全）。
func (s *Store) ListRepoPatternStates(ctx context.Context, platform, owner, repo string) ([]learning.PatternState, error) {
	rows, err := s.ListRepoPatterns(ctx, platform, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]learning.PatternState, 0, len(rows))
	for _, p := range rows {
		out = append(out, p.project())
	}
	return out, nil
}
