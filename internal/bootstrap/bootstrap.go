// Package bootstrap 启动装配：plugins.yaml → 三类适配器 → Registry。
// 信任锚点（§3.4/§8）：cli 二进制与 remote endpoint 只能来自本文件处理的 Hub 全局配置，
// 仓库配置（.reviewagents.yaml）永远只能"选"不能"增"。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/cloudwego/eino/components/model"

	"github.com/oekit/logx"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oagentkit/tools/workcopy"

	"argus/internal/config"
	"argus/internal/dispatch"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_acpx"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/plugin/adapter_cli"
	"argus/internal/plugin/adapter_remote"
)

// Deps 构造插件所需的共享依赖。
type Deps struct {
	Cfg    *config.Config
	Log    logx.Logger
	WC     *workcopy.Pool
	Model  model.BaseChatModel // 可 nil（无 LLM 配置时仅 cli 插件可用）
	Skills skill.Provider
	// Plugins 预加载的插件清单（Assemble 统一加载一次后传入，MCP 工具表/编队段共用）；
	// nil = BuildRegistry 自行加载，缺文件回退内置默认（独立调用方保持原语义）。
	Plugins *plugin.PluginsFile
	// WorkflowFactory 由 runner 提供（requirement-compliance 插件延迟注册）。
	// 契约类型在 plugin（v3.5.7 上提；adapter_builtin.WorkflowFactory 是其别名）。
	WorkflowFactory plugin.WorkflowFactory
	// ReviewTools 由 runner 提供（builtin Reviewer 的 ReAct 工具表钩子；nil = 单轮模式）。
	ReviewTools plugin.ToolsHook
	// ReviewerLLM / ReviewerName 阶段覆盖的 Reviewer 生成器（P6：ARGUS_REVIEWER_MODEL；
	// nil = Reviewer 用全局 Model 构造）。
	ReviewerLLM  llm.Generator
	ReviewerName string
	// ReactModel ReAct 专用模型（已 Failover 装饰）；nil = 用 Model / ReviewerLLM.RawModel。
	// Resilient.RawModel 只返回主模型，ReAct 旁路靠本字段补齐主备切换。
	ReactModel model.BaseChatModel
}

// BuildRegistry 读取 plugins.yaml（缺文件时回退内置默认配置；Deps.Plugins 非空时
// 直接使用预加载清单）并注册全部插件。
// 返回 registry 与 requirement-compliance 的 skillRefs（要传给 runner 做 R3 注入）。
func BuildRegistry(d *Deps) (*dispatch.Registry, []string, error) {
	pf := d.Plugins
	if pf == nil {
		var err error
		if pf, err = loadPluginsFile(d.Cfg, d.Log); err != nil {
			return nil, nil, err
		}
	}

	reg := dispatch.NewRegistry()
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
		// best-of-N 采样次数钳制（0/负 = 关闭；上限 MaxReviewN 防配置滥用）。
		// 执行期 Dispatcher.Run 再经 ClampReviewN 归一，manifest 保留语义原值。
		reviewN := decl.ReviewN
		if reviewN > dispatch.MaxReviewN {
			d.Log.Warn("argus.bootstrap.review_n_clamped", "plugin", decl.Name,
				"configured", decl.ReviewN, "effective", dispatch.MaxReviewN)
			reviewN = dispatch.MaxReviewN
		}
		if reviewN < 0 {
			d.Log.Warn("argus.bootstrap.review_n_clamped", "plugin", decl.Name,
				"configured", decl.ReviewN, "effective", 0)
			reviewN = 0
		}
		manifest := dispatch.AgentManifest{
			Name:     decl.Name,
			Version:  decl.Version,
			Kind:     plugin.Kind(decl.Kind),
			Protocol: plugin.Protocol(decl.Protocol),
			Skills:   decl.Skills,
			Needs:    needs,
			Triggers: plugin.Trigger{Paths: decl.Triggers.Paths, Labels: decl.Triggers.Labels, Events: decl.Triggers.Events},
			Cost:     decl.Cost,
			Timeout:  timeout,
			ReviewN:  reviewN,
		}

		var p plugin.AgentPlugin
		isCompliance := false
		switch manifest.Kind {
		case plugin.KindBuiltin:
			p, isCompliance, err = newBuiltinAgent(d, decl, manifest)
			if err != nil {
				return nil, nil, err
			}
		case plugin.KindCLI:
			p, enabled = newCLIAgent(d, decl, manifest)
		case plugin.KindRemote:
			p, enabled = newRemoteAgent(d, decl, manifest)
		case plugin.KindACPX:
			p, enabled = newACPXAgent(d, decl, manifest)
		default:
			return nil, nil, fmt.Errorf("插件 %s kind 非法: %s", decl.Name, decl.Kind)
		}
		if isCompliance {
			complianceSkillRefs = decl.SkillRefs
		}

		if err := reg.Register(&dispatch.Registration{
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

// newBuiltinAgent builtin 工厂：requirement-compliance 外壳（isCompliance=true，主循环
// 收集 skillRefs）或 Reviewer（P6 覆盖链 + ReAct 工具表）。错误 = 启动失败（fail-fast）。
func newBuiltinAgent(d *Deps, decl *plugin.AgentDecl, manifest dispatch.AgentManifest) (
	plugin.AgentPlugin, bool, error) {
	if strings.EqualFold(decl.Name, "requirement-compliance") ||
		slices.Contains(decl.Skills, plugin.SkillRequirementCompliance) {
		if d.WorkflowFactory == nil {
			return nil, false, fmt.Errorf("requirement-compliance 插件缺少 workflow factory")
		}
		p, err := adapter_builtin.NewCompliance(manifest, d.WorkflowFactory)
		if err != nil {
			return nil, false, err
		}
		return p, true, nil
	}
	if d.Model == nil {
		d.Log.Warn("argus.bootstrap.builtin_no_llm", "plugin", decl.Name,
			"reason", "未配置 OPENAI_*，插件将不可用")
	}
	reviewerName := d.Cfg.LLM.Model
	if d.ReviewerName != "" { // P6 阶段覆盖：R4 Reviewer 专用模型
		reviewerName = d.ReviewerName
	}
	var client llm.Generator = llm.NewClient(d.Model, reviewerName, nil)
	if c, ok := client.(*llm.Client); ok && d.ReviewerLLM == nil {
		c.ContextTokens = d.Cfg.LLM.ContextToken
		c.MaxOutputTokens = d.Cfg.MaxTokensResolved()
		// v5.5.2：插件单轮兜底路径（ReAct 失败后落这里）此前恒用库默认
		// 3 试/2s——分钟级 RPM 限流下退避够不着窗口，security 等插件被
		// 429 静默打掉；韧性参数在此同样接线。
		c.MaxRetries = d.Cfg.LLM.LLMResilienceRetries + 1
		c.BaseDelay = d.Cfg.LLM.LLMResilienceBaseDelay
	}
	if d.ReviewerLLM != nil {
		client = d.ReviewerLLM
	}
	var opts []adapter_builtin.ReviewerOption
	opts = append(opts, adapter_builtin.WithLogger(d.Log))
	if d.ReviewTools != nil {
		// ReAct 模式（oagentkit agentrun + R3 同源工具表）；单轮路径保留为降级兜底
		opts = append(opts, adapter_builtin.WithToolsHook(d.ReviewTools))
	}
	if d.ReactModel != nil {
		opts = append(opts, adapter_builtin.WithReactModel(d.ReactModel))
	}
	// ReAct 迭代上限（ARGUS_REACT_MAX_ITERATIONS；<=0 = builtin 内缺省 12）
	if d.Cfg.ReactMaxIterations > 0 {
		opts = append(opts, adapter_builtin.WithReactMaxIterations(d.Cfg.ReactMaxIterations))
	}
	p, err := adapter_builtin.NewReviewer(manifest, reviewerFocus(decl.Name, decl.Skills),
		client, decl.SkillRefs, d.Skills, opts...)
	if err != nil {
		return nil, false, err
	}
	return p, false, nil
}

// newCLIAgent cli 工厂。返回 enabled=false 的情形一律装 unavailablePlugin（可观测）：
// 未声明 cmd / delegate 模式（delegate 由 runner 前置实装）/ 可执行文件不存在（§8 供应链
// 白名单：绝不尝试其他解析路径）。
func newCLIAgent(d *Deps, decl *plugin.AgentDecl, manifest dispatch.AgentManifest) (plugin.AgentPlugin, bool) {
	if len(decl.Cmd) == 0 {
		return &unavailablePlugin{manifest: manifest, reason: "plugins.yaml 未声明 cmd"}, false
	}
	if strings.EqualFold(decl.Mode, "delegate") {
		// delegate 产物是审查输入而非 findings，装不进插件契约（运行期必报错）——
		// 已实装为 runner 层前置步骤（internal/ocrdeleg，ARGUS_OCR_BIN 启用）。
		// 启动期即停用并注明，不注册成"可用"插件每个 PR 撞一次错、白烧熔断额度。
		return &unavailablePlugin{manifest: manifest,
			reason: "cli 插件无 delegate 模式（delegate 由 runner 前置实装：ARGUS_OCR_BIN）"}, false
	}
	if _, err := exec.LookPath(decl.Cmd[0]); err != nil {
		// 供应链信任（§8）：白名单可执行文件不存在 → 不启用（绝不尝试其他解析路径）
		d.Log.Warn("argus.bootstrap.cli_binary_missing", "plugin", decl.Name, "cmd", decl.Cmd[0])
		return &unavailablePlugin{manifest: manifest, reason: "可执行文件不存在: " + decl.Cmd[0]}, false
	}
	return adapter_cli.New(manifest, decl.Cmd, decl.Mode, decl.Env, d.WC, d.Log), true
}

// newRemoteAgent remote 工厂。protocol 白名单（深挖修复）：拼错的 protocol 此前启动
// 照常注册，运行期每次 Invoke 走 default 分支报错 → 3 个 PR 后熔断。auth_token_env
// 声明了凭证而环境未给时同样启动期停用（与 needs:[working-copy] 缺池同款语义）。
func newRemoteAgent(d *Deps, decl *plugin.AgentDecl, manifest dispatch.AgentManifest) (plugin.AgentPlugin, bool) {
	if manifest.Protocol != plugin.ProtocolA2A && manifest.Protocol != plugin.ProtocolMCP {
		d.Log.Warn("argus.bootstrap.remote_bad_protocol", "plugin", decl.Name,
			"protocol", string(manifest.Protocol), "allowed", "a2a|mcp")
		return &unavailablePlugin{manifest: manifest, reason: "protocol 非法: " + string(manifest.Protocol)}, false
	}
	authToken := ""
	if decl.AuthTokenEnv != "" {
		authToken = os.Getenv(decl.AuthTokenEnv)
		if authToken == "" {
			// 与 needs:[working-copy] 缺池同款启动期停用：配置声明了凭证
			// 而环境未给，运行期必被对端 401——不注册成"可用"插件每个 PR 撞一次
			d.Log.Warn("argus.bootstrap.remote_auth_env_missing", "plugin", decl.Name, "env", decl.AuthTokenEnv)
			return &unavailablePlugin{manifest: manifest,
				reason: "auth_token_env 环境变量未设置: " + decl.AuthTokenEnv}, false
		}
	}
	rp, err := adapter_remote.New(manifest, manifest.Protocol, decl.Endpoint, decl.Tool, authToken, d.WC)
	if err != nil {
		d.Log.Warn("argus.bootstrap.remote_skipped", "plugin", decl.Name, "error", err.Error())
		return &unavailablePlugin{manifest: manifest, reason: "endpoint 未配置"}, false
	}
	return rp, true
}

// newACPXAgent acpx 工厂。二进制缺失（未装该 CLI）= 正常形态，停用并说明；未知名才值得留 probe。
func newACPXAgent(d *Deps, decl *plugin.AgentDecl, manifest dispatch.AgentManifest) (plugin.AgentPlugin, bool) {
	if decl.Agent == "" {
		return &unavailablePlugin{manifest: manifest, reason: "plugins.yaml 未声明 agent"}, false
	}
	ap, err := adapter_acpx.New(manifest, decl.Agent, d.WC, d.Log)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, adapter_acpx.ErrBinaryMissing) {
			d.Log.Warn("argus.bootstrap.acpx_binary_missing", "plugin", decl.Name, "error", reason)
		} else {
			d.Log.Warn("argus.bootstrap.acpx_skipped", "plugin", decl.Name, "error", reason)
		}
		return &unavailablePlugin{manifest: manifest, reason: reason}, false
	}
	return ap, true
}

// reviewerFocus 不同 builtin 审查插件的人设差异（§3.2：correctness / security）。
func reviewerFocus(name string, skills []string) string {
	switch {
	case slices.Contains(skills, "security") || strings.Contains(strings.ToLower(name), "security"):
		return "聚焦安全审查：注入（SQL/命令/XSS）、越权、敏感信息泄漏、不安全反序列化、路径穿越、弱随机、错误处理中的信息泄露。"
	default:
		return "聚焦正确性审查：逻辑错误、边界条件、并发与竞态、资源泄漏、错误被吞、空指针/越界。"
	}
}

// loadPluginsFile 插件清单统一加载（缺文件回退内置默认，告警一次）。
// v3.5.9：原 BuildRegistry 与 Assemble 的 MCP 段各加载一次，收敛为单次解析共用。
// 深挖修复：区分「文件不存在」（合理兜底）与「存在但解析失败」（fail-fast）——
// 一个 YAML 缩进错误让全部自定义 agents/squads 静默替换为内置默认，生产上
// 表现为"插件神秘消失"，必须启动期报错而不是降级硬扛。
func loadPluginsFile(cfg *config.Config, log logx.Logger) (*plugin.PluginsFile, error) {
	pf, err := plugin.LoadPluginsFile(cfg.PluginsFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Warn("argus.bootstrap.plugins_file_missing", "file", cfg.PluginsFile,
				"error", err.Error(), "fallback", "使用内置默认插件配置")
			return defaultPluginsFile(), nil
		}
		return nil, fmt.Errorf("插件清单解析失败 %s: %w", cfg.PluginsFile, err)
	}
	if len(pf.Agents) == 0 {
		log.Warn("argus.bootstrap.plugins_empty", "file", cfg.PluginsFile)
	}
	return pf, nil
}

// unavailablePlugin 已声明但不可用的插件（可观测：/agents 能看到它以及为何没启用）。
type unavailablePlugin struct {
	manifest dispatch.AgentManifest
	reason   string
}

func (u *unavailablePlugin) Manifest() dispatch.AgentManifest { return u.manifest }

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
			// 600s（v5.2.1）：300s 在推理型模型（mimo-v2.6-pro 实测）下频繁
			// context deadline exceeded——单次审查链含工具核实，推理耗时不可控。
			Cost: plugin.CostStandard, Timeout: "600s", Enabled: &yes,
		},
		{
			Name: "security", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview},
			Needs:  []string{"diff", "repo-file-read"},
			// 600s（v5.2.1）：原 120s 对推理模型过紧（同上实测），锁定插件
			// 超时失败还会打完备性告警。
			Cost: plugin.CostHeavy, Timeout: "600s", LockRepoConfig: true, Enabled: &yes,
		},
		{
			Name: "ocr", Kind: "cli",
			Skills: []string{plugin.SkillCodeReview},
			Needs:  []string{"working-copy"},
			// 按 PATH 解析（LookPath 失败 → 插件停用且可观测），不硬编码个人机器路径
			Cmd:  []string{"ocr", "review", "--format", "json"},
			Mode: "full", Cost: plugin.CostHeavy, Timeout: "300s", Enabled: &yes,
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
