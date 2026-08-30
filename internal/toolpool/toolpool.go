// Package toolpool L5 工具子系统：MCP client 连接管理 → eino tool.BaseTool 适配（design §9.2 方向 A）。
// v3.0：接口 + mock tool（走 e2e 测试链路）。mark3labs/mcp-go 等依赖到 v3.1 实装时再进 go.mod
// （§8 扩展点纪律：未实装前不引依赖）。
package toolpool

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// ToolSpec 工具白名单声明（plugins.yaml/配置：tools: [{mcp: docs, allow: [search_docs]}]）。
type ToolSpec struct {
	MCP   string   `yaml:"mcp" json:"mcp"`
	Allow []string `yaml:"allow" json:"allow"`
}

// Pool 工具池接口：把外部工具（MCP 连接）转成 eino 工具挂进 agent 工具表。
type Pool interface {
	// Tools 返回 spec 白名单内的工具集合；无匹配返回空切片。
	Tools(ctx context.Context, specs []ToolSpec) ([]tool.BaseTool, error)
}

// MockPool v3.0 mock 实现：忽略 spec，返回一个 search_docs 假工具（e2e 断言其被调用）。
type MockPool struct {
	// Calls 记录 mock 工具被调用次数（测试断言用）。
	Calls int
}

func NewMock() *MockPool { return &MockPool{} }

type searchDocsIn struct {
	Query string `json:"query" jsonschema:"description=检索关键词"`
}

type searchDocsOut struct {
	Results []string `json:"results"`
}

// SearchDocs mock 工具实现（导出以便测试注入桩逻辑）。
func (m *MockPool) SearchDocs(_ context.Context, in *searchDocsIn) (*searchDocsOut, error) {
	m.Calls++
	return &searchDocsOut{Results: []string{fmt.Sprintf("mock 文档片段（query=%s）", in.Query)}}, nil
}

func (m *MockPool) Tools(_ context.Context, _ []ToolSpec) ([]tool.BaseTool, error) {
	t, err := utils.InferTool("search_knowledge",
		"检索团队知识库/编码规范（mock 实现，v3.1 接 MCP server）", m.SearchDocs)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{t}, nil
}

var _ Pool = (*MockPool)(nil)
