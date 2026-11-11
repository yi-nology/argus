// Package arch 可执行架构守卫（v3.62.0）：把「高内聚低耦合」的分层纪律从口头
// 约定变成 go test 门禁。规则 = 当前实现的分层事实快照（2026-09-30 建表，
// 2026-11-11 架构卫生批 B 刷新：销 3 个幽灵条目 + 4 个死条目，补 4 个零约束包），全部当前
// 满足；测试的价值在于**任何依赖方向变更都必须先改本文件**——即架构变更在
// diff 里显式可见、可评审、可回滚。
//
// 三类检查：
//  1. TestLayering        分层白名单：受约束包的 internal 依赖必须是封闭允许集
//     （新增依赖 → 测试失败 → 显式扩集 → CHANGELOG 记档）；
//  2. TestBootstrapNotImported 组合根单向性：bootstrap 只许 import、不许被 import；
//  3. TestPackageDocs     包文档完整性：每个 internal 包必须有包级 doc comment
//     （包的职责与层级写在第一眼能看到的地方）。
//
// 规则与 docs/architecture-deep-dive.md / design.md 的分工：design 是设计契约
// （§ 引用），deep-dive 是历史演进分析，本测试是**当前分层事实的机器可读快照**
// ——三者不一致时，以能跑的为准。
package arch

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// deps 包 → 直接 import 集合（TestMain 一次采集）。
var deps map[string]map[string]bool

func TestMain(m *testing.M) {
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports "|"}}`, "github.com/yi-nology/argus/internal/...").Output()
	if err != nil {
		// 编译失败等场景：让 go test 自身的构建错误先行暴露，这里直接失败
		panic("go list 失败: " + err.Error() + ": " + string(out))
	}
	deps = map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		imports := map[string]bool{}
		if parts[1] != "" {
			for _, imp := range strings.Split(parts[1], "|") {
				imports[imp] = true
			}
		}
		deps[parts[0]] = imports
	}
	os.Exit(m.Run())
}

// layering 分层白名单。key = 受约束包（精确路径）；value = 允许的 argus/internal/*
// 依赖集合（封闭）。不在表内的包不受 import 方向约束（现仅剩 bootstrap 组合根
// ——装配一切、单向性由 TestBootstrapNotImported 独立守卫；42 个 internal 包
// 其余 41 个全部在表内）。
// 规则变更 = 架构变更：必须同步 docs/architecture-deep-dive.md 并在 CHANGELOG 记档。
var layering = map[string][]string{
	// ---- L0 纯底座：零 internal 依赖 ----
	"github.com/yi-nology/argus/internal/codesearch": {},
	"github.com/yi-nology/argus/internal/semsearch":  {},
	"github.com/yi-nology/argus/internal/learning":   {},
	"github.com/yi-nology/argus/internal/progress":   {},
	"github.com/yi-nology/argus/internal/gitdeepen":  {},
	// v5.10.0：go/types 精确调用边抽取器（semsearch.EdgeAugmenter 档二实现，
	// bootstrap 经 ARGUS_GRAPH_PRECISE opt-in 注入）——唯一 internal 依赖是
	// semsearch 取 NodeKey 键契约单源；bootstrap→goedges 为组合根单向装配
	// （bootstrap 不受本表约束），不另立白名单条目。
	"github.com/yi-nology/argus/internal/goedges": {"github.com/yi-nology/argus/internal/semsearch"},
	// v3.183.0：影响面管线（外部依赖仅 textx/procx，包装接线在 runner）
	// v5.4.7 funcdiff 浅克隆先 deepen（补录：d0db25c 引入消费未同步白名单，门禁已在 v6.3.0 补录转绿）
	// v5.8.0 findings 挂图：渲染层消费 plugin.Finding 契约（findingswire 同向）
	// v5.8.2 惯犯热力：ViewOpts.History 用 storage.FindingFileHistory 聚合结果类型
	// v6.0.0 L2 调用方升级：消歧符号图优先解析调用方（semsearch.NodeKey 键契约
	// + SymbolContext 邻接；纯底座消费，方向与 goedges 同款）
	"github.com/yi-nology/argus/internal/impactview": {"github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage"},
	// v3.187.0：服务侧按仓配置（类型+Parse 校验纯函数，消费方 storage/runner/service）
	"github.com/yi-nology/argus/internal/reposettings": {},

	// ---- 域类型层：只依赖更纯的域类型 ----
	"github.com/yi-nology/argus/internal/domain": {"github.com/yi-nology/argus/internal/learning"},

	// ---- 引擎面（v3.173.0 自 plugin 拆出）：契约/观测经别名桥引用 plugin，方向单一 ----
	// v6.2.x 架构卫生批 B：销幽灵边 agentrun/blackboard/llm（三包早已不存在，
	// 职责由 dispatch 自身 + 外部 oagentkit/agent/blackboard、oagentkit/llm 承接）
	// 与死边 storage（未 import）——封闭允许集语义恢复「只查实到边」。
	"github.com/yi-nology/argus/internal/dispatch": {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/plugin"},

	// ---- L4 核心域与横切底座 ----
	// v6.2.x 架构卫生批 B：销死边 learning（plugin 实际仅消费 domain）。
	"github.com/yi-nology/argus/internal/plugin": {"github.com/yi-nology/argus/internal/domain"},
	"github.com/yi-nology/argus/internal/config": {"github.com/yi-nology/argus/internal/learning"},
	// obs 的 plugin 依赖是钩子接线（HookLLMTokens 等 → plugin.Hook*，装配期注入实现）
	"github.com/yi-nology/argus/internal/obs":    {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/notify": {"github.com/yi-nology/argus/internal/notify/octopus", "github.com/yi-nology/argus/internal/obs"},

	// ---- 自有协议客户端子包：零 internal 依赖（v3.148.0 octopus 回归本仓）----
	"github.com/yi-nology/argus/internal/notify/octopus": {},

	// ---- 能力层：可依赖核心域与基座，禁编排/入口 ----
	"github.com/yi-nology/argus/internal/reportview": {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/toolpool":   {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/sastscan":   {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/sink":       {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/storage":    {"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/learning"},
	"github.com/yi-nology/argus/internal/worker":     {"github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/health":     {"github.com/yi-nology/argus/internal/learning"},
	// v6.2.x 架构卫生批 B：销死边 obs（argusctx 实际仅消费 config/plugin）。
	"github.com/yi-nology/argus/internal/argusctx": {"github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/qa":       {"github.com/yi-nology/argus/internal/codesearch", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/dream":    {"github.com/yi-nology/argus/internal/learning", "github.com/yi-nology/argus/internal/memorykernel", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/poster":   {"github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/reportview", "github.com/yi-nology/argus/internal/storage"},
	// v6.2.x 架构卫生批 B：销死边 plugin（poller 实际未 import plugin）。
	"github.com/yi-nology/argus/internal/poller":   {"github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/obs", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/storage"},
	"github.com/yi-nology/argus/internal/ocrdeleg": {"github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/ocrshim":  {"github.com/yi-nology/argus/internal/gitdeepen", "github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/plugin/adapter_cli"},

	// ---- v6.2.x 架构卫生批 B 补边（原 layering 盲区 4 包，此前纯靠自觉）----
	// 允许集 = 现状 import 快照（2026-11-11 go list 实测）：四包均有稳定消费方
	// （service→commentcmd、runner→fixverifier/writing、dream/service→memorykernel），
	// 当前依赖面恰好干净——立此约束后新增 internal 依赖须显式扩集并记档。
	"github.com/yi-nology/argus/internal/commentcmd":   {},
	"github.com/yi-nology/argus/internal/fixverifier":  {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/memorykernel": {},
	"github.com/yi-nology/argus/internal/writing":      {},

	// ---- 编排层：依赖能力层，禁跨编排互依 ----
	"github.com/yi-nology/argus/internal/workflow/requirement": {
		"github.com/yi-nology/argus/internal/codesearch", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/domain",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/obs", "github.com/yi-nology/argus/internal/plugin",
		// v5.11.0：图面进审查管线——Workflow.Graph 字段与 get_trace/
		// get_symbol_context 工具渲染消费 semsearch（SymRef/Index.Precise）。
		"github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/toolpool",
	},
	"github.com/yi-nology/argus/internal/runner": {
		"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/argusctx",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/fixverifier", "github.com/yi-nology/argus/internal/impactview", "github.com/yi-nology/argus/internal/learning",
		"github.com/yi-nology/argus/internal/memorykernel",
		"github.com/yi-nology/argus/internal/notify", "github.com/yi-nology/argus/internal/ocrdeleg", "github.com/yi-nology/argus/internal/plugin",
		"github.com/yi-nology/argus/internal/poster", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/reposettings", "github.com/yi-nology/argus/internal/reportview",
		// v5.11.0：injectCodeGraph 同工作副本再建消歧符号图（semsearch 纯图模式）。
		"github.com/yi-nology/argus/internal/sastscan", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/sink", "github.com/yi-nology/argus/internal/storage",
		"github.com/yi-nology/argus/internal/toolpool", "github.com/yi-nology/argus/internal/workflow/requirement", "github.com/yi-nology/argus/internal/writing",
	},
	"github.com/yi-nology/argus/internal/service": {
		// v5.4.4：评论指令词法层剥出（目录/解析/词表纯函数单源，执行体仍在本包）。
		"github.com/yi-nology/argus/internal/commentcmd",
		"github.com/yi-nology/argus/internal/domain", "github.com/yi-nology/argus/internal/config", "github.com/yi-nology/argus/internal/argusctx", "github.com/yi-nology/argus/internal/health",
		"github.com/yi-nology/argus/internal/dispatch",
		"github.com/yi-nology/argus/internal/impactview",
		"github.com/yi-nology/argus/internal/memorykernel", "github.com/yi-nology/argus/internal/obs",
		"github.com/yi-nology/argus/internal/plugin", "github.com/yi-nology/argus/internal/progress", "github.com/yi-nology/argus/internal/qa",
		"github.com/yi-nology/argus/internal/reposettings", "github.com/yi-nology/argus/internal/semsearch", "github.com/yi-nology/argus/internal/storage",
		// v3.123.0：diff 预审 prompt 单源下沉 writing.Service，service.ReviewDiff
		// 委派（与 runner→writing 同向；同 qa 消费先例）。
		"github.com/yi-nology/argus/internal/writing",
	},

	// ---- 入口适配层：channel 靠消费侧窄接口（Core/Copilot/ReviewQA）实现
	// 零 internal 依赖——service 由 main 装配注入，mcpserver 永不见 runner/service ----
	"github.com/yi-nology/argus/internal/channel/mcpserver": {},

	// ---- 适配器子包：单向依赖核心域（plugin 不得反向 import 适配器）----
	"github.com/yi-nology/argus/internal/plugin/adapter_acpx":    {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/plugin/adapter_builtin": {"github.com/yi-nology/argus/internal/dispatch", "github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/plugin/adapter_cli":     {"github.com/yi-nology/argus/internal/plugin"},
	"github.com/yi-nology/argus/internal/plugin/adapter_remote":  {"github.com/yi-nology/argus/internal/plugin"},
}

// TestLayering 分层白名单守卫。
func TestLayering(t *testing.T) {
	for pkg, imports := range deps {
		allowed, constrained := layering[pkg]
		if !constrained {
			// 未归类包必须显式归类（v6.3.0 评估 P2 修复）：此前新包靠
			// "不在白名单"直接跳过约束——盲区。例外仅 bootstrap：组合根
			// 由 TestBootstrapNotImported 单独守卫（装配根允许触达一切，
			// 分层允许集对它无意义）。
			if pkg == "github.com/yi-nology/argus/internal/bootstrap" {
				continue
			}
			t.Errorf("%s 未在 layering 白名单归类：新包必须显式声明分层位置"+
				"（允许集可为空 {}，如自有协议子包）", pkg)
			continue
		}
		for imp := range imports {
			if !strings.HasPrefix(imp, "github.com/yi-nology/argus/internal/") {
				continue
			}
			ok := false
			for _, a := range allowed {
				if a == imp {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s → %s：白名单外依赖。\n"+
					"这是架构变更：请确认方向正确后更新 test/arch 的 layering 白名单，"+
					"并同步 docs/architecture-deep-dive.md 与 CHANGELOG 记档。", pkg, imp)
			}
		}
	}
}

// TestBootstrapNotImported 组合根单向性：bootstrap 装配一切但不被任何 internal
// 包 import（main 除外——main 不在 internal 集合内，天然通过）。组合根被反向
// 依赖意味着装配职责泄漏，装配图不再单点可见。
func TestBootstrapNotImported(t *testing.T) {
	for pkg, imports := range deps {
		if pkg == "github.com/yi-nology/argus/internal/bootstrap" {
			continue
		}
		if imports["github.com/yi-nology/argus/internal/bootstrap"] {
			t.Errorf("%s → argus/internal/bootstrap：组合根被反向依赖（装配职责泄漏）", pkg)
		}
	}
}

// docAllowlist 包文档检查的暂缓名单（原因必填）。当前为空——v3.62.0 遗留的
// domain「迁移在途」条目已死（domain 现有包文档），v6.2.x 架构卫生批 B 销账。
var docAllowlist = map[string]string{}

// TestPackageDocs 包文档完整性：包级 doc comment 是架构可读性的第一道门——
// 职责、层级、边界写在 go doc 第一屏。
func TestPackageDocs(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{.Doc}}`,
		"github.com/yi-nology/argus/internal/...").Output()
	if err != nil {
		t.Fatalf("go list 失败: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, doc := parts[0], parts[1]
		if doc == "" {
			if reason, ok := docAllowlist[pkg]; ok {
				t.Logf("包 %s 缺 doc（暂缓：%s）", pkg, reason)
				continue
			}
			t.Errorf("包 %s 缺包级 doc comment：请在包内任一文件 package 子句上方补「// Package <name> 职责一句话（层级/边界要点）」", pkg)
		}
	}
}
