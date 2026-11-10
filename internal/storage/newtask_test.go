package storage

import (
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// TestNewTask 钉工厂不变量：四个建任务入口（taskapi/webhook×2/poller）此前
// 各自组装 ID/Status/CreatedAt，漏一处即幻影 ID/缺时间戳形态漂移。
func TestNewTask(t *testing.T) {
	before := time.Now().Add(-time.Second)
	task := NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "7")
	after := time.Now().Add(time.Second)

	if task.ID == "" {
		t.Error("NewTask 未填 ID（幻影 ID 形态）")
	}
	if task.Status != StatusPending {
		t.Errorf("NewTask Status = %q, want pending", task.Status)
	}
	if task.CreatedAt.Before(before) || task.CreatedAt.After(after) {
		t.Errorf("NewTask CreatedAt %v 超出构建窗口", task.CreatedAt)
	}
	if task.Kind != domain.TaskKindCodeReview || task.Platform != "gitea" ||
		task.Owner != "o" || task.Repo != "r" || task.Number != "7" {
		t.Errorf("身份字段透传失真: %+v", task)
	}
	// kind 专属字段必须由调用方显式补——工厂不猜。
	if task.HeadSHA != "" || task.Priority != 0 || task.EventID != nil || task.ArtifactsJSON != "" {
		t.Errorf("工厂越权预填专属字段: %+v", task)
	}
	// 每次调用独立 ID（复用会撞主键/去重键）。
	if NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "7").ID == task.ID {
		t.Error("两次 NewTask 产出相同 ID")
	}
}
