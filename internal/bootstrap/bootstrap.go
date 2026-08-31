// Package bootstrap 启动装配：plugins.yaml → 三类适配器 → Registry。
// 信任锚点（§3.4/§8）：cli 二进制与 remote endpoint 只能来自本文件处理的 Hub 全局配置，
// 仓库配置（.reviewagents.yaml）永远只能"选"不能"增"。
package bootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/components/model"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/knowledge/skill"
	"argus/internal/llm"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/plugin/adapter_cli"
	"argus/internal/plugin/adapter_remote"
)

// Deps 构造插件所需的共享依赖。
type Deps struct {
	Cfg    *config.Config
	Log    logx.Logger
	WC     *argusctx.Workcopy
	Model  model.BaseChatModel // 可 nil（无 LLM 配置时仅 cli 插件可用）
	Skills skill.Provider
	// WorkflowFactory 由 runner 提供（requirement-compliance 插件延迟注册）。
	WorkflowFactory adapter_builtin.WorkflowFactory
}

// BuildRegistry 读取 plugins.yaml（缺文件时回退内置默认配置）并注册全部插件。
// 返回 registry 与 requirement-compliance 的 skillRefs（要传给 runner 做 R3 注入）。
func BuildRegistry(d *Deps) (*plugin.Registry, []string, error) {
	pf, err := plugin.LoadPluginsFile(d.Cfg.PluginsFile)
	if err != nil {
		d.Log.Warn("argus.bootstrap.plugins_file_missing", "file", d.Cfg.PluginsFile,
			"error", err.Error(), "fallback", "使用内置默认插件配置")
		pf = defaultPluginsFile()
	} else if len(pf.Agents) == 0 {
		d.Log.Warn("argus.bootstrap.plugins_empty", "file", d.Cfg.PluginsFile)
	}

	reg := plugin.NewRegistry()
	var complianceSkillRefs []string

	for i := range pf.Agents {
		decl := &pf.Agents[i]
		if decl.Name == "" || decl.Kind == "" {
			d.Log.Warn("argus.bootstrap.decl_invalid", "index", i)
			continue
		}
		enabled := true
		if decl.Enabled != nil {
			enabled = *decl.Enabled
		}
		timeout, err := decl.ParseTimeout()
		if err != nil {
			return nil, nil, fmt.Errorf("插件 %s timeout 非法: %w", decl.Name, err)
		}
		needs, unknownNeeds := plugin.NeedsFromStrings(decl.Needs)
		if len(unknownNeeds) > 0 {
			d.Log.Warn("argus.bootstrap.unknown_needs", "plugin", decl.Name, "needs", strings.Join(unknownNeeds, ","))
		}
		manifest := plugin.AgentManifest{
			Name:     decl.Name,
			Version:  decl.Version,
			Kind:     plugin.Kind(decl.Kind),
			Protocol: plugin.Protocol(decl.Protocol),
			Skills:   decl.Skills,
			Needs:    needs,
			Triggers: plugin.Trigger{Paths: decl.Triggers.Paths, Labels: decl.Triggers.Labels, Events: decl.Triggers.Events},
			Cost:     decl.Cost,
			Timeout:  timeout,
		}

		var p plugin.AgentPlugin
		switch manifest.Kind {
		case plugin.KindBuiltin:
			if strings.EqualFold(decl.Name, "requirement-compliance") ||
				containsSkill(decl.Skills, plugin.SkillRequirementCompliance) {
				if d.WorkflowFactory == nil {
					return nil, nil, fmt.Errorf("requirement-compliance 插件缺少 workflow factory")
				}
				p, err = adapter_builtin.NewCompliance(manifest, d.WorkflowFactory)
				if err != nil {
					return nil, nil, err
				}
				complianceSkillRefs = decl.SkillRefs
			} else {
				if d.Model == nil {
					d.Log.Warn("argus.bootstrap.builtin_no_llm", "plugin", decl.Name,
						"reason", "未配置 OPENAI_*，插件将不可用")
				}
				client := llm.NewClient(d.Model, d.Cfg.LLM.Model, nil)
				client.ContextTokens = d.Cfg.LLM.ContextToken
				client.MaxOutputTokens = d.Cfg.MaxTokensResolved()
				p, err = adapter_builtin.NewReviewer(manifest, reviewerFocus(decl.Name, decl.Skills),
					client, decl.SkillRefs, d.Skills)
				if err != nil {
					return nil, nil, err
				}
			}
		case plugin.KindCLI:
			if len(decl.Cmd) == 0 {
				enabled = false
				p = &unavailablePlugin{manifest: manifest, reason: "plugins.yaml 未声明 cmd"}
			} else if _, err := exec.LookPath(decl.Cmd[0]); err != nil {
				// 供应链信任（§8）：白名单可执行文件不存在 → 不启用（绝不尝试其他解析路径）
				d.Log.Warn("argus.bootstrap.cli_binary_missing", "plugin", decl.Name, "cmd", decl.Cmd[0])
				enabled = false
				p = &unavailablePlugin{manifest: manifest, reason: "可执行文件不存在: " + decl.Cmd[0]}
			} else {
				p = adapter_cli.New(manifest, decl.Cmd, decl.Mode, decl.Env, d.WC, d.Log)
			}
		case plugin.KindRemote:
			rp, err := adapter_remote.New(manifest, manifest.Protocol, decl.Endpoint, decl.Tool)
			if err != nil {
				d.Log.Warn("argus.bootstrap.remote_skipped", "plugin", decl.Name, "error", err.Error())
				enabled = false
				p = &unavailablePlugin{manifest: manifest, reason: "endpoint 未配置"}
				break
			}
			p = rp
		default:
			return nil, nil, fmt.Errorf("插件 %s kind 非法: %s", decl.Name, decl.Kind)
		}

		if err := reg.Register(&plugin.Registration{
			Manifest:       p.Manifest(),
			Plugin:         p,
			LockRepoConfig: decl.LockRepoConfig,
			SkillRefs:      decl.SkillRefs,
			Enabled:        enabled,
		}); err != nil {
			return nil, nil, err
		}
		d.Log.Info("argus.bootstrap.plugin_registered", "plugin", decl.Name,
			"kind", string(manifest.Kind), "enabled", enabled)
	}
	return reg, complianceSkillRefs, nil
}

// reviewerFocus 不同 builtin 审查插件的人设差异（§3.2：correctness / security）。
func reviewerFocus(name string, skills []string) string {
	switch {
	case containsSkill(skills, "security") || strings.Contains(strings.ToLower(name), "security"):
		return "聚焦安全审查：注入（SQL/命令/XSS）、越权、敏感信息泄漏、不安全反序列化、路径穿越、弱随机、错误处理中的信息泄露。"
	default:
		return "聚焦正确性审查：逻辑错误、边界条件、并发与竞态、资源泄漏、错误被吞、空指针/越界。"
	}
}

func containsSkill(skills []string, want string) bool {
	for _, s := range skills {
		if s == want {
			return true
		}
	}
	return false
}

// unavailablePlugin 已声明但不可用的插件（可观测：/agents 能看到它以及为何没启用）。
type unavailablePlugin struct {
	manifest plugin.AgentManifest
	reason   string
}

func (u *unavailablePlugin) Manifest() plugin.AgentManifest { return u.manifest }

func (u *unavailablePlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, fmt.Errorf("插件不可用: %s", u.reason)
}

// defaultPluginsFile 内置默认插件配置（plugins.yaml 缺失时的兜底，等价 design §12 示例）。
func defaultPluginsFile() *plugin.PluginsFile {
	yes := true
	enabledFalse := false
	return &plugin.PluginsFile{Agents: []plugin.AgentDecl{
		{
			Name: "requirement-compliance", Kind: "builtin",
			Skills: []string{plugin.SkillRequirementCompliance},
			Needs:  []string{"diff", "repo-file-read", "requirement-spec"},
			Cost:   plugin.CostHeavy, Timeout: "600s", LockRepoConfig: true,
			SkillRefs: []string{"ocr-grading"}, Enabled: &yes,
		},
		{
			Name: "correctness", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview},
			Needs:  []string{"diff", "repo-file-read"},
			Cost:   plugin.CostStandard, Timeout: "300s", Enabled: &yes,
		},
		{
			Name: "security", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview},
			Needs:  []string{"diff", "repo-file-read"},
			Cost:   plugin.CostHeavy, Timeout: "120s", LockRepoConfig: true, Enabled: &yes,
		},
		{
			Name: "ocr", Kind: "cli",
			Skills: []string{plugin.SkillCodeReview},
			Needs:  []string{"working-copy"},
			Cmd:    []string{"/opt/argus/bin/ocr", "diff", "--format", "json"},
			Mode:   "full", Cost: plugin.CostHeavy, Timeout: "300s", Enabled: &yes,
		},
		{
			Name: "review-service", Kind: "remote", Protocol: "mcp",
			Skills: []string{plugin.SkillCodeReview},
			Cost:   plugin.CostStandard, Timeout: "180s", Enabled: &yes,
		},
		{
			Name: "frontend-visual", Kind: "remote", Protocol: "a2a",
			Skills:  []string{plugin.SkillVisualCheck},
			Needs:   []string{"preview-url"},
			Enabled: &enabledFalse,
		},
	}}
}
