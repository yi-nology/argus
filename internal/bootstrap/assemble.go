// assemble.go —— 进程装配的唯一归属（v3.5.8，自 main.go runApp 收敛）：
// SDK/工作副本/技能 → 模型栈（BuildModels）→ 知识子系统 → 总线/推送 →
// 插件注册（含 runner 延迟解引用环）→ MCP 工具表 → 成本记账 →
// dispatcher/merger/poster/runner。
// main 只保留生命周期语义：存储开关、资源注册、选主、worker 排水、HTTP。
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/notify"
	"argus/internal/plugin"
	"argus/internal/poster"
	"argus/internal/progress"
	"argus/internal/runner"
	"argus/internal/service"
	"argus/internal/sink"
	"argus/internal/storage"
	"argus/internal/toolpool"
	"git.example.com/enjoydream/agentkit/audit"
	"git.example.com/enjoydream/agentkit/knowledge/rag"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/skill"
	"git.example.com/enjoydream/agentkit/workcopy"
)

// System 装配产物：全部子系统就绪后的共享引用集合。
// 生命周期（Start/Stop 顺序）仍由 main 经 ekit bootstrap 管理，本结构不做任何启停。
type System struct {
	Models    *Models
	SDK       *argusctx.SDKCtx
	WC        *workcopy.Pool
	Skills    skill.Provider
	Knowledge rag.KnowledgeService
	Bus       *progress.Bus
	Notify    *notify.Notifier // nil = 未配置 octopus

	Registry            *plugin.Registry
	ComplianceSkillRefs []string
	ToolPool            toolpool.Pool
	ToolSpecs           []toolpool.ToolSpec
	Dispatcher          *plugin.Dispatcher
	Merger              *plugin.Merger
	Poster              *poster.Poster
	Runner              *runner.Runner
	Service             *service.Service
}

// Assemble 装配除存储/HTTP/生命周期外的全部子系统。
// store 由调用方打开后传入（merger/runner 共享）；version 供 poster/runner 留痕；
// instanceID 供 workcopy 按实例隔离工作副本目录。
func Assemble(ctx context.Context, cfg *config.Config, log logx.Logger, store *storage.Store,
	version, instanceID string) (*System, error) {
	sys := &System{}

	// ---- L5 上下文：SDK 封装 ----
	sdk, err := argusctx.NewSDKCtx(cfg, log)
	if err != nil {
		return nil, err
	}
	sys.SDK = sdk

	// workcopy 按实例隔离：共享 WorkRoot 时，本实例 Sweep 的孤儿扫描会把
	// 他实例"已跟踪"的目录误判为孤儿删除（entries 是实例内内存态）
	wcRoot := filepath.Join(cfg.WorkRoot, "instance-"+instanceID)
	// fail-fast（v3.8.0）：目录建不出来时每个任务执行期才失败、/readyz 却照常
	// 绿灯——启动期能判定的失败不该拖到每任务撞一次。
	if err := os.MkdirAll(wcRoot, 0o755); err != nil {
		return nil, fmt.Errorf("工作目录创建失败 %s: %w", wcRoot, err)
	}
	wc := workcopy.NewPool(wcRoot, log)
	wc.CredentialOf = func(platform string) (string, string, bool) {
		pc, ok := cfg.Platforms[platform]
		// GitHub App 认证时，优先用 installation token（自动刷新）
		if platform == "github" && sdk.GitHubAppToken() != "" {
			return pc.BaseURL, sdk.GitHubAppToken(), ok
		}
		return pc.BaseURL, pc.Token, ok
	}
	wc.InsecureTLSOf = func(platform string) bool {
		return cfg.Platforms[platform].SkipTLS
	}
	sys.WC = wc
	sys.Skills = skill.NewFileProvider(cfg.SkillsDir)

	// ---- 模型栈（主模型 + 全局降级 + P6 阶段覆盖，见 models.go）----
	models, err := BuildModels(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	sys.Models = models

	// ---- L5 知识子系统：Milvus 向量检索 > 本地 RAG > Noop ----
	sys.Knowledge = buildKnowledge(ctx, cfg, log)

	// ---- L1 扩展：进度事件总线 + octopus IM 推送（v3.1）----
	sys.Bus = progress.NewBus()
	if cfg.OctopusURL != "" {
		sys.Notify = &notify.Notifier{
			BaseURL: cfg.OctopusURL, ChannelID: cfg.OctopusChannelID,
			User: cfg.OctopusUser, Token: cfg.OctopusToken, Log: log,
		}
		log.Info("argus.boot.notify_ready", "octopus", cfg.OctopusURL, "channel", cfg.OctopusChannelID)
	}

	// ---- L4 插拔层 + L3 编排 + runner ----
	// 插件清单一次加载：registry 与 MCP 工具表/编队段共用（v3.5.9）。
	// 解析失败 fail-fast（深挖修复）：坏 YAML 不能静默降级为内置默认插件集。
	pf, err := loadPluginsFile(cfg, log)
	if err != nil {
		return nil, err
	}
	// runnerRef 延迟填充：requirement-compliance 插件的 factory 在 Invoke 时才解引用。
	var runnerRef *runner.Runner
	reg, complianceSkillRefs, err := BuildRegistry(&Deps{
		Cfg: cfg, Log: log, WC: wc, Model: models.React, Skills: sys.Skills, Plugins: pf,
		WorkflowFactory: func(budget *plugin.TokenBudget) plugin.WorkflowRunner {
			if runnerRef == nil {
				return nil // 装配完成前不该被调用；调用方 panic=明确报错
			}
			return runnerRef.WorkflowFactory()(budget)
		},
		// R4 builtin ReAct 化：Reviewer 复用 R3 工具基础设施（get_file/get_diff/
		// 知识/技能/MCP），失败自动落回单轮调用
		ReviewTools: func(ctx context.Context, task plugin.Task, budget *plugin.TokenBudget) ([]tool.BaseTool, string, error) {
			if runnerRef == nil {
				return nil, "", fmt.Errorf("runner 未就绪")
			}
			wf := runnerRef.WorkflowFor(budget)
			// 仓库级评审规范（v3.38.0）：R4 与 R3 同源装载
			wf.RepoGuide = runnerRef.RepoGuideFor(ctx, task.PR.Platform, task.PR.Owner, task.PR.Repo, task.PR.HeadSHA)
			return wf.ReviewTools(ctx, task)
		},
		ReviewerLLM:  models.ReviewerGen,
		ReviewerName: models.ReviewerName,
		ReactModel:   models.ReviewerReact,
	})
	if err != nil {
		return nil, err
	}
	sys.Registry = reg
	sys.ComplianceSkillRefs = complianceSkillRefs

	// ---- L5 工具子系统：MCP server → eino 工具表（design §9.2 方向 A 实装）----
	// plugins.yaml 的 mcpServers/tools 段驱动；未声明时 nil（workflow 不挂工具）。
	var toolPool toolpool.Pool
	var toolSpecs []toolpool.ToolSpec
	var squads []plugin.SquadDecl
	if len(pf.MCPServers) > 0 {
		if rp := toolpool.NewRealPool(pf.MCPServers, log); rp != nil {
			// 闲置连接回收（ARGUS_MCP_IDLE_REAP_MAX_IDLE > 0 启用）：长驻池的
			// 闲置 stdio/HTTP 连接周期性摘除，重连在下次取工具时按需发生。
			if cfg.MCPIdleReapMaxIdle > 0 {
				rp.StartIdleReaper(cfg.MCPIdleReapInterval, cfg.MCPIdleReapMaxIdle)
				log.Info("argus.boot.mcp_idle_reaper", "interval", cfg.MCPIdleReapInterval.String(),
					"max_idle", cfg.MCPIdleReapMaxIdle.String())
			} else if cfg.MCPIdleReapInterval != 5*time.Minute {
				// 配置陷阱如实化（v3.41.0）：单配 INTERVAL 不启用回收
				log.Warn("argus.boot.mcp_idle_reaper_ignored", "interval_configured", cfg.MCPIdleReapInterval.String(),
					"hint", "仅配置 ARGUS_MCP_IDLE_REAP_INTERVAL 不启用回收；需 ARGUS_MCP_IDLE_REAP_MAX_IDLE>0")
			}
			toolPool = &toolpool.RealAdapter{Pool: rp}
			toolSpecs = toolpool.SpecsFromDecls(pf.Tools)
			log.Info("argus.boot.mcp_ready", "servers", strings.Join(rp.Servers(), ","),
				"tool_specs", len(toolSpecs))
		}
	}
	squads = pf.Squads
	sys.ToolPool, sys.ToolSpecs = toolPool, toolSpecs

	// ---- LLM 成本记账（obsx OnUsage 回流，覆盖直连与 ReAct RawModel 两条路径）----
	costTracker := llm.NewCostTracker()
	costRates := cfg.CostPer1K()
	// v3.36.0：未知模型（阶段覆盖）按主费率近似记账——此前恒按 0，成本行系统性
	// 低估（v3.8.0 告警在案解除）；同厂兼容端点近似同价，误差远小于 0。
	costPer1K := func(mdl string) [2]float64 {
		return costRates
	}

	// ---- 裁决/合并/回帖/执行编排 ----
	dispatcher := plugin.NewDispatcher(reg, log).WithSquads(squads)
	dispatcher.MaxConcurrent = cfg.PluginMaxConcurrent
	merger := plugin.NewMerger(store, log)
	if cfg.RegretLearning {
		merger.WithLearning(store, cfg.LearningConfig())
		log.Info("argus.regret_learning.enabled",
			"min_samples", cfg.RegretMinSamples,
			"soft_floor", cfg.RegretSoftFloor,
			"hard_floor", cfg.RegretHardFloor)
	}
	sys.Dispatcher = dispatcher
	sys.Merger = merger
	sys.Poster = &poster.Poster{SDK: sdk, Store: store, Version: version, BotUsername: cfg.BotUsername, Log: log}
	auditLog := audit.New(log, "argus-audit") // v3.86.0：runner（verdict.posted/fix.push）与 service 共用同一审计器
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdk,
		Model: models.React, ModelName: cfg.LLM.Model,
		LLM:        models.Generate,
		ToolPool:   toolPool,
		ToolSpecs:  toolSpecs,
		Dispatcher: dispatcher, Merger: merger,
		Poster: sys.Poster, Log: log, Version: version,
		Audit:         auditLog,
		SkillProvider: sys.Skills, SkillRefs: complianceSkillRefs,
		WC:        sys.WC, // OCR delegate 委托前置（ARGUS_OCR_BIN 启用）
		Bus:       sys.Bus,
		Notify:    sys.Notify,
		Knowledge: sys.Knowledge,
		// Limiter 仅作用于非降级路径的阶段覆盖 Client（历史行为，原样保留）
		LLMLimiter: models.Limiter,
		Cost:       costTracker,
		CostPer1K:  costPer1K,
		StageLLMs:  models.StageLLMs,
		R3Model:    models.R3,
		// ReAct failover：R3Model 已包时用它；nil 时 runner 用 RawModel + FallbackModel 再包
		FallbackModel: models.Fallback,
		FallbackName:  models.FallbackName,
		// R3.5 报告反思（v3.4 reflection）：轮数 0 = 关闭；模型缺省回退 R3 专用模型
		ReflectRounds:   cfg.ReflectRounds,
		ReflectModel:    models.Reflect,
		ReflectEvidence: cfg.ReflectEvidence,
		// G4 完备性裁判（v3.11.0）：annotate 默认只注明；demote 降级门；retry 受限补审
		CompletenessPolicy: cfg.CompletenessPolicy,
		CompletenessRetry:  cfg.CompletenessRetry,
		// 模式级后悔学习环（v3.5）：默认关闭；阈值单源自 cfg.LearningConfig()
		Learning: cfg.LearningConfig(),
	}
	// 延迟绑定不变量自检：WorkflowFactory/ReviewTools 闭包捕获 runnerRef 指针，
	// 依赖「Invoke 时装配已完成」。此处显式钉死——未来若出现条件构造路径，
	// 漏构造在此 fail-fast，而不是运行期静默拿到 nil factory。
	if runnerRef == nil {
		return nil, fmt.Errorf("bootstrap: runner 未装配（延迟绑定闭包将拿到 nil factory）")
	}
	// 结果上报 sink（ARGUS_SINK_URL 非空启用）：审查完成后 push 给 review-service
	// 承接端点（旁路，失败只告警不阻断审查主流程）。
	if cfg.SinkURL != "" {
		log.Info("argus.sink.enabled", "endpoint", cfg.SinkURL)
		runnerRef.Sink = sink.NewReporter(cfg.SinkURL, cfg.SinkToken, log)
	}
	sys.Runner = runnerRef

	// ---- service 门面（v3.48.0 自 main 收敛）：webhook/CRUD/Q&A/MCP 数据面 ----
	// System 由此成为完整对象图；main 只剩 HTTP 接线（SetService/Bearer/路由/停机排水）。
	sys.Service = &service.Service{
		Cfg: cfg, Store: store, SDK: sdk, Registry: reg,
		Knowledge: sys.Knowledge, Log: log, Bus: sys.Bus,
		Model: models.React, QAModel: models.QA,
		LLM:         models.Generate, // diff 预审单轮写作（v3.123.0）
		RouterModel: models.Router,
		WC:          sys.WC, // testgen 沙箱验证（ARGUS_TESTGEN_VERIFY_CMD 启用；nil 安全）
		Audit:       auditLog, Version: version,
	}
	return sys, nil
}

// buildKnowledge 知识检索装配（v3.119.0 Hybrid 双路优先，agentkit v0.14.0）：
//   - Milvus + 本地知识目录双备 → NewHybrid（语义+TF-IDF RRF 融合，检索单路
//     失败自动降级另一路——此前 Milvus 查询故障即检索全挂）；
//   - 仅 Milvus → Milvus（构建失败降级本地）；仅本地 → Local；全无 → Noop。
func buildKnowledge(ctx context.Context, cfg *config.Config, log logx.Logger) rag.KnowledgeService {
	local, localOK := buildLocalRAG(cfg.KnowledgeDir, log)
	var milvus rag.KnowledgeService
	if cfg.MilvusAddr != "" && cfg.EmbeddingBaseURL != "" {
		embedder := rag.NewOpenAIEmbedder(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim)
		milvusCfg := rag.MilvusConfig{
			Address:    cfg.MilvusAddr,
			Collection: cfg.MilvusCollection,
			Dimension:  cfg.EmbeddingDim,
		}
		if k, e := rag.NewMilvusStore(ctx, milvusCfg, embedder); e == nil {
			milvus = k
			log.Info("argus.boot.milvus_ready", "addr", cfg.MilvusAddr, "collection", cfg.MilvusCollection,
				"embedding", cfg.EmbeddingModel, "dim", cfg.EmbeddingDim)
			// 启动时索引 knowledge 目录到 Milvus（语义路语料）
			if fi, err := os.Stat(cfg.KnowledgeDir); err == nil && fi.IsDir() {
				indexKnowledgeDir(ctx, k, cfg.KnowledgeDir, log)
			}
		} else {
			log.Warn("argus.boot.milvus_failed", "addr", cfg.MilvusAddr, "error", e.Error(),
				"fallback", "本地 RAG")
		}
	}
	switch {
	case milvus != nil && localOK:
		log.Info("argus.boot.knowledge_hybrid", "semantic", "milvus", "keyword", cfg.KnowledgeDir)
		return rag.NewHybrid(milvus, local)
	case milvus != nil:
		return milvus
	case localOK:
		return local
	default:
		return rag.NewNoop()
	}
}

// buildLocalRAG 本地 RAG（目录缺失/构建失败返回 nil 并日志留痕）。
func buildLocalRAG(dir string, log logx.Logger) (*rag.Local, bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		log.Warn("argus.boot.knowledge_absent", "dir", dir,
			"hint", "创建目录并放入 markdown 知识文件即启用 search_knowledge")
		return nil, false
	}
	k, e := rag.NewLocal(dir)
	if e != nil {
		log.Warn("argus.boot.knowledge_failed", "dir", dir, "error", e.Error())
		return nil, false
	}
	log.Info("argus.boot.knowledge_ready", "dir", dir)
	return k, true
}

// indexKnowledgeDir 将知识库目录中的 markdown 文件索引到 Milvus。
func indexKnowledgeDir(ctx context.Context, store *rag.MilvusStore, dir string, log logx.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("argus.boot.index_dir_failed", "dir", dir, "error", err.Error())
		return
	}
	indexed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn("argus.boot.index_file_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		if err := store.Index(ctx, entry.Name(), string(data)); err != nil {
			log.Warn("argus.boot.index_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		indexed++
	}
	log.Info("argus.boot.knowledge_indexed", "dir", dir, "files", indexed)
}
