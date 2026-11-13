// server_tools.go —— MCP 工具 schema 构造器（v3.165.0 自 server.go 拆出；
// 零逻辑变更）。schema 文案是 get_capabilities/skills 文档的外部契约面，
// 改描述须同步 skills/argus-mcp/references/tools.md。

package mcpserver

import "github.com/mark3labs/mcp-go/mcp"

// platformVocab 平台词表（工具描述口径）：与 config.SupportedPlatforms 同步——
// mcpserver 生产面零 internal 依赖，一致性由 server_test 的 TestPlatformVocabSync
// 钉；改此词表须同步 skills/argus-mcp/references/tools.md。
const platformVocab = "gitea/forgejo/github/gitlab/gitee/gitcode/tencent_code"

// ---------- 工具定义 ----------

func reviewPrTool() mcp.Tool {
	return mcp.NewTool("review_pr",
		mcp.WithDescription("Argus 审查指定 PR：需求复核（R1→R5）+ 专家插件意见合并，回帖报告到 PR。wait=true 时阻塞至终态并返回结论与报告"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台："+platformVocab)),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("kind", mcp.Description("审查类型：requirement_compliance（默认）| code_review | visual_check"),
			mcp.WithStringEnumItems([]string{"requirement_compliance", "code_review", "visual_check"})),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func requirementComplianceTool() mcp.Tool {
	return mcp.NewTool("requirement_compliance",
		mcp.WithDescription("Argus 需求复核：解析需求 issue/描述 → 逐条判定验收清单完成度（含 scope creep）→ 确定性裁决。等价 review_pr(kind=requirement_compliance)"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台："+platformVocab)),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithBoolean("wait", mcp.Description("是否等待终态（默认 true）")),
		mcp.WithNumber("timeout_seconds", mcp.Description("等待超时秒数（默认 180）")),
	)
}

func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("列出 Argus 全部审查插件（名称/形态/技能/生效状态；platform+owner+repo 三参全传时附带该仓库 .reviewagents.yaml 配置视图与信任边界告警）"),
		mcp.WithString("platform", mcp.Description("可选：平台")),
		mcp.WithString("owner", mcp.Description("可选：仓库所属空间")),
		mcp.WithString("repo", mcp.Description("可选：仓库名")),
	)
}

func listTasksTool() mcp.Tool {
	return mcp.NewTool("list_tasks",
		mcp.WithDescription("枚举 Argus 审查任务（created_at 倒序，created_at/head_sha/verdict 摘要面）——巡检/找 task_id 的入口，配合 get_report/list_findings 消费"),
		mcp.WithString("status", mcp.Description("可选：按状态过滤，空=全部"),
			mcp.WithStringEnumItems([]string{"pending", "running", "done", "failed", "stale"})),
		mcp.WithNumber("limit", mcp.Description("可选：条数上限（缺省 20，上界 100）")),
	)
}

func searchCodeTool() mcp.Tool {
	return mcp.NewTool("search_code",
		mcp.WithDescription("在 PR 工作副本内做受控全文正则检索（跨文件找调用方/相似实现/字符串与配置线索，含非 Go 文件）——核查审查意见/修复前侦察用。pattern 为 Go 正则；命中不等于结论。服务未启用（ARGUS_CODESEARCH=0 或无工作副本池）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Go 正则表达式")),
		mcp.WithString("glob", mcp.Description("可选：文件过滤 glob（无 / 时按文件名匹配如 *.go，含 / 时按仓库相对路径匹配）")),
		mcp.WithBoolean("ignore_case", mcp.Description("可选：忽略大小写（默认 false）")),
	)
}

func searchKnowledgeTool() mcp.Tool {
	return mcp.NewTool("search_knowledge",
		mcp.WithDescription("检索 Argus 知识库：组织/仓库级规范文档、历史审查结论与误报判例（markdown 知识 + Milvus 语义/本地 TF-IDF 混合检索，与审查管线 R3/R4 的 search_knowledge 同源）——回答「团队怎么做/这条以前怎么裁」类问题、审查与修复前对齐团队规范用。命中不等于结论；服务未配置知识源时返回错误"),
		mcp.WithString("query", mcp.Required(), mcp.Description("自然语言查询")),
		mcp.WithNumber("top_k", mcp.Description("可选：返回条数（缺省 5，上界 10）")),
	)
}

func searchSemTool() mcp.Tool {
	return mcp.NewTool("search_sem",
		mcp.WithDescription("在 PR 工作副本内做语义检索（按意图/自然语言找代码，如「哪里处理认证」「哪里刷新 session token」——代码里未必有查询词，词法 search_code 查不到）。结构化摘要 embedding + 进程内余弦（Go=声明级符号索引，其余语言=文件级摘要 chunk），命中按相似度降序带 file:line 与摘要——命中是线索，引用前须 get_file 核实。服务未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）时返回错误"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台")),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("query", mcp.Required(), mcp.Description("自然语言意图描述")),
		mcp.WithNumber("top_k", mcp.Description("可选：返回条数（缺省 5，上界 10）")),
	)
}

func reviewDiffAsyncTool() mcp.Tool {
	return mcp.NewTool("review_diff_async",
		mcp.WithDescription("把一段 diff 作为异步预审任务提交（≤48KB），立即返回 task_id——提交时触发、稍后轮询的闭环形态。用 get_report(task_id) 取结果（意见列表或「✅ 未发现问题」）；需同步等结果的快审用 review_diff"),
		mcp.WithString("diff", mcp.Required(), mcp.Description("完整 diff 文本")),
	)
}

func getReportTool() mcp.Tool {
	return mcp.NewTool("get_report",
		mcp.WithDescription("按 taskId 获取 Argus 审查结果：状态/verdict/完整报告 markdown"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("任务 ID")),
	)
}

// askArgusTool ask_argus 工具 schema。
func askArgusTool() mcp.Tool {
	return mcp.NewTool("ask_argus",
		mcp.WithDescription("查询 Argus 对某 MR/PR 的最新审查结论（verdict + findings + 报告摘录）；"+
			"带 question 时基于该报告回答追问（如 \"为什么这条是 high\"）"),
		mcp.WithString("platform", mcp.Required(), mcp.Description("git 平台："+platformVocab)),
		mcp.WithString("owner", mcp.Required(), mcp.Description("仓库所属空间")),
		mcp.WithString("repo", mcp.Required(), mcp.Description("仓库名")),
		mcp.WithString("number", mcp.Required(), mcp.Description("PR 序号（纯数字）")),
		mcp.WithString("question", mcp.Description("可选追问；缺省返回完整审查结论")),
	)
}
