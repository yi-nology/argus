// r3tools.go —— R3/R4 工具装配面：内置工具 + 仓库工具 + CodeGraph + 检索 + 外部工具注册与排序。
package requirement

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/toolgate/toolprior"

	"argus/internal/codesearch"
	"argus/internal/plugin"
	"argus/internal/semsearch"
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
	getTrace, getSymCtx, err := w.r3GraphTools()
	if err != nil {
		return nil, "", err
	}

	// ---- 优先级表：三层决策约束（指引/排序/限制），见 oagentkit/toolgate/toolprior ----
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
	if getTrace != nil {
		table.Add(toolprior.Entry{Tool: getTrace, Priority: toolprior.PriorityCore,
			When: "查两符号间调用链路（改动如何传导/上游从哪进来）", Cost: "low"})
	}
	if getSymCtx != nil {
		table.Add(toolprior.Entry{Tool: getSymCtx, Priority: toolprior.PriorityCore,
			When: "查符号定义+调用方+被调全景（消歧后，比 get_callers 全）", Cost: "low"})
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
	return wrapAnomalyGuard(ctx, newAnomalyGuard(), table.Ordered(ctx)), strategy, nil
}

// r3RepoTools 仓库内容三工具：get_file（行号前缀 + 大输出两级化 + 数据区）、
// get_issue（标题正文合并数据区）、get_diff（截断重取）。
func (w *Workflow) r3RepoTools(ctx context.Context, task plugin.Task, pr plugin.PRContext,
	fileMax, issueMax, diffMax int) (getFile, getIssue, getDiff tool.BaseTool, err error) {
	getFile, err = utils.InferTool("get_file",
		plugin.ToolGetFileDesc,
		func(ctx context.Context, in *plugin.ToolFileIn) (*plugin.ToolFileOut, error) {
			content, err := w.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo, in.Path, pr.HeadSHA)
			if err != nil {
				return &plugin.ToolFileOut{Error: plugin.ToolErrNote(err)}, nil // 工具错误回注文本（提醒信封），不中断 ReAct
			}
			// 逐行加行号（深挖 P0）：证据门要求 file:line 定位，而无行号的原文等于
			// 逼模型凭空编行号。行号前缀先于截断，保证已见行号真实可查。
			content = textutil.NumberLines(content)
			// 大输出两级化（v3.39.0）：超限全文入任务级详情库（get_detail 取回），
			// 进上下文的只留上限内正文 + 取回指引
			content = w.stashOverflowAt(content, fileMax)
			// 文件内容是 PR 作者完全可控的注入面：回注 agent 前必须包数据区（防线 #1）
			return &plugin.ToolFileOut{Content: DataFence("get_file 文件内容（数据）", content)}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}

	getIssue, err = utils.InferTool("get_issue",
		"读取本仓库某个 issue 的标题与正文（补充需求上下文）。",
		func(ctx context.Context, in *getIssueIn) (*getIssueOut, error) {
			issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, in.Number)
			if err != nil {
				return &getIssueOut{Error: plugin.ToolErrNote(err)}, nil
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
		func(ctx context.Context, _ *plugin.ToolDiffIn) (*plugin.ToolDiffOut, error) {
			if task.Diff != nil {
				text := task.Diff.Text
				text = textutil.TruncNote(text, diffMax, "截断")
				return &plugin.ToolDiffOut{Diff: DataFence("get_diff（数据）", text)}, nil
			}
			return &plugin.ToolDiffOut{Diff: ""}, nil
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

// ---------- 消歧符号图工具（v5.11.0 纯图模式） ----------

// 图边向词表（semsearch.SymRef.Rel；semsearch 不导出关系词表，与 qa 包同款
// 手写单点，改 semsearch 词表须同步两处）。
const (
	graphRelCalls    = "calls"     // 本步调用下一步
	graphRelCalledBy = "called_by" // 本步被下一步调用（无向 BFS 逆向步）
)

// graphTraceHops get_trace 跳数（与 semsearch graphHopsDefault 同值手写，改此须同步）。
const graphTraceHops = 4

// graphNeighborCap get_symbol_context 调用方/被调单侧文案上限（底座
// graphCallersCap/graphCalleesCap 同值——底座已封顶，此处只管渲染文案）。
const graphNeighborCap = 8

// graphEdgeNote 图面边界措辞随精确边模式切换（precise=Index.Precise()）：
// ARGUS_GRAPH_PRECISE 部署开启且成功应用精确边时为 go/types 类型解析级
// （接口分派/方法值不可见），否则词法级。get_trace/get_symbol_context 提示共用
// （qa.edgeQualityNote 同款措辞的 requirement 包内单点）。
func graphEdgeNote(precise bool) string {
	if precise {
		return "类型解析级边（go/types；接口分派/方法值不可见）"
	}
	return "词法级边（接口分派/方法值不可见）"
}

// r3GraphTools 消歧符号图工具对（v5.11.0 纯图模式）：get_trace 两符号最短调用
// 链 + get_symbol_context 单符号全景。w.Graph 为 nil（图索引未注入：codegraph
// 关闭/工作副本不可用/图构建失败降级）时返回 nil（不进表）——与 CodeGraph
// 四件套独立判空互不绑定；降级文案为提示文本非 error（查无结果是正常查询结果）。
func (w *Workflow) r3GraphTools() (getTrace, getSymCtx tool.BaseTool, err error) {
	if w.Graph == nil {
		return nil, nil, nil
	}
	getTrace, err = utils.InferTool("get_trace",
		"查询两个 Go 符号间的最短调用链（无向 BFS，≤4 跳）——改动如何传导到下游、上游从哪里进来。返回逐跳链，每步带 file:line。",
		func(ctx context.Context, in *traceIn) (*symOut, error) {
			if w.Graph == nil {
				return &symOut{Result: "符号图未注入（本次审查不可用图查询，改用 get_callers/get_references）"}, nil
			}
			from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
			if from == "" || to == "" {
				return &symOut{Result: "from/to 均不能为空"}, nil
			}
			chain, found := w.Graph.Path(from, to, graphTraceHops)
			if !found {
				// 不可达/符号不在图内是正常查询结果：提示文本非 error，注明边界。
				return &symOut{Result: fmt.Sprintf(
					"未找到 %s → %s 的 ≤%d 跳调用链（%s，可改用 get_callers 查调用形态）",
					from, to, graphTraceHops, graphEdgeNote(w.Graph.Precise()))}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "get_trace（%s → %s）≤%d 跳最短调用链，%s，引用前须 get_file 核实：\n",
				from, to, graphTraceHops, graphEdgeNote(w.Graph.Precise()))
			for i, r := range chain {
				if i > 0 {
					switch r.Rel {
					case graphRelCalls:
						b.WriteString("—calls→ ")
					case graphRelCalledBy:
						b.WriteString("—called_by→ ")
					}
				}
				if r.Path != "" {
					fmt.Fprintf(&b, "%s (%s:%d)\n", r.Symbol, r.Path, r.Line)
				} else {
					fmt.Fprintf(&b, "%s\n", r.Symbol)
				}
			}
			return &symOut{Result: DataFence("get_trace 调用链（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, err
	}
	getSymCtx, err = utils.InferTool("get_symbol_context",
		"查询 Go 符号定义位置、调用方与被调全景（消歧符号图，比 get_callers 全；同名多候选合并邻接）。返回 定义 file:line + 调用方/被调列表。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			if w.Graph == nil {
				return &symOut{Result: "符号图未注入（本次审查不可用图查询，改用 get_callers/get_references）"}, nil
			}
			name := strings.TrimSpace(in.Name)
			if name == "" {
				return &symOut{Result: "name 不能为空"}, nil
			}
			self, callers, callees, cands, ok := w.Graph.SymbolContext(name)
			if !ok {
				// 未解析到符号是正常查询结果：提示文本非 error。
				return &symOut{Result: fmt.Sprintf("未在符号图内解析到符号 %q（裸名或 dir.Recv.Name 全名均可）", name)}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "get_symbol_context（symbol=%s）\n", name)
			if self.Path != "" {
				fmt.Fprintf(&b, "定义：%s (%s:%d)\n", self.Symbol, self.Path, self.Line)
			} else {
				fmt.Fprintf(&b, "定义：%s\n", self.Symbol)
			}
			// 候选数 >1 即邻接为同名合并，提示引用前逐候选核实（v5.10.0 同款语义）。
			if cands > 1 {
				fmt.Fprintf(&b, "同名候选 %d 处，邻接已合并（引用前须逐候选核实）\n", cands)
			}
			fmt.Fprintf(&b, "%s，引用前须 get_file 核实\n", graphEdgeNote(w.Graph.Precise()))
			if seg := renderGraphNeighbors(callers); seg != "" {
				fmt.Fprintf(&b, "调用方（≤%d）：%s\n", graphNeighborCap, seg)
			}
			if seg := renderGraphNeighbors(callees); seg != "" {
				fmt.Fprintf(&b, "被调（≤%d）：%s\n", graphNeighborCap, seg)
			}
			return &symOut{Result: DataFence("get_symbol_context 图上下文（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, err
	}
	return getTrace, getSymCtx, nil
}

// renderGraphNeighbors 图邻接单行渲染（get_symbol_context 调用方/被调用）：有
// 定义位置带 (file:line)，无位置（预算截断挤出 loc 的边残留）只显符号；「、」分隔。
func renderGraphNeighbors(refs []semsearch.SymRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Path != "" {
			parts = append(parts, fmt.Sprintf("%s (%s:%d)", r.Symbol, r.Path, r.Line))
		} else {
			parts = append(parts, r.Symbol)
		}
	}
	return strings.Join(parts, "、")
}

// r3SearchTool search_code 全文检索工具（v3.84.0）：工作副本上的受控正则搜索，
// 覆盖符号索引到不了的词法线索（字符串/配置/非 Go 代码）。未注入工作副本路径
// （功能关闭/无工作副本池/无纳入文件）时返回 nil（不进表）。
func (w *Workflow) r3SearchTool() tool.BaseTool {
	if w.CodeSearchDir == "" {
		return nil
	}
	st, err := utils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			res, err := codesearch.Search(ctx, w.CodeSearchDir, in.Pattern,
				codesearch.Options{Globs: codesearch.NormalizeGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
			if err != nil {
				return &codesearch.ToolOut{Result: plugin.ToolErrNote(err)}, nil
			}
			return &codesearch.ToolOut{Result: DataFence("search_code 检索结果（数据）", w.stashOverflowAt(res.Render(), detailOverflowMax))}, nil
		})
	if err != nil {
		w.Log.Warn("argus.r3.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// r3DetailTool get_detail（大输出两级化；v5.0.0 存档面切磁盘——ref 白名单挡注入，
// 全文落盘自 stashDetail）：被截断工具的全文取回通道。
func (w *Workflow) r3DetailTool() (tool.BaseTool, error) {
	return utils.InferTool("get_detail",
		"读取此前被截断的完整工具输出。ref 来自截断提示中的 ref 编号（如 \"d1\"）。",
		func(ctx context.Context, in *getDetailIn) (*symOut, error) {
			ref := strings.TrimSpace(in.Ref)
			if !spillRefRe.MatchString(ref) || w.SpillDir == "" {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			raw, err := os.ReadFile(filepath.Join(w.SpillDir, ref))
			if err != nil {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			return &symOut{Result: DataFence("get_detail 完整输出（数据）", string(raw))}, nil
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

type getIssueIn struct {
	Number string `json:"number" jsonschema:"description=issue 编号（纯数字）"`
}

type getIssueOut struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Error string `json:"error,omitempty"`
}

type getDetailIn struct {
	Ref string `json:"ref" jsonschema:"description=截断提示中的详情编号"`
}

type symIn struct {
	Name string `json:"name" jsonschema:"description=符号名（函数/方法/类型/变量名）"`
}

type traceIn struct {
	From string `json:"from" jsonschema:"description=起始符号名（裸名或 dir.Recv.Name 全名）"`
	To   string `json:"to" jsonschema:"description=目标符号名（裸名或 dir.Recv.Name 全名）"`
}

type symOut struct {
	Result string `json:"result"`
}
