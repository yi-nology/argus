package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func seedKnowledge(t *testing.T) *Local {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"coding-standard.md": "# 编码规范\n\n## 错误处理\n\nGo 服务返回错误必须包装上下文：fmt.Errorf 带 %w。\n禁止裸 return err，一层以上调用链必须补充语义。\n\n## 日志\n\n日志使用 logx 结构化键值对，禁止 fmt.Println。\n",
		"deploy.md":          "# 部署约定\n\n生产部署走 ArgoCD，禁止 kubectl 直接 apply 到生产集群。\n镜像推送 Harbor 后再触发同步。\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestLocalRetrieve 中文二元组检索命中规范条文。
func TestLocalRetrieve(t *testing.T) {
	l := seedKnowledge(t)
	chunks, err := l.Retrieve(context.Background(), "错误处理 裸return", 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("应至少命中 1 条")
	}
	if !strings.Contains(chunks[0].Content, "fmt.Errorf") {
		t.Fatalf("top1 应是错误处理条文: %+v", chunks[0])
	}
	if chunks[0].Metadata["file"] != "coding-standard.md" {
		t.Fatalf("metadata.file 不符: %v", chunks[0].Metadata)
	}
}

// TestLocalRetrieveNoHit 无关查询返回空（不硬凑结果）。
func TestLocalRetrieveNoHit(t *testing.T) {
	l := seedKnowledge(t)
	chunks, _ := l.Retrieve(context.Background(), "quantum entanglement 深渊巨口 zz", 3, nil)
	if len(chunks) != 0 {
		t.Fatalf("无关查询应返回空，got %d", len(chunks))
	}
}

// TestLocalAsTool 工具化：search_knowledge 返回带出处的片段。
func TestLocalAsTool(t *testing.T) {
	l := seedKnowledge(t)
	knowledgeTool := l.AsTool()
	if knowledgeTool == nil {
		t.Fatal("AsTool 不应为 nil")
	}
	inv, ok := knowledgeTool.(tool.InvokableTool)
	if !ok {
		t.Fatal("应为 InvokableTool")
	}
	res, err := inv.InvokableRun(context.Background(), `{"query":"生产集群 kubectl apply 禁止"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), "deploy.md") || !strings.Contains(string(b), "ArgoCD") {
		t.Fatalf("工具输出应含出处与命中内容: %s", b)
	}
}
