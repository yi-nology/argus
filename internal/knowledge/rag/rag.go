// Package rag L5 知识子系统 · RAG 检索（design §9.3）。
// v3.0 只落接口 + Noop 实现；indexer 后面接什么（Redis/ES/pgvector）到实装期再定，
// 实装时直接用 eino 原生 RAG 组件（demo08 已验证），不自造轮子。
package rag

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// Chunk 检索片段。
type Chunk struct {
	ID       string
	Content  string
	Score    float64
	Metadata map[string]string
}

// Filter 检索过滤条件（如 repo、规范文档名）。
type Filter map[string]string

// KnowledgeService 知识检索服务（design §9.3 接口原样）。
// 模式一：预检索（确定性）——workflow 阶段在拼 prompt 前取 topK 片段；
// 模式二：工具化（自主）——包成 search_knowledge(query) 挂进 ReAct agent。
type KnowledgeService interface {
	Retrieve(ctx context.Context, query string, topK int, filter Filter) ([]Chunk, error)
	AsTool() tool.BaseTool
}

// Noop 空实现：Retrieve 返回空、AsTool 返回 nil（调用方 nil 检查后不挂工具）。
type Noop struct{}

func NewNoop() *Noop { return &Noop{} }

func (n *Noop) Retrieve(_ context.Context, _ string, _ int, _ Filter) ([]Chunk, error) {
	return nil, nil
}

func (n *Noop) AsTool() tool.BaseTool { return nil }

var _ KnowledgeService = (*Noop)(nil)
