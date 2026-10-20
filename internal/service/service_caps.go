// service_caps.go —— 能力面快照（v3.129.0，MCP get_capabilities 数据面）：
// 一次看清「这个部署开了什么」——外部 agent 免盲调报错探测（fix 未配/
// 检索未挂等在这里先知道，而不是调用工具吃错误文案）。
package service

import (
	"context"
	"strings"

	"github.com/oagentkit/rag/rag"
)

// Capabilities 能力面快照（纯读配置与装配态，零 IO）。
func (s *Service) Capabilities(context.Context) map[string]any {
	knowledgeOn := false
	if s.Knowledge != nil {
		if _, noop := s.Knowledge.(*rag.Noop); !noop {
			knowledgeOn = true
		}
	}
	allowlist := "all"
	if s.Cfg != nil && len(s.Cfg.RepoAllowlist) > 0 {
		allowlist = strings.Join(s.Cfg.RepoAllowlist, ",")
	}
	return map[string]any{
		"repo_allowlist": allowlist,
		"version":        s.Version,
		"llm":            s.LLM != nil || s.Model != nil,
		"diff_preview":   s.LLM != nil,
		"qa":             s.Model != nil,
		"code_search":    s.Cfg != nil && s.Cfg.CodeSearch && s.WC != nil,
		"sem_search":     s.Cfg != nil && s.Cfg.SemSearch && s.SemSearch != nil,
		"knowledge":      knowledgeOn,
		"fix_agent":      s.Cfg != nil && s.Cfg.FixAgent != "",
		"fix_push":       s.Cfg != nil && s.Cfg.FixPush,
		"codegraph":      s.Cfg != nil && s.Cfg.Codegraph,
		"related_repos":  s.Cfg != nil && s.Cfg.RelatedRepos,
		"ocr":            s.Cfg != nil && s.Cfg.OcrBin != "",
	}
}
