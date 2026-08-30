// Package adapter_remote remote 形态适配器（design §3.2/§3.5）。
// v3.0 只留接口位：不做 a2a/mcp 协议子集（§8 反过度设计纪律），
// e2e 用 fake AgentPlugin 直接注入 Registry 测整条链路。
// 实装锚点：review-service 的 MCP server 是第一个 remote 对接通道（§3.5 方案 B）。
package adapter_remote

import (
	"context"
	"fmt"
	"time"

	"argus/internal/plugin"
)

// Remote 远端 agent 插件占位。
type Remote struct {
	manifest plugin.AgentManifest
	Endpoint string
}

// New 按 plugins.yaml 声明构造；protocol 仅接受 a2a|mcp。
func New(m plugin.AgentManifest, protocol plugin.Protocol, endpoint string) (*Remote, error) {
	m.Kind = plugin.KindRemote
	m.Protocol = protocol
	if m.Timeout <= 0 {
		m.Timeout = 180 * time.Second
	}
	if endpoint == "" {
		return nil, fmt.Errorf("remote 插件 %s 缺少 endpoint", m.Name)
	}
	return &Remote{manifest: m, Endpoint: endpoint}, nil
}

func (r *Remote) Manifest() plugin.AgentManifest { return r.manifest }

// Invoke v3.0 明确未实现：返回错误即触发 Merger 部分失败容忍（降级注明），
// 或在注册时 enabled: false 不参与。
func (r *Remote) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, fmt.Errorf(
		"remote 插件 %s（protocol=%s）v3.0 为接口位，a2a/mcp 协议子集 v3.1 实装",
		r.manifest.Name, r.manifest.Protocol)
}
