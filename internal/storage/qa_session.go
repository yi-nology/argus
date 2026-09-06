// Q&A 会话（v3.3 P5）：@argus 追问的多轮上下文。一个 PR 一条会话，
// 历史按轮次封顶 + 时效过期——追问超过时效视为新话题（避免跨天误连语境）。
package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// QASession 单个 PR 的问答历史。
type QASession struct {
	// SessionKey 形如 "gitea:owner/repo#12"（一 PR 一会话）。
	SessionKey  string    `gorm:"column:session_key;primaryKey;size:160"`
	HistoryJSON string    `gorm:"column:history_json;type:text"` // []QATurn 序列化
	UpdatedAt   time.Time `gorm:"column:updated_at;index"`
}

func (QASession) TableName() string { return "qa_sessions" }

// QATurn 单轮问答。
type QATurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	At       string `json:"at"` // RFC3339
}

// GetQASession 读会话（不存在返回 nil, nil）。过期（超 ttl）视为无会话。
func (s *Store) GetQASession(ctx context.Context, sessionKey string, ttl time.Duration) (*QASession, error) {
	var row QASession
	err := s.db.WithContext(ctx).Where("session_key = ?", sessionKey).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if time.Since(row.UpdatedAt) > ttl {
		return nil, nil // 过期：新话题，历史清零
	}
	return &row, nil
}

// SaveQASession 保存会话（UPSERT）。
func (s *Store) SaveQASession(ctx context.Context, sess *QASession) error {
	sess.UpdatedAt = time.Now().UTC()
	return s.db.WithContext(ctx).Save(sess).Error
}
