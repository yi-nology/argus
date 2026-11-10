// service_suppress.go —— 误报抑制的 MCP 数据面（v3.128.0，scope 透传 admin 门）：
// HTTP /findings/suppress 的写语义复用 SuppressFinding 单源，本文件只补
// 「MCP 工具级权限二次判定」——/mcp 路由鉴权只要 ci 档，而抑制写库进学习环
// 是信任边界操作，必须按工具级 admin 收口（fail-closed：ctx 无 scope 一律拒）。

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/plugin"
)

// SuppressFindingScoped 单条意见的误报抑制（MCP suppress_finding 数据面）：
// 按 fp 短码解析入库意见 → 置 suppressed → 知识回流 + 学习环负票（SuppressFinding
// 单源执行）。fp 为报告/list_findings 的指纹短码（≥6 位），行号/评语漂移由
// 入库指纹覆盖（模式一语义）。
func (s *Service) SuppressFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, fmt.Errorf("suppress_finding 需 admin 档 token（误报抑制是信任边界写操作，scope 透传二次判定），当前档位不足")
	}
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, errors.New(errRepoDenied)
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
	resp, code := s.SuppressFinding(ctx, &argus.SuppressFindingRequest{
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

// RestoreFindingScoped 恢复被抑制的意见（MCP restore_finding / HTTP
// /findings/restore 的数据面，v3.131.0）：suppress 反向操作——置回 open，下一轮
// 起重新可见。与评论命令 "restore fp:xxx" 共用 restoreFindingCore 单源；权限
// 同 suppress（admin scope 二次判定，fail-closed）。
func (s *Service) RestoreFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	if ScopeFromContext(ctx) != "admin" {
		return nil, fmt.Errorf("restore_finding 需 admin 档 token（scope 透传二次判定），当前档位不足")
	}
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, errors.New(errRepoDenied)
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
	matched, err := s.restoreFindingCore(ctx, platform, owner, repo, number, strings.ToLower(fp))
	if err != nil {
		return nil, err
	}
	return map[string]any{"matched": matched}, nil
}

// restoreFindingCore 恢复执行体（评论命令与 API/MCP 共用单源）：短码解析入库
// 身份 → 置回 open → 审计 → 学习环 PatternRestore。不回流知识（恢复只是撤销
// 剔除，不是新结论）。
func (s *Service) restoreFindingCore(ctx context.Context, platform, owner, repo, number, fpPrefix string) (bool, error) {
	num, err := strconv.Atoi(number)
	if err != nil {
		return false, fmt.Errorf("number 必须为纯数字 PR 序号")
	}
	// 学习环：解析完整行——restore 的 pattern 键必须由入库原始评语派生
	//（固定 "restore:"+短码会让 hex 被剥离后塌缩到垃圾键，v3.8.0 教训）
	var pluginName, fullFP string
	var resolvedFinding plugin.Finding
	if s.Cfg != nil && s.Cfg.RegretLearning {
		if rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, number, fpPrefix); ok {
			fullFP = rec.Print
			pluginName = rec.Plugin
			resolvedFinding = plugin.Finding{
				File: rec.File, Line: rec.Line, Severity: rec.Severity,
				Comment: rec.Comment, By: rec.Plugin,
			}
		}
	}
	matched, err := s.Store.RestorePrint(ctx, platform, owner, repo, num, fpPrefix)
	if err != nil {
		return false, err
	}
	s.Audit.Log(auditFindingRestore, "platform", platform, "owner", owner,
		"repo", repo, "number", number, "fingerprint", fpPrefix, "matched", matched,
		"token_fp", TokenFPFromContext(ctx))
	if matched && s.Cfg != nil && s.Cfg.RegretLearning && fullFP != "" {
		if err := s.Store.PatternRestore(ctx, platform, owner, repo, pluginName,
			resolvedFinding, time.Now()); err != nil {
			s.log().Warn("argus.regret.pattern_restore_failed", "error", err.Error())
		}
	}
	return matched, nil
}
