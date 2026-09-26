// Package toolpool L5 工具子系统：MCP client 连接管理 → eino tool.BaseTool 适配（design §9.2 方向 A）。
// RealPool 直接实现 MCP 工具池（mcpServers 配置段驱动），经 internal/bootstrap 装配为任务级工具源。
package toolpool

import (
	"context"
	"os"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/mcp"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"

	"github.com/cloudwego/eino/components/tool"
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

// ---------- 真实现：agentkit/mcp.Pool 适配（v3.2；plugins.yaml mcpServers 段） ----------

// NewRealPool 从 plugins.yaml 声明构建 agentkit/mcp 连接池（含 ${VAR} 头部环境展开）。
// 声明为空返回 nil——workflow 对 nil Tools 安全（不挂工具）。
func NewRealPool(decls []plugin.MCPServerDecl, log logx.Logger) *mcp.Pool {
	if len(decls) == 0 {
		return nil
	}
	cfgs := make([]mcp.ServerConfig, 0, len(decls))
	for _, d := range decls {
		if d.Name == "" || (len(d.Command) == 0 && d.URL == "") {
			log.Warn("argus.mcp.decl_invalid", "name", d.Name,
				"reason", "缺 name 或 command/url 均空，跳过")
			continue
		}
		timeout, err := time.ParseDuration(d.Timeout)
		if err != nil {
			timeout = 0 // agentkit 缺省 30s
		}
		probe, err := time.ParseDuration(d.ProbeTimeout)
		if err != nil {
			probe = 0 // agentkit 缺省 5s（v0.10.20 探活独立预算，不误吃连接级 30s）
		}
		cfgs = append(cfgs, mcp.ServerConfig{
			Name:         d.Name,
			Command:      d.Command,
			Env:          d.Env,
			URL:          d.URL,
			Headers:      expandHeaders(d.Headers, log),
			Timeout:      timeout,
			ProbeTimeout: probe,
		})
	}
	pool := mcp.NewPool(cfgs...)
	pool.OnError = func(server string, err error) {
		log.Warn("argus.mcp.server_error", "server", server, "error", err.Error())
	}
	// 请求侧上下文透传（v0.10.20）：任务级元数据随每笔 CallTool 注入 JSON-RPC
	// _meta，server 侧可关联「哪次评审在调我」；无任务上下文（Q&A 等）不注入。
	pool.RequestMeta = CallMeta
	return pool
}

// callMetaKey 任务级 MCP 调用元数据的 ctx 键。
type callMetaKey struct{}

// WithCallMeta 注入任务级调用元数据（runner.Execute 在任务 ctx 上挂一次，
// 随 ctx 流入该任务全部 MCP 工具调用）。
func WithCallMeta(ctx context.Context, meta map[string]any) context.Context {
	return context.WithValue(ctx, callMetaKey{}, meta)
}

// CallMeta 从 ctx 取调用元数据；无注入返回 nil（metaClient 对空不注入 _meta）。
func CallMeta(ctx context.Context) map[string]any {
	meta, _ := ctx.Value(callMetaKey{}).(map[string]any)
	return meta
}

// SpecsFromDecls 汇总 plugins.yaml tools 白名单段为 runner 形态。
func SpecsFromDecls(decls []plugin.ToolSpecDecl) []ToolSpec {
	specs := make([]ToolSpec, 0, len(decls))
	for _, d := range decls {
		specs = append(specs, ToolSpec{MCP: d.Server, Allow: d.Allow})
	}
	return specs
}

// expandHeaders 头部值 ${VAR} 环境引用展开（token 不落配置文件）。
// 未定义变量保留原样并告警——显式可见优于静默空值。
func expandHeaders(headers map[string]string, log logx.Logger) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = expandEnvRef(v, log)
	}
	return out
}

func expandEnvRef(v string, log logx.Logger) string {
	const marker = "${"
	var b strings.Builder
	for {
		start := strings.Index(v, marker)
		if start < 0 {
			b.WriteString(v)
			return b.String()
		}
		end := strings.Index(v[start:], "}")
		if end < 0 {
			b.WriteString(v)
			return b.String()
		}
		end += start
		name := v[start+len(marker) : end]
		if val, ok := os.LookupEnv(name); ok {
			b.WriteString(v[:start])
			b.WriteString(val)
		} else {
			log.Warn("argus.mcp.env_ref_missing", "var", name, "action", "保留原样")
			b.WriteString(v[:end+1]) // 未定义保留原样：扫描位置跳过本标记，防死循环
		}
		v = v[end+1:]
	}
}

// RealAdapter agentkit/mcp.Pool 的 toolpool.Pool 适配（spec 词表映射 MCP→Allow）。
type RealAdapter struct {
	Pool *mcp.Pool
}

// Tools 按白名单返回 eino 工具（Argus ToolSpec → agentkit ToolSpec 映射）。
func (a *RealAdapter) Tools(ctx context.Context, specs []ToolSpec) ([]tool.BaseTool, error) {
	mspecs := make([]mcp.ToolSpec, len(specs))
	for i, s := range specs {
		mspecs[i] = mcp.ToolSpec{Server: s.MCP, Allow: s.Allow}
	}
	return a.Pool.Tools(ctx, mspecs)
}

// Close 关闭底层连接池。
func (a *RealAdapter) Close() { a.Pool.Close() }
