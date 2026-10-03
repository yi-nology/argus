// qa_tools.go —— Q&A 工具装配面：diff/file/similar/search_code 工具集 + 工作副本目录管理。
package qa

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"git.example.com/enjoydream/agentkit/textutil"
	"git.example.com/enjoydream/agentkit/toolprior"
	"git.example.com/enjoydream/agentkit/workcopy"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/codesearch"
	argusctx "argus/internal/context"
	"argus/internal/plugin"
)

// qa 工具入出参（具名类型——InferTool 泛型推断需要）。

type similarIn struct {
	Keywords string `json:"keywords" jsonschema:"description=检索关键词（报错类型/模块名）"`
}

type similarItem struct {
	Number string   `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state,omitempty"`
	URL    string   `json:"url,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type similarOut struct {
	Issues []similarItem `json:"issues,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// searchManager 取平台 SearchManager 可选能力（type assertion；未实现返回 false）。
func searchManager(sdk *argusctx.SDKCtx, platform string) (provider.SearchManager, bool) {
	p, err := sdk.Provider(platform)
	if err != nil {
		return nil, false
	}
	sm, ok := p.(provider.SearchManager)
	return sm, ok
}

// tools Q&A 工具表（复用 R3 同源纪律：内容截断 + 数据区围栏防注入）。
func (e *Engine) tools(ctx context.Context, platform, owner, repo, number string) ([]tool.BaseTool, string, error) {
	getDiff, err := toolutils.InferTool("get_diff",
		"获取本 PR 的完整 diff 文本（回答涉及具体代码改动的问题时使用）。",
		func(ctx context.Context, _ *plugin.ToolDiffIn) (*plugin.ToolDiffOut, error) {
			md, err := e.SDK.GetCRDiff(ctx, platform, owner, repo, number)
			if err != nil {
				return nil, err
			}
			text := ""
			if md != nil {
				text = md.RawDiff
			}
			text = textutil.TruncNote(text, 30_000, "截断")
			return &plugin.ToolDiffOut{Diff: plugin.DataFence("get_diff（数据）", text)}, nil
		})
	if err != nil {
		return nil, "", err
	}

	getFile, err := toolutils.InferTool("get_file",
		"读取仓库某文件在 PR head 提交时的完整内容（核实 diff 看不到的实现上下文）。参数 path 为仓库相对路径。返回内容每行带行号前缀（行号|内容），引用证据 file:line 时使用该行号。",
		func(ctx context.Context, in *plugin.ToolFileIn) (*plugin.ToolFileOut, error) {
			ref := ""
			if cr, err := e.SDK.GetCR(ctx, platform, owner, repo, number); err == nil && cr != nil {
				ref = cr.HeadSHA
			}
			content, ferr := e.SDK.GetFileContent(ctx, platform, owner, repo, in.Path, ref)
			if ferr != nil {
				return &plugin.ToolFileOut{Error: plugin.ReminderNote("tool-error", ferr.Error())}, nil // 工具错误回注（提醒信封），不中断 ReAct
			}
			// 逐行加行号（深挖 P0）：回答涉及代码行为时要求 file:line 证据，
			// 无行号原文等于逼模型凭空编行号。行号前缀先于截断。
			content = textutil.NumberLines(content)
			if rc := utf8.RuneCountInString(content); rc > 20_000 {
				var trunc string
				trunc, _ = textutil.TruncRunes(content, 20_000)
				content = trunc + "\n…（截断）"
			}
			return &plugin.ToolFileOut{Content: plugin.DataFence("get_file 文件内容（数据）", content)}, nil
		})
	if err != nil {
		return nil, "", err
	}

	// search_similar_issues（v3.73.0，对标 PR-Agent /similar_issue）：仓库内
	// 相似 issue 检索——"这个问题之前报过吗/和 #x 是一回事吗"。SearchManager
	// 是可选能力，未实现的平台不加该工具（ReAct 面自然收窄）。
	var similarIssue tool.BaseTool
	if sm, ok := searchManager(e.SDK, platform); ok {
		si, terr := toolutils.InferTool("search_similar_issues",
			"在本仓库内搜索与某个问题/报错相似的历史 issue（判断问题是否已知/重复）。参数 keywords 为检索关键词（如报错类型、模块名）。",
			func(ctx context.Context, in *similarIn) (*similarOut, error) {
				res, _, serr := sm.SearchIssues(ctx, provider.SearchIssuesOptions{
					Query: in.Keywords, Repo: owner + "/" + repo, PerPage: 10,
				})
				if serr != nil {
					return &similarOut{Error: plugin.ReminderNote("tool-error", serr.Error())}, nil
				}
				items := make([]similarItem, 0, len(res))
				for _, r := range res {
					if r == nil {
						continue
					}
					items = append(items, similarItem{
						Number: r.Number, Title: r.Title, State: string(r.State),
						URL: r.WebURL, Labels: r.Labels,
					})
				}
				return &similarOut{Issues: items}, nil
			})
		if terr != nil {
			return nil, "", terr
		}
		similarIssue = si
	}

	// search_code（v3.91.0）：工作副本全文检索（与 R3/R4 审查面同款纪律）。
	// v3.91.1 起惰性解析——目录就绪（含冷克隆，30s 预算）推迟到工具首调，
	// 不需要检索的提问不付任何克隆成本。
	var searchCode tool.BaseTool
	if e.WC != nil && e.CodeSearch {
		searchCode = e.searchCodeTool(platform, owner, repo, number)
	}

	table := toolprior.NewTable()
	table.Add(toolprior.Entry{Tool: getDiff, Priority: toolprior.PriorityCore,
		When: "回答涉及具体代码改动的问题前先通读", Cost: "low"})
	table.Add(toolprior.Entry{Tool: getFile, Priority: toolprior.PriorityCore,
		When: "核实 diff 看不到的调用方/实现细节时使用", Cost: "low"})
	if searchCode != nil {
		table.Add(toolprior.Entry{Tool: searchCode, Priority: toolprior.PriorityCore,
			When: "跨文件找调用方/相似实现/字符串或配置线索（get_file 只能按路径读）时使用", Cost: "low"})
	}
	if similarIssue != nil {
		table.Add(toolprior.Entry{Tool: similarIssue, Priority: toolprior.PrioritySupport,
			When: "用户问『有没有类似问题/是否已知』时检索历史 issue", Cost: "low"})
	}
	if e.Knowledge != nil {
		if kt := e.Knowledge.AsTool(); kt != nil {
			table.Add(toolprior.Entry{Tool: kt, Priority: toolprior.PrioritySupport,
				When: "涉及团队规范/历史结论时检索", Cost: "medium"})
		}
	}
	strategy := table.StrategyPrompt(ctx)
	return table.Ordered(ctx), strategy, nil
}

// qaWorkcopyBudget 冷克隆预算（QA 交互路径，不拖首答）。
const qaWorkcopyBudget = 30 * time.Second

// workcopyDir 就绪 PR 工作副本（30s 预算；失败静默降级）。池语义：Ensure 后
// 立即 Release（目录由 TTL 兜底存活），search_code 调用期按路径直读。
func (e *Engine) workcopyDir(ctx context.Context, platform, owner, repo, number string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, qaWorkcopyBudget)
	defer cancel()
	headSHA := ""
	if cr, err := e.SDK.GetCR(cctx, platform, owner, repo, number); err == nil && cr != nil {
		headSHA = cr.HeadSHA
	}
	key := workcopy.WorktreeKey{Platform: platform, Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}
	dir, err := e.WC.Ensure(cctx, key)
	if err != nil {
		e.log().Warn("argus.qa.workcopy_failed", "error", err.Error())
		return "", false
	}
	e.WC.Release(key)
	return dir, true
}

// searchCodeExec search_code 执行体（惰性/绑定目录两构造器单源；目录由调用方
// 解析，检索、错误回执与超长截断语义在此统一）。
func searchCodeExec(ctx context.Context, dir string, in *codesearch.ToolIn) *codesearch.ToolOut {
	res, serr := codesearch.Search(ctx, dir, in.Pattern,
		codesearch.Options{Globs: codesearch.NormalizeGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
	if serr != nil {
		return &codesearch.ToolOut{Result: plugin.ReminderNote("tool-error", serr.Error())}
	}
	text, _ := textutil.TruncRunes(res.Render(), 4000)
	return &codesearch.ToolOut{Result: plugin.DataFence("search_code 检索结果（数据）", text)}
}

// searchCodeTool 惰性构造 search_code 工具（v3.91.1）：目录解析（含冷克隆
// 30s 预算）推迟到工具首调，构造期零成本；解析失败时调用返回工具错误文本
// （ReAct 面自然降级，与其他工具错误同型）。
func (e *Engine) searchCodeTool(platform, owner, repo, number string) tool.BaseTool {
	var once sync.Once
	var dir string
	resolve := func(ctx context.Context) string {
		once.Do(func() {
			if d, ok := e.workcopyDir(ctx, platform, owner, repo, number); ok {
				dir = d
			}
		})
		return dir
	}
	st, err := toolutils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			d := resolve(ctx)
			if d == "" {
				return &codesearch.ToolOut{Result: plugin.ReminderNote("tool-error", "工作副本不可用（冷克隆超时或失败），本轮无法全文检索，请改用 get_file 按路径核实")}, nil
			}
			return searchCodeExec(ctx, d, in), nil
		})
	if err != nil {
		e.log().Warn("argus.qa.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// searchCodeToolFor 绑定目录构造 search_code 工具（单测锚点；dir 为空返回
// nil）。生产路径走 searchCodeTool（惰性）。
func (e *Engine) searchCodeToolFor(dir string) tool.BaseTool {
	if dir == "" {
		return nil
	}
	st, err := toolutils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			return searchCodeExec(ctx, dir, in), nil
		})
	if err != nil {
		e.log().Warn("argus.qa.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}
