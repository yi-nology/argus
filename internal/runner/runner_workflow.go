// runner_workflow.go —— 工作流装配面：技能文本注入 + Workflow 构造/工厂（阶段模型/预接线）。

package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/skillkit/skill"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// skillText 惰性解析 skillRef（§9.1：含 checksum，防内容漂移难排查），10 分钟周期刷新。
// 改 skill 文件后无需重启，下一个任务自动加载新内容。
// 锁只护缓存字段；SkillProvider I/O 在锁外执行，避免多任务 R3 装配互相阻塞。
func (r *Runner) skillText(_ context.Context) string {
	if r.SkillProvider == nil || len(r.SkillRefs) == 0 {
		return ""
	}
	r.skillMu.Lock()
	if r.skillLoaded && time.Now().Before(r.skillRefresh) {
		cached := r.skillCache
		r.skillMu.Unlock()
		return cached
	}
	r.skillMu.Unlock()

	// 用 Background ctx：skill 是本地文件读取，不需要任务级取消。
	bgCtx := context.Background()
	text := r.loadSkillText(bgCtx)

	r.skillMu.Lock()
	r.skillCache = text
	r.skillLoaded = true
	r.skillRefresh = time.Now().Add(10 * time.Minute)
	r.skillMu.Unlock()
	return text
}

// loadSkillText 实际解析 skill 内容（无锁；结果由 skillText 写回缓存）。
func (r *Runner) loadSkillText(bgCtx context.Context) string {
	var parts []string
	toolMode := r.Cfg.SkillMode == "tool"
	if toolMode {
		// 渐进披露：清单用 agentkit skill.ListPrompt 渲染——加粗主词是规范引用名
		//（use_skill 加载依据），frontmatter 展示名降为括注；模型用展示名回填时
		// AsSkillTool 会别名归一化（v0.7.0 起闭环）
		lister, ok := r.SkillProvider.(skill.Lister)
		if !ok {
			r.Log.Warn("argus.runner.skill_list_failed", "error", "provider 不支持 ListSkills")
			return ""
		}
		metas, err := lister.ListSkills(bgCtx)
		if err != nil {
			r.Log.Warn("argus.runner.skill_list_failed", "error", err.Error())
			return ""
		}
		allowed := map[string]bool{}
		for _, ref := range r.SkillRefs {
			if i := strings.Index(ref, "@"); i >= 0 {
				ref = ref[:i]
			}
			allowed[ref] = true
		}
		var filtered []skill.Meta
		for _, m := range metas {
			if len(allowed) == 0 || allowed[m.Name] {
				filtered = append(filtered, m)
			}
		}
		// 两级预算降级（v0.10.18，对标 ZCode skills 注入预算）：清单超 20K 字节
		// 先降纯名单、仍超再截断注明；预算内渲染与 ListPrompt 完全一致。
		return skill.RenderList(filtered, skill.DefaultListBudget)
	}
	for _, ref := range r.SkillRefs {
		name, ver := ref, ""
		if i := strings.Index(ref, "@"); i >= 0 {
			name, ver = ref[:i], ref[i+1:]
		}
		s, err := r.SkillProvider.Resolve(bgCtx, skill.Ref{Name: name, Version: ver, Source: "file"})
		if err != nil {
			r.Log.Warn("argus.runner.skill_resolve_failed", "ref", ref, "error", err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("--- skill:%s (checksum=%s) ---\n%s", s.Name, s.Checksum, s.Content))
	}
	return strings.Join(parts, "\n")
}

// WorkflowFor 导出每任务 Workflow 构造（ReviewTools 钩子复用 R3 的工具基础设施；
// 构造成本可忽略——字段全部共享引用）。
func (r *Runner) WorkflowFor(budget *llm.Budget) *requirement.Workflow {
	return r.buildWorkflow(budget)
}

// WorkflowFactory 每任务构造 Workflow（供 requirement-compliance 插件与 runner 共用）。
// budget 决定该任务 LLM 客户端的预算累计器；nil = 不限（协议重入路径）。
// 契约类型用 plugin.WorkflowFactory（v3.5.6 上提）——编排层不再 import 具体适配器。
func (r *Runner) WorkflowFactory() plugin.WorkflowFactory {
	return func(budget *llm.Budget) plugin.WorkflowRunner {
		return r.buildWorkflow(budget)
	}
}

// buildWorkflow 每任务构造 Workflow 实例：LLM 客户端持有任务预算累计器，
// 其余字段全部共享引用，构造成本可忽略。
func (r *Runner) buildWorkflow(budget *llm.Budget) *requirement.Workflow {
	// 决策使用模式：use_skill 工具进 R3 工具表（与静态注入互斥，ARGUS_SKILL_MODE）
	var skillTool tool.BaseTool
	if r.Cfg.SkillMode == "tool" && r.SkillProvider != nil && len(r.SkillRefs) > 0 {
		// 显式内容上限（v3.39.0）：与清单 RenderList 预算构成两级纪律
		if st, e := skill.AsSkillTool(r.SkillProvider, r.SkillRefs,
			skill.WithContentCap(r.Cfg.SkillContentCap)); e == nil {
			skillTool = st
		} else {
			r.Log.Warn("argus.runner.skill_tool_failed", "error", e.Error(), "fallback", "静态注入")
		}
	}
	// 优先使用装配期构造的 Generator（*llm.Client 或 *llm.Resilient 降级链）；
	// 通过 BudgetInjector 注入任务级预算（Resilient 副本共享熔断状态，Client 浅拷贝）。
	var gen llm.Generator
	if r.LLM != nil {
		gen = r.LLM
		if budget != nil {
			if bi, ok := gen.(llm.BudgetInjector); ok {
				gen = bi.WithBudget(budget)
			}
		}
		// P6 阶段路由：R1/R2 覆盖链各自注入任务预算后注册（精确/前缀匹配，
		// "R1" 命中 "R1a"）；未覆盖阶段走缺省链
		if len(r.StageLLMs) > 0 {
			sr := llm.NewStageRouter(gen)
			for stage, sg := range r.StageLLMs {
				if bi, ok := sg.(llm.BudgetInjector); ok && budget != nil {
					sg = bi.WithBudget(budget)
				}
				sr.Use(stage, sg)
			}
			gen = sr
		}
	} else {
		c := llm.NewClient(r.Model, r.ModelName, budget)
		// 窗口与输出预留注入 → Generate 发送前输入自守恒（fitInput）
		c.ContextTokens = r.Cfg.LLM.ContextToken
		c.MaxOutputTokens = r.Cfg.MaxTokensResolved()
		// v5.5.2：同 bootstrap——单模型 Client 也吃韧性参数（MaxRetries 含首次，+1 对齐）。
		c.MaxRetries = r.Cfg.LLM.LLMResilienceRetries + 1
		c.BaseDelay = r.Cfg.LLM.LLMResilienceBaseDelay
		c.Limiter = r.LLMLimiter
		// agentkit llm 用 OnUsage 回调替代直接调用 plugin.ReportLLMTokens
		c.OnUsage = func(stage string, prompt, completion int) {
			plugin.ReportLLMTokens(stage, r.ModelName, prompt+completion)
		}
		gen = c
	}
	// R3 ReAct 模型解析：阶段覆盖 > RawModel；配置了备模型则包 FailoverModel
	//（ADK 直调 BaseChatModel，不走 Resilient 链）。⚠️ 这层包**不可删**：阶段覆盖的
	// R3Model 是裸模型（buildStageModel 出 openai.NewChatModel，无降级），靠它补
	// 主备切换；仅当来源是 RawModel（v0.10.9+ 自带 failover）时它才是冗余。
	r3Chat := r.R3Model
	if r3Chat == nil && r.LLM != nil {
		r3Chat = r.LLM.RawModel()
	}
	if r3Chat != nil && r.FallbackModel != nil {
		// v0.10.11：链节结构化（ChainLink{Model,Name}），消平行切片错位
		fm := llm.NewFailoverModel(
			llm.ChainLink{Model: r3Chat, Name: r.ModelName},
			llm.ChainLink{Model: r.FallbackModel, Name: r.FallbackName},
		)
		if r.Log != nil {
			fm.OnFailover = func(from, to, reason string) {
				r.Log.Warn("argus.llm.failover", "from", from, "to", to,
					"stage", "R3(react)", "reason", reason)
			}
		}
		r3Chat = fm
	}
	return &requirement.Workflow{
		LLM:                gen,
		R3ChatModel:        r3Chat,
		SkillTool:          skillTool,
		SDK:                r.SDK,
		Dispatcher:         r.Dispatcher,
		Merger:             r.Merger,
		Tools:              r.ToolPool,
		ToolSpecs:          r.ToolSpecs,
		Knowledge:          r.Knowledge,
		SkillText:          r.skillText,
		Log:                r.Log,
		ReqBodyMaxChars:    r.Cfg.ReqBodyMaxChars(),
		ReqIssueMaxChars:   r.Cfg.ReqIssueMaxChars(),
		GetFileMaxChars:    r.Cfg.GetFileMaxRunes(),
		GetIssueMaxChars:   r.Cfg.GetIssueMaxRunes(),
		GetDiffMaxChars:    r.Cfg.GetDiffMaxRunes(),
		ReflectRounds:      r.ReflectRounds,
		ReflectModel:       r.ReflectModel,
		ReflectEvidence:    r.ReflectEvidence,
		CompletenessPolicy: r.CompletenessPolicy,
		CompletenessRetry:  r.CompletenessRetry,
	}
}
