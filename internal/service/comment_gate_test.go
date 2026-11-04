package service

import (
	"context"
	"testing"

	"argus/internal/config"
)

// actorCanDrive：open 模式零调用放行；collaborator 模式空 actor 拒绝（v5.3.0 门控，
// 自 cmd_registry_test 迁回 service——执行体面）。
func TestActorCanDrive_OpenAndEmpty(t *testing.T) {
	mk := func(gate string) *Service {
		return &Service{Cfg: &config.Config{CommentGate: gate}}
	}
	if !mk("open").actorCanDrive(context.Background(), "gitea", "o", "r", "anyone") {
		t.Fatal("open 模式应放行")
	}
	s := mk("collaborator")
	if s.actorCanDrive(context.Background(), "gitea", "o", "r", "") {
		t.Fatal("collaborator 模式空 actor 应拒绝（无需 SDK）")
	}
}
