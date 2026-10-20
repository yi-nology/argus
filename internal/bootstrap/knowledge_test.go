package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oagentkit/rag"
	"github.com/oekit/logx"

	"argus/internal/config"
)

// TestBuildKnowledge_LocalAndNoop 钉知识检索装配的本地路径：目录就绪 → Local
// （Hybrid 的词料路），目录缺失 → Noop；Hybrid 分支需活 Milvus 不入单测，
// 由日志 argus.boot.knowledge_hybrid 在部署面观测。
func TestBuildKnowledge_LocalAndNoop(t *testing.T) {
	log := logx.NewSlogLogger("test")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "k.md"), []byte("# 知识\n内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := buildKnowledge(context.Background(), &config.Config{KnowledgeDir: dir}, log)
	if _, ok := k.(*rag.Local); !ok {
		t.Fatalf("本地目录就绪应装配 Local, got %T", k)
	}
	k2 := buildKnowledge(context.Background(), &config.Config{KnowledgeDir: filepath.Join(dir, "absent")}, log)
	if _, ok := k2.(*rag.Noop); !ok {
		t.Fatalf("目录缺失应装配 Noop, got %T", k2)
	}
}
