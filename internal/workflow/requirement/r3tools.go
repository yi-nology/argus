// r3tools.go —— R3/R4 工具装配面：内置工具 + 仓库工具 + CodeGraph + 检索 + 外部工具注册与排序。
package requirement

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"git.example.com/enjoydream/agentkit/textutil"
	"git.example.com/enjoydream/agentkit/toolprior"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"argus/internal/codesearch"
	"argus/internal/plugin"
)

// r3Tools R3 的工具表（每个任务构造一次，闭包绑定任务上下文）。
// 经 toolprior 表组织：优先级排序 + 策略提示 + 外部工具调用限制。
// v3.55.0 拆分：核心仓库工具/CodeGraph 工具/getDetail/外部工具各自成方法，
// 本函数只负责限额缺省与优先级表组装。
func (w *Workflow) r3Tools(ctx context.Context, task plugin.Task,
	spec *plugin.RequirementSpec) ([]tool.BaseTool, string, error) {
	pr := task.PR
	fileMax, issueMax, diffMax := w.GetFileMaxChars, w.GetIssueMaxChars, w.GetDiffMaxChars
	if fileMax <= 0 {
		fileMax = 20_000
	}
	if issueMax <= 0 {
		issueMax = 8_000
	}
	if diffMax <= 0 {
		diffMax = 30_000
	}

	getFile, getIssue, getDiff, err := w.r3RepoTools(ctx, task, pr, fileMax, issueMax, diffMax)
	if err != nil {
		return nil, "", err
	}
	getSymbol, getRefs, getCallers, getPoly, err := w.r3CodeGraphTools()
	if err != nil {
		return nil, "", err
	}

	// ---- 优先级表：三层决策约束（指引/排序/限制），见 agentkit/toolprior ----
	table := toolprior.NewTable()
	getFileWhen := "核实 AC 证据的唯一来源：每条 implemented/partial 判定前必查"
	if spec == nil {
		getFileWhen = "核实 diff 看不到的实现上下文（调用方/边界条件/历史约定）时使用"
	}
	table.Add(toolprior.Entry{Tool: getFile, Priority: toolprior.PriorityCore,
		When: getFileWhen, Cost: "low"})
	// 条件暴露：需求未引用 issue 时 get_issue 是死工具，不进表（省模型注意力）
	if spec == nil || len(spec.IssueRefs) > 0 {
		table.Add(toolprior.Entry{Tool: getIssue, Priority: toolprior.PriorityCore,
			When: "需求源引用了 issue，核对原始需求措辞时使用", Cost: "low"})
	}
	table.Add(toolprior.Entry{Tool: getDiff, Priority: toolprior.PriorityCore,
		When: "变更概览，判定前先通读", Cost: "low"})
	if getSymbol != nil && getRefs != nil {
		table.Add(toolprior.Entry{Tool: getSymbol, Priority: toolprior.PriorityCore,
			When: "需要核实 diff 中看不到的符号定义/签名（跨文件实现）时使用", Cost: "low"})
		table.Add(toolprior.Entry{Tool: getRefs, Priority: toolprior.PriorityCore,
			When: "评估被改符号的跨文件影响面（调用方/使用点）时使用", Cost: "low"})
	}
	if getCallers != nil {
		table.Add(toolprior.Entry{Tool: getCallers, Priority: toolprior.PriorityCore,
			When: "Go 函数改动前先查调用形态分布（词法级，比 get_references 更聚焦调用）", Cost: "low"})
	}
	if getPoly != nil {
		table.Add(toolprior.Entry{Tool: getPoly, Priority: toolprior.PriorityCore,
			When: "diff 涉及 Python/JS/TS 函数且需核实定义/参数时使用", Cost: "low"})
	}
	if st := w.r3SearchTool(); st != nil {
		table.Add(toolprior.Entry{Tool: st, Priority: toolprior.PriorityCore,
			When: "跨文件找调用方/相似实现/字符串或配置线索（符号定位之外的词法线索，含非 Go 文件）时使用", Cost: "low"})
	}
	if w.Knowledge != nil {
		if kt := w.Knowledge.AsTool(); kt != nil {
			table.Add(toolprior.Entry{Tool: kt, Priority: toolprior.PrioritySupport,
				When: "需引用团队编码规范/部署约定作判据时使用", Cost: "medium"})
		}
	}
	if w.SkillTool != nil {
		// 决策使用模式：提示词只带可用清单（runner.skillText 返回摘要），
		// 全文由 agent 按相关性经 use_skill 工具按需加载
		table.Add(toolprior.Entry{Tool: w.SkillTool, Priority: toolprior.PrioritySupport,
			When: "任务与可用清单中某条方法论相关时加载全文", Cost: "low"})
	}

	// get_detail（v3.39.0 大输出两级化）：被截断工具的全文取回通道
	getDetail, err := w.r3DetailTool()
	if err != nil {
		return nil, "", err
	}
	table.Add(toolprior.Entry{Tool: getDetail, Priority: toolprior.PrioritySupport,
		When: "需要被截断工具输出的完整内容（ref 在截断提示中）时使用", Cost: "low"})

	w.r3AddExternalTools(ctx, table)

	strategy := table.StrategyPrompt(ctx)
	// 循环异常守卫（v3.37.0）：整表套连续同参调用拦截——ReAct 死循环的唯一防线
	return wrapAnomalyGuard(newAnomalyGuard(), table.Ordered(ctx)), strategy, nil
}

// r3RepoTools 仓库内容三工具：get_file（行号前缀 + 大输出两级化 + 数据区）、
// get_issue（标题正文合并数据区）、get_diff（截断重取）。
func (w *Workflow) r3RepoTools(ctx context.Context, task plugin.Task, pr plugin.PRContext,
	fileMax, issueMax, diffMax int) (getFile, getIssue, getDiff tool.BaseTool, err error) {
	getFile, err = utils.InferTool("get_file",
		"读取仓库某文件在 PR head 提交时的完整内容，用于核实 diff 中看不到的实现。参数 path 为仓库相对路径。返回内容每行带行号前缀（行号|内容），引用证据 file:line 时使用该行号。",
		func(ctx context.Context, in *getFileIn) (*getFileOut, error) {
			content, err := w.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo, in.Path, pr.HeadSHA)
			if err != nil {
				return &getFileOut{Error: plugin.ReminderNote("tool-error", err.Error())}, nil // 工具错误回注文本（提醒信封），不中断 ReAct
			}
			// 逐行加行号（深挖 P0）：证据门要求 file:line 定位，而无行号的原文等于
			// 逼模型凭空编行号。行号前缀先于截断，保证已见行号真实可查。
			content = numberLines(content)
			// 大输出两级化（v3.39.0）：超限全文入任务级详情库（get_detail 取回），
			// 进上下文的只留上限内正文 + 取回指引
			content = w.stashOverflowAt(content, fileMax)
			// 文件内容是 PR 作者完全可控的注入面：回注 agent 前必须包数据区（防线 #1）
			return &getFileOut{Content: DataFence("get_file 文件内容（数据）", content)}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}

	getIssue, err = utils.InferTool("get_issue",
		"读取本仓库某个 issue 的标题与正文（补充需求上下文）。",
		func(ctx context.Context, in *getIssueIn) (*getIssueOut, error) {
			issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, in.Number)
			if err != nil {
				return &getIssueOut{Error: plugin.ReminderNote("tool-error", err.Error())}, nil
			}
			body := textutil.TruncNote(issue.Body, issueMax, "截断")
			// 标题+正文一起进数据区（两者都攻击者可控）
			return &getIssueOut{Body: DataFence("get_issue 标题与正文（数据）", issue.Title+"\n"+body)}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}

	getDiff, err = utils.InferTool("get_diff",
		"重新获取本次 PR 的清洗后 diff 文本（当上下文中 diff 被截断时使用）。",
		func(ctx context.Context, _ *emptyIn) (*getDiffOut, error) {
			if task.Diff != nil {
				text := task.Diff.Text
				text = textutil.TruncNote(text, diffMax, "截断")
				return &getDiffOut{Diff: DataFence("get_diff（数据）", text)}, nil
			}
			return &getDiffOut{Diff: ""}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}
	return getFile, getIssue, getDiff, nil
}

// r3CodeGraphTools Go 符号工具四件套（v3.15.0 P0-1 codegraph）：跨文件影响面/
// 定义定位。idx 为 nil（非 Go 变更/功能关闭/无工作副本）时返回 nil（不进表）。
func (w *Workflow) r3CodeGraphTools() (getSymbol, getRefs, getCallers, getPoly tool.BaseTool, err error) {
	if w.CodeGraph == nil {
		return nil, nil, nil, nil, nil
	}
	getSymbol, err = utils.InferTool("get_symbol",
		"按名称查询 Go 符号（函数/方法/类型/变量）的定义位置与签名，用于核实 diff 中看不到的实现细节。返回 file:line + 签名列表。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.Definitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				name := d.Name
				if d.Recv != "" {
					name = d.Recv + "." + d.Name
				}
				fmt.Fprintf(&b, "%s:%d  %s %s\n", d.File, d.Line, name, d.Signature)
			}
			return &symOut{Result: DataFence("get_symbol 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getRefs, err = utils.InferTool("get_references",
		"查询 Go 标识符在仓库中的出现位置（file:line 列表），用于评估跨文件影响面（谁调用了被改的函数/谁用了被改的类型）。精确大小写匹配。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.References(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到出现位置（可能仅定义处或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_references 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getCallers, err = utils.InferTool("get_callers",
		"查询 Go 函数的调用形态出现位置（pkg.F( / F( 词法匹配，file:line 列表）——评估改动影响面优先用本工具；词法级索引，引用前须用 get_file 核实。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.GoCallers(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到调用形态出现位置（可能未被调用或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_callers 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getPoly, err = utils.InferTool("get_poly_symbol",
		"按名称查询 Python/JavaScript/TypeScript 函数的定义位置与参数（词法级索引），用于核实 diff 中看不到的非 Go 实现。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.PolyDefinitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				if d.Doc != "" {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)  # %s\n", d.File, d.Line, d.Name, d.Args, d.Doc)
				} else {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)\n", d.File, d.Line, d.Name, d.Args)
				}
			}
			return &symOut{Result: DataFence("get_poly_symbol 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return getSymbol, getRefs, getCallers, getPoly, nil
}

// r3SearchTool search_code 全文检索工具（v3.84.0）：工作副本上的受控正则搜索，
// 覆盖符号索引到不了的词法线索（字符串/配置/非 Go 代码）。未注入工作副本路径
// （功能关闭/无工作副本池/无纳入文件）时返回 nil（不进表）。
func (w *Workflow) r3SearchTool() tool.BaseTool {
	if w.CodeSearchDir == "" {
		return nil
	}
	st, err := utils.InferTool("search_code",
		"在仓库工作副本中全文正则搜索代码（跨文件找调用方/相似实现/字符串与配置线索，含非 Go 文件）。pattern 为 Go 正则；glob 可选过滤文件（无 / 时按文件名匹配如 *.go，含 / 时按仓库相对路径匹配）。命中不等于结论：引用前须用 get_file 核实上下文。",
		func(ctx context.Context, in *searchIn) (*searchOut, error) {
			res, err := codesearch.Search(ctx, w.CodeSearchDir, in.Pattern,
				codesearch.Options{Globs: searchGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
			if err != nil {
				return &searchOut{Result: plugin.ReminderNote("tool-error", err.Error())}, nil
			}
			return &searchOut{Result: DataFence("search_code 检索结果（数据）", w.stashOverflowAt(res.Render(), detailOverflowMax))}, nil
		})
	if err != nil {
		w.Log.Warn("argus.r3.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// searchGlobs 可选 glob 入参归一（空串 → 无过滤）。
func searchGlobs(glob string) []string {
	if s := strings.TrimSpace(glob); s != "" {
		return []string{s}
	}
	return nil
}

// r3DetailTool get_detail（v3.39.0 大输出两级化）：被截断工具的全文取回通道。
func (w *Workflow) r3DetailTool() (tool.BaseTool, error) {
	return utils.InferTool("get_detail",
		"读取此前被截断的完整工具输出。ref 来自截断提示中的 ref 编号（如 \"d1\"）。",
		func(ctx context.Context, in *getDetailIn) (*symOut, error) {
			w.detailMu.Lock()
			content, ok := w.detailStore[in.Ref]
			w.detailMu.Unlock()
			if !ok {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			return &symOut{Result: DataFence("get_detail 完整输出（数据）", content)}, nil
		})
}

// r3AddExternalTools 外部 MCP 工具进表：按名排序（缓存前缀纪律）→ 风险分级
// 注解 → External 档 + 调用上限硬止损（v3.41.0 起上限可配，缺省 5）。
func (w *Workflow) r3AddExternalTools(ctx context.Context, table *toolprior.Table) {
	if w.Tools == nil {
		return
	}
	extra, err := w.Tools.Tools(ctx, w.ToolSpecs)
	if err != nil {
		w.Log.Warn("argus.r3.toolpool_error", "error", err.Error())
		return
	}
	// 缓存前缀纪律（v3.38.0）：MCP 目录来自 map 迭代、顺序不定——按名排序
	// 后再注册，保证工具表与策略提示逐字节稳定（provider context cache 前提）
	sortToolsByName(ctx, extra)
	// 风险分级（v3.39.0）：目录注解驱动——破坏性工具加显式警示并升成本档
	hints := w.Tools.Hints(w.ToolSpecs)
	for _, t := range extra {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		note := ""
		cost := "high"
		if h, ok := hints[info.Name]; ok && h.Destructive {
			note = "；⚠️ 该工具具有破坏性副作用，调用前确认必要性与最小影响范围"
			cost = "heavy"
		}
		// 外部工具：每次调用有网络/进程开销 → External 档 + 调用上限硬止损
		//（v3.41.0 起上限可配 ARGUS_EXTERNAL_TOOL_CALL_LIMIT，缺省 5）
		limit := w.ExternalToolCallLimit
		if limit <= 0 {
			limit = 5
		}
		table.Add(toolprior.Entry{
			Tool:     toolprior.LimitCalls(asInvokable(t), limit),
			Priority: toolprior.PriorityExternal,
			When:     fmt.Sprintf("外部系统工具（%s）：本地证据穷尽后再用%s", info.Name, note),
			Cost:     cost,
		})
	}
}

// ReviewTools R4 复用：审查工具表（get_file/get_diff/知识/技能/外部 MCP；
// spec 传 nil 自动跳过 AC 判定专属的 get_issue，When 文案切审查语境）。
// 与 R3 共用同一套工具基础设施——diffprep 清洗、数据区围栏、限流包装全部一致。
func (w *Workflow) ReviewTools(ctx context.Context, task plugin.Task) ([]tool.BaseTool, string, error) {
	return w.r3Tools(ctx, task, nil)
}

// asInvokable BaseTool → InvokableTool 断言（不可调用工具跳过外部限制包装）。
func asInvokable(t tool.BaseTool) tool.InvokableTool {
	if it, ok := t.(tool.InvokableTool); ok {
		return it
	}
	return nil
}

// sortToolsByName 工具表按名排序（原位）。Info 失败的工具保持相对位置（稳定排序）。
func sortToolsByName(ctx context.Context, tools []tool.BaseTool) {
	sort.SliceStable(tools, func(i, j int) bool {
		ni, erri := tools[i].Info(ctx)
		nj, errj := tools[j].Info(ctx)
		if erri != nil || errj != nil {
			return false
		}
		return ni.Name < nj.Name
	})
}

// ---------- 大输出两级化（v3.39.0） ----------

type getFileIn struct {
	Path string `json:"path" jsonschema:"description=仓库相对路径"`
}

type getFileOut struct {
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type getIssueIn struct {
	Number string `json:"number" jsonschema:"description=issue 编号（纯数字）"`
}

type getIssueOut struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Error string `json:"error,omitempty"`
}

type emptyIn struct{}

type getDetailIn struct {
	Ref string `json:"ref" jsonschema:"description=截断提示中的详情编号"`
}

type getDiffOut struct {
	Diff string `json:"diff"`
}

type symIn struct {
	Name string `json:"name" jsonschema:"description=符号名（函数/方法/类型/变量名）"`
}

type symOut struct {
	Result string `json:"result"`
}

type searchIn struct {
	Pattern    string `json:"pattern" jsonschema:"description=Go 正则表达式（如 MyFunc\\(|TODO|apiKey）"`
	Glob       string `json:"glob,omitempty" jsonschema:"description=可选文件过滤：无 / 按文件名（如 *.go），含 / 按仓库相对路径"`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"description=忽略大小写"`
}

type searchOut struct {
	Result string `json:"result"`
}
