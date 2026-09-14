// reviewtarget.go —— review_targets 的补充操作。
// 与 UpsertReviewTarget 分离的原因：Upsert 的语义是">0 才写"（防 GORM 零值误覆盖），
// 而"总结评论已被删除"需要显式清零缓存定位（poster ① 级 404 自愈，深挖修复）——
// 传 0 复用 Upsert 是 no-op，清不掉。
package storage

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"git.example.com/enjoydream/ekit/data/gormx"
)

// ClearReviewTargetComment 显式清零 last_summary_comment_id（评论 404 后调用），
// head SHA 不动（同 SHA 去重语义保留）。行不存在视为已清除。
func (s *Store) ClearReviewTargetComment(ctx context.Context, platform, owner, repo, number string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		return tx.Model(&ReviewTarget{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ?", platform, owner, repo, number).
			Update("last_summary_comment_id", 0).Error
	})
}

// knownCommentIDsCap 历史报告评论 ID 缓存上限（postSummary 三级降级之外的第二代
// 兜底：报告评论被顶出 SDK ListIssueComments 第一页后扫描不中，缓存 ID 可直连更新）。
const knownCommentIDsCap = 10

// AppendReviewTargetComment 记录一条本 bot 发出的总结评论 ID（去重、保序、封顶，
// 超出淘汰最旧）。与 LastSummaryCommentID 分开：后者是"最新定位"，前者是历史全量。
func (s *Store) AppendReviewTargetComment(ctx context.Context, platform, owner, repo, number string, id int64) error {
	if id <= 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		var t ReviewTarget
		err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).First(&t).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			t = ReviewTarget{Platform: platform, Owner: owner, Repo: repo, Number: number}
		}
		var ids []int64
		if t.KnownCommentIDs != "" {
			_ = json.Unmarshal([]byte(t.KnownCommentIDs), &ids)
		}
		for _, e := range ids {
			if e == id {
				return nil
			}
		}
		ids = append(ids, id)
		if len(ids) > knownCommentIDsCap {
			ids = ids[len(ids)-knownCommentIDsCap:]
		}
		b, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		t.KnownCommentIDs = string(b)
		return tx.Save(&t).Error
	})
}

// KnownReviewTargetComments 返回缓存的历史评论 ID（新→旧排列供逐个尝试；无缓存返回 nil）。
func (s *Store) KnownReviewTargetComments(ctx context.Context, platform, owner, repo, number string) ([]int64, error) {
	var t ReviewTarget
	err := s.db.WithContext(ctx).Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
		platform, owner, repo, number).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if t.KnownCommentIDs == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(t.KnownCommentIDs), &ids); err != nil {
		return nil, nil // 缓存损坏按无缓存处理，不阻断回帖
	}
	// 新→旧：倒序遍历（最新评论最可能仍有效）
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids, nil
}
