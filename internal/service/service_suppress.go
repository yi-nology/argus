// service_suppress.go —— 误报抑制的 MCP 数据面（v3.128.0，scope 透传 admin 门）：
// HTTP /findings/suppress 的写语义复用 SuppressFinding 单源，本文件只补
// 「MCP 工具级权限二次判定」——/mcp 路由鉴权只要 ci 档，而抑制写库进学习环
// 是信任边界操作，必须按工具级 admin 收口（fail-closed：ctx 无 scope 一律拒）。
package service

import (
	"context"
	"fmt"
	"net/http"

	api "argus/biz/model/api"
)

// SuppressFindingScoped 单条意见的误报抑制（MCP suppress_finding 数据面）：
// 按 fp 短码解析入库意见 → 置 suppressed → 知识回流 + 学习环负票（SuppressFinding
// 单源执行）。fp 为报告/list_findings 的指纹短码（≥6 位），行号/评语漂移由
// 入库指纹覆盖（模式一语义）。
func (s *Service) SuppressFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, fmt.Errorf("suppress_finding 需 admin 档 token（误报抑制是信任边界写操作，scope 透传二次判定），当前档位不足")
	}
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner/repo 不能为空")
	}
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	if !isDigits(number) || len(number) > 10 {
		return nil, fmt.Errorf("number 必须为纯数字 PR 序号")
	}
	if len(fp) < 6 || len(fp) > 64 {
		return nil, fmt.Errorf("fp 须为报告中的指纹短码（6-64 位十六进制）")
	}
	resp, code := s.SuppressFinding(ctx, &api.SuppressFindingRequest{
		Platform:    platform,
		Owner:       owner,
		Repo:        repo,
		Number:      number,
		Fingerprint: &fp,
	})
	if code != http.StatusOK {
		return nil, fmt.Errorf("抑制失败（HTTP %d）", code)
	}
	return map[string]any{"matched": resp.Matched, "fingerprint": resp.Fingerprint}, nil
}
