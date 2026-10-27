// Package arch 可执行架构守卫（v3.62.0）：把「高内聚低耦合」的分层纪律从口头
// 约定变成 go test 门禁。规则 = 当前实现的分层事实快照（2026-09-30），全部当前
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
		`{{.ImportPath}}|{{join .Imports "|"}}`, "argus/internal/...").Output()
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
// 依赖集合（封闭）。不在表内的包不受 import 方向约束（bootstrap 组合根、runner/
// service 编排入口的既有依赖面较宽，收窄另行批次推进）。
// 规则变更 = 架构变更：必须同步 docs/architecture-deep-dive.md 并在 CHANGELOG 记档。
var layering = map[string][]string{
	// ---- L0 纯底座：零 internal 依赖 ----
	"argus/internal/codesearch": {},
	"argus/internal/semsearch":  {},
	"argus/internal/learning":   {},
	"argus/internal/progress":   {},
	"argus/internal/gitdeepen":  {},
	// v3.183.0：影响面管线（外部依赖仅 textx/procx，包装接线在 runner）
	"argus/internal/impactview": {},
	// v3.187.0：服务侧按仓配置（类型+Parse 校验纯函数，消费方 storage/runner/service）
	"argus/internal/reposettings": {},

	// ---- 域类型层：只依赖更纯的域类型 ----
	"argus/internal/domain": {"argus/internal/learning"},

	// ---- 引擎面（v3.173.0 自 plugin 拆出）：契约/观测经别名桥引用 plugin，方向单一 ----
	"argus/internal/dispatch": {"argus/internal/agent/agentrun", "argus/internal/agent/blackboard", "argus/internal/learning", "argus/internal/domain", "argus/internal/llm", "argus/internal/plugin", "argus/internal/storage"},

	// ---- L4 核心域与横切底座 ----
	"argus/internal/plugin": {"argus/internal/domain", "argus/internal/learning"},
	"argus/internal/config": {"argus/internal/learning"},
	// obs 的 plugin 依赖是钩子接线（HookLLMTokens 等 → plugin.Hook*，装配期注入实现）
	"argus/internal/obs":    {"argus/internal/plugin"},
	"argus/internal/notify": {"argus/internal/notify/octopus", "argus/internal/obs"},

	// ---- 自有协议客户端子包：零 internal 依赖（v3.148.0 octopus 回归本仓）----
	"argus/internal/notify/octopus": {},

	// ---- 能力层：可依赖核心域与基座，禁编排/入口 ----
	"argus/internal/reportview": {"argus/internal/dispatch", "argus/internal/plugin"},
	"argus/internal/toolpool":   {"argus/internal/plugin"},
	"argus/internal/sastscan":   {"argus/internal/dispatch", "argus/internal/plugin"},
	"argus/internal/sink":       {"argus/internal/plugin"},
	"argus/internal/storage":    {"argus/internal/domain", "argus/internal/learning"},
	"argus/internal/worker":     {"argus/internal/storage"},
	"argus/internal/health":     {"argus/internal/learning"},
	"argus/internal/context":    {"argus/internal/config", "argus/internal/obs", "argus/internal/plugin"},
	"argus/internal/qa":         {"argus/internal/codesearch", "argus/internal/context", "argus/internal/plugin", "argus/internal/semsearch", "argus/internal/storage"},
	"argus/internal/dream":      {"argus/internal/learning", "argus/internal/memorykernel", "argus/internal/plugin", "argus/internal/storage"},
	"argus/internal/poster":       {"argus/internal/context", "argus/internal/plugin", "argus/internal/dispatch", "argus/internal/reportview", "argus/internal/storage"},
	"argus/internal/poller":       {"argus/internal/context", "argus/internal/obs", "argus/internal/plugin", "argus/internal/progress", "argus/internal/storage"},
	"argus/internal/ocrdeleg":     {"argus/internal/gitdeepen", "argus/internal/plugin"},
	"argus/internal/ocrshim":      {"argus/internal/gitdeepen", "argus/internal/plugin", "argus/internal/plugin/adapter_cli"},

	// ---- 编排层：依赖能力层，禁跨编排互依 ----
	"argus/internal/workflow/requirement": {
		"argus/internal/codesearch", "argus/internal/context",
		"argus/internal/dispatch",
		"argus/internal/obs", "argus/internal/plugin", "argus/internal/toolpool",
	},
	"argus/internal/runner": {
		"argus/internal/config", "argus/internal/context",
		"argus/internal/dispatch",
		"argus/internal/fixverifier", "argus/internal/impactview", "argus/internal/learning",
		"argus/internal/memorykernel",
		"argus/internal/notify", "argus/internal/ocrdeleg", "argus/internal/plugin",
		"argus/internal/poster", "argus/internal/progress", "argus/internal/reposettings", "argus/internal/reportview",
		"argus/internal/sastscan", "argus/internal/sink", "argus/internal/storage",
		"argus/internal/toolpool", "argus/internal/workflow/requirement", "argus/internal/writing",
	},
	"argus/internal/service": {
		"argus/internal/config", "argus/internal/context", "argus/internal/health",
		"argus/internal/dispatch",
		"argus/internal/impactview",
		"argus/internal/memorykernel", "argus/internal/obs",
		"argus/internal/plugin", "argus/internal/progress", "argus/internal/qa",
		"argus/internal/reposettings", "argus/internal/semsearch", "argus/internal/storage",
		// v3.123.0：diff 预审 prompt 单源下沉 writing.Service，service.ReviewDiff
		// 委派（与 runner→writing 同向；同 qa 消费先例）。
		"argus/internal/writing",
	},

	// ---- 入口适配层：channel 靠消费侧窄接口（Core/Copilot/ReviewQA）实现
	// 零 internal 依赖——service 由 main 装配注入，mcpserver 永不见 runner/service ----
	"argus/internal/channel/mcpserver": {},

	// ---- 适配器子包：单向依赖核心域（plugin 不得反向 import 适配器）----
	"argus/internal/plugin/adapter_acpx":    {"argus/internal/dispatch", "argus/internal/plugin"},
	"argus/internal/plugin/adapter_builtin": {"argus/internal/dispatch", "argus/internal/plugin"},
	"argus/internal/plugin/adapter_cli":     {"argus/internal/plugin"},
	"argus/internal/plugin/adapter_remote":  {"argus/internal/plugin"},
}

// TestLayering 分层白名单守卫。
func TestLayering(t *testing.T) {
	for pkg, imports := range deps {
		allowed, constrained := layering[pkg]
		if !constrained {
			continue
		}
		for imp := range imports {
			if !strings.HasPrefix(imp, "argus/internal/") {
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
		if pkg == "argus/internal/bootstrap" {
			continue
		}
		if imports["argus/internal/bootstrap"] {
			t.Errorf("%s → argus/internal/bootstrap：组合根被反向依赖（装配职责泄漏）", pkg)
		}
	}
}

// docAllowlist 包文档检查的暂缓名单（原因必填）。
//   - domain：并行会话迁移刚建包，doc 待其提交后补（v3.62.0 时在途，勿代写）。
var docAllowlist = map[string]string{
	"argus/internal/domain": "迁移在途，收尾批次补 doc 后移除本条",
}

// TestPackageDocs 包文档完整性：包级 doc comment 是架构可读性的第一道门——
// 职责、层级、边界写在 go doc 第一屏。
func TestPackageDocs(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{.Doc}}`,
		"argus/internal/...").Output()
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
