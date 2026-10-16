// Package toolpool L5 工具子系统：MCP client 连接管理 → eino tool.BaseTool 适配（design §9.2 方向 A）。
// RealPool 直接实现 MCP 工具池（mcpServers 配置段驱动），经 internal/bootstrap 装配为任务级工具源。
package toolpool

import (
	"context"
	"time"

	"git.example.com/enjoydream/agentkit/mcp"
	"github.com/oekit/logx"

	"argus/internal/plugin"

	"github.com/cloudwego/eino/components/tool"
)

// ToolSpec 工具白名单声明（plugins.yaml/配置：tools: [{mcp: docs, allow: [search_docs]}]）。
type ToolSpec struct {
	MCP   string   `yaml:"mcp" json:"mcp"`
	Allow []string `yaml:"allow" json:"allow"`
}

// RiskHints 工具风险注解（agentkit mcp.ToolHints 的消费侧投影；无注解的工具
// 按 MCP 规范缺省视为破坏性——保守缺省）。
type RiskHints struct {
	ReadOnly       bool
	Destructive    bool
	ConcurrentSafe bool
}

// Pool 工具池接口：把外部工具（MCP 连接）转成 eino 工具挂进 agent 工具表。
type Pool interface {
	// Tools 返回 spec 白名单内的工具集合；无匹配返回空切片。
	Tools(ctx context.Context, specs []ToolSpec) ([]tool.BaseTool, error)
	// Hints 返回 spec 白名单内工具的风险注解（工具名 → 注解；v3.39.0）。
	Hints(specs []ToolSpec) map[string]RiskHints
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
			Name:    d.Name,
			Command: d.Command,
			Env:     d.Env,
			URL:     d.URL,
			Headers: mcp.ExpandEnvRefs(d.Headers, func(name string) {
				log.Warn("argus.mcp.env_ref_missing", "var", name, "action", "保留原样")
			}),
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

// WithCallMeta/CallMeta v3.80.0 实现下沉 agentkit/mcp（任务级 _meta ctx 键
// 标准实现），本包保留别名——runner 调用方零改动。
var (
	WithCallMeta = mcp.WithCallMeta
	CallMeta     = mcp.CallMeta
)

// SpecsFromDecls 汇总 plugins.yaml tools 白名单段为 runner 形态。
func SpecsFromDecls(decls []plugin.ToolSpecDecl) []ToolSpec {
	specs := make([]ToolSpec, 0, len(decls))
	for _, d := range decls {
		specs = append(specs, ToolSpec{MCP: d.Server, Allow: d.Allow})
	}
	return specs
}

// RealAdapter agentkit/mcp.Pool 的 toolpool.Pool 适配（spec 词表映射 MCP→Allow）。
type RealAdapter struct {
	Pool *mcp.Pool
}

// Tools 按白名单返回 eino 工具（Argus ToolSpec → agentkit ToolSpec 映射）。
// v0.10.38 起 Pool.Tools 返回 ToolsResult：失败面已容忍且经 OnError 同源发射
// （NewRealPool 装配为 argus.mcp.server_error 日志），此处取成功集即可。
func (a *RealAdapter) Tools(ctx context.Context, specs []ToolSpec) ([]tool.BaseTool, error) {
	mspecs := make([]mcp.ToolSpec, len(specs))
	for i, s := range specs {
		mspecs[i] = mcp.ToolSpec{Server: s.MCP, Allow: s.Allow}
	}
	res, err := a.Pool.Tools(ctx, mspecs)
	return res.Tools, err
}

// Hints 汇总各 server 的目录注解为消费侧投影（v0.10.19 ToolHints）。
func (a *RealAdapter) Hints(specs []ToolSpec) map[string]RiskHints {
	out := map[string]RiskHints{}
	for _, s := range specs {
		for name, h := range a.Pool.Hints(s.MCP) {
			out[name] = RiskHints{
				ReadOnly:       h.ReadOnly,
				Destructive:    h.Destructive,
				ConcurrentSafe: h.ConcurrentSafe(),
			}
		}
	}
	return out
}

// Close 关闭底层连接池。
func (a *RealAdapter) Close() { a.Pool.Close() }
