package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestRAG_EmptyDir 空目录 → 索引为空，查询不 panic。
func TestRAG_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunks, _ := l.Retrieve(context.Background(), "任何查询", 5, nil)
	if len(chunks) != 0 {
		t.Fatalf("空目录应返回空，got %d", len(chunks))
	}
}

// TestRAG_NonExistentDir 目录不存在 → 返回错误。
func TestRAG_NonExistentDir(t *testing.T) {
	if _, err := NewLocal("/tmp/nonexistent-dir-xyz-abc"); err == nil {
		t.Fatal("不存在的目录应返回错误")
	}
}

// TestRAG_SingleCharQuery 单字符查询 → 不 panic，可能命中（CJK 单字）。
func TestRAG_SingleCharQuery(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# 测试\n\nGo 错误处理规范\n"), 0o644)
	l, _ := NewLocal(dir)
	chunks, _ := l.Retrieve(context.Background(), "Go", 3, nil)
	// 单字符可能命中也可能不命中（取决于 tokenization），不 panic 即可
	t.Logf("单字符查询命中 %d 条", len(chunks))
}

// TestRAG_CJKBigram CJK 二元组检索。
func TestRAG_CJKBigram(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cn.md"), []byte("# 中文知识\n\n微服务部署必须走 CI/CD 流水线，禁止手动上线\n"), 0o644)
	l, _ := NewLocal(dir)
	chunks, _ := l.Retrieve(context.Background(), "微服务 手动", 3, nil)
	if len(chunks) == 0 {
		t.Fatal("CJK 二元组应命中")
	}
	if !strings.Contains(chunks[0].Content, "CI/CD") {
		t.Fatalf("命中内容不符: %s", chunks[0].Content)
	}
}

// TestRAG_Filter 精确过滤。
func TestRAG_Filter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# 规范A\n\n错误处理\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("# 规范B\n\n日志要求\n"), 0o644)
	l, _ := NewLocal(dir)
	// 过滤 file=a.md → 只命中 a.md
	chunks, _ := l.Retrieve(context.Background(), "错误", 5, Filter{"file": "a.md"})
	for _, c := range chunks {
		if c.Metadata["file"] != "a.md" {
			t.Fatalf("过滤后不应返回其他文件: %+v", c)
		}
	}
}

// TestRAG_ScoreNonOverlap 不相关词元 → 零分，不命中。
func TestRAG_ScoreNonOverlap(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# 规范\n\nGo 错误处理\n"), 0o644)
	l, _ := NewLocal(dir)
	chunks, _ := l.Retrieve(context.Background(), "quantum entanglement", 3, nil)
	if len(chunks) != 0 {
		t.Fatalf("无关查询应返回空，got %d", len(chunks))
	}
}

// TestRAG_TopKLimit topK 限制返回数量。
func TestRAG_TopKLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dir, string(rune('a'+i))+".md"),
			[]byte("# 知识\n\nGo 错误处理规范 "+string(rune('a'+i))+"\n"), 0o644)
	}
	l, _ := NewLocal(dir)
	chunks, _ := l.Retrieve(context.Background(), "Go 错误", 3, nil)
	if len(chunks) > 3 {
		t.Fatalf("topK=3 应至多3条，got %d", len(chunks))
	}
}

// TestRAG_ToolOutputHasSource 工具输出包含出处标记。
func TestRAG_ToolOutputHasSource(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("# 部署\n\n生产环境禁止 kubectl apply\n"), 0o644)
	l, _ := NewLocal(dir)
	inv, ok := l.AsTool().(tool.InvokableTool)
	if !ok {
		t.Fatal("AsTool 应为 InvokableTool")
	}
	out, err := inv.InvokableRun(context.Background(), `{"query":"kubectl apply 生产"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deploy.md") || !strings.Contains(out, "kubectl") {
		t.Fatalf("工具输出应含出处与命中内容: %s", out)
	}
}
