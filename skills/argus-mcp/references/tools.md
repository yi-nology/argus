# Argus MCP 工具参考

与 `internal/channel/mcpserver`（server.go / copilot.go）逐字段校准。所有工具经 `POST /mcp`（streamable HTTP）调用，输出为缩进 JSON 或 markdown 文本；失败以 MCP tool error 文本返回（HTTP 层仍 200）。

## 端点与鉴权

| 项 | 值 |
|---|---|
| URL | `http://<argus 地址>:8899/mcp`（`ARGUS_ADDR` 缺省 `:8899`） |
| 方法 | POST（一问一答）；GET/DELETE 为 streamable 生命周期 |
| 鉴权 | `Authorization: Bearer <token>`；scope ∈ `read|ci|admin`（`ARGUS_API_TOKENS`），MCP 全工具 ci 档即可 |
| 会话 | 服务端下发 `Mcp-Session-Id` 响应头，客户端按 MCP 规范回传 |

## 通用约定

- `number` 一律纯数字 PR/MR 序号（`ask_argus` 服务端强校验，其余工具在服务层校验）。
- `fp` 指纹短码：6-64 位十六进制，来自报告正文 `fp:` 后或 `list_findings` 的 `fp` 字段；服务端统一转小写，大小写不敏感。
- 平台词表：`review_pr`/`requirement_compliance`/`ask_argus` 描述为 gitea/github/gitlab，`fix_finding` 另列 forgejo/gitee；实际以部署接入的平台为准（可用 `list_agents` 或部署配置确认）。

## 工具明细

### review_pr

发起审查（建任务 → 可选轮询至终态），报告回帖到 PR。

| 参数 | 类型 | 必填 | 缺省 | 说明 |
|---|---|---|---|---|
| `platform` | string | ✓ | | gitea/github/gitlab |
| `owner` | string | ✓ | | 仓库所属空间 |
| `repo` | string | ✓ | | 仓库名 |
| `number` | string | ✓ | | PR 序号（纯数字） |
| `kind` | string | | `requirement_compliance` | enum：`requirement_compliance` \| `code_review` \| `visual_check` |
| `wait` | boolean | | `true` | 是否阻塞至终态 |
| `timeout_seconds` | number | | `180` | 等待上界，服务端钳制 ≤600 秒 |

- `wait=true`：轮询至 `done`（返回 taskView）/`failed`（error：`审查失败: <fail_reason>`）/`stale`（taskView + `note`：报告未回帖）/超时（返回 `{task_id, status, note}`，任务继续执行）。
- `wait=false`：立即返回 `{task_id, status}`。
- 服务端轮询节奏 2s 起、1 分钟后自适应放宽到 5s，客户端无需复刻。

### requirement_compliance

`review_pr(kind=requirement_compliance)` 的固定形态，参数同上（无 `kind`）。

### review_diff

本地 diff 单轮预审：不建任务、不落库、不回帖。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `diff` | string | ✓ | 完整 diff 文本，≤120KB（超限服务端按字节截断） |

返回 markdown 意见列表（每行 `- 文件路径:行号 [high|medium|low] 说明`，severity 三档词表钉死）或「✅ 未发现问题」。服务端未配 LLM 时 error：`模型未配置（ARGUS_LLM_*），本地 diff 审查不可用`。需要「提交时触发、稍后轮询」的闭环用 `review_diff_async`。

### review_diff_async

本地 diff **异步**预审任务（v3.123.0）：diff 入队 → 秒回 task_id → 服务端 runner 执行（与 review_diff 同一执行体）→ 报告落库 → 提交方 `get_report` 轮询。不回帖、无 PR 语义。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `diff` | string | ✓ | 完整 diff 文本，≤**48KB**（落库面：artifacts_json 为 MySQL TEXT 列；更长请分块提交） |

成功返回 `{task_id, status, note}`；`note` 提示用 `get_report(task_id)` 轮询（建议 15-30s 间隔）。错误映射：空 diff/超限 →「diff 为空或超过 48KB 上限」；未配 LLM →「模型未配置（ARGUS_LLM_*）」。参考客户端闭环：`copilot review -async` + `copilot poll -task <id>`（内部循环 get_report，10min 上限）。

### list_agents

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo` | string | | 全可选；三参全传时附带该仓库 `.reviewagents.yaml` 生效视图与信任边界告警 |

返回插件清单 JSON（名称/形态/技能/生效状态）。

### get_report

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | 任务 ID |

返回 taskView：`{task_id, status}` 恒带，`verdict`/`report`/`fail_reason` 非空才带。任务不存在时 error（HTTP 码内嵌）。轮询至 `done/failed/stale` 即终态。

### list_tasks

| 参数 | 类型 | 必填 | 缺省 | 说明 |
|---|---|---|---|---|
| `status` | string | | 空=全部 | enum：`pending` \| `running` \| `done` \| `failed` \| `stale` |
| `limit` | number | | 20 | 上界 100（服务端钳制） |

返回 `{tasks: [...]}` 数组，每条字段为 **IDL 驼峰**：`taskId/status/kind/platform/owner/repo/number/createdAt`（Unix 秒），`headSha`/`verdict` 非空才带（注意与 get_report 的手写蛇形 `task_id` 不同源，勿混用）。createdAt 倒序。巡检工作流（list_tasks → list_findings → 分诊）的入口；非法 status 在工具层直接报错，不落 HTTP 码。

### get_task_stages

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | 任务 ID |

返回 R1 需求解析 / R2 变更理解 / R3 完成度判定 / R3.5 反思 / R4 专家委派 / R5 裁决的阶段时间线（完成状态与依据）。任务不存在时 error。

### list_findings

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | 任务 ID |

返回结构化意见数组，每条含：`file`、`line`、`severity`、`comment`、`by`（来源插件）、`fp`（指纹短码）、`suggested_fix`。替代解析 markdown 报告的唯一正道。

### get_suggestion

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | 任务 ID |
| `fp` | string | ✓ | 指纹短码（6-64 位十六进制） |

返回单条意见的修复建议内容（JSON）。只读取回，不改代码。未命中返回提示文本，用 `list_findings` 的 fp 重试。

### ask_argus

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo` | string | ✓ | 定位 PR |
| `number` | string | ✓ | 纯数字，服务端强校验 |
| `question` | string | | 可选追问 |

- 无 `question`：返回最近一次审查结论 `{task_id, finished_at, verdict, findings, report}`；无记录返回文本「该 PR 尚无已完成的 Argus 审查记录。」
- 带 `question`：基于最近报告回答（单轮直调）；无模型或无报告返回文本「无法回答：Argus 未配置 LLM，或该 PR 尚无已完成的审查记录。」

### search_code

PR 工作副本内受控全文正则检索（与审查管线 R3/R4 同款纪律：结果围栏防注入 + 4000 字符截断）。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo`/`number` | string | ✓ | 定位 PR（工作副本按当前 head 惰性准备，冷克隆预算 30s） |
| `pattern` | string | ✓ | Go 正则表达式 |
| `glob` | string | | 文件过滤 glob：无 `/` 按文件名匹配（`*.go`），含 `/` 按仓库相对路径匹配 |
| `ignore_case` | boolean | | 缺省 false |

返回围栏文本（命中列表或检索类错误回执，均在文本内）。**命中不等于结论**——引用前需读文件核实。环境面故障以 MCP error 返回：`ARGUS_CODESEARCH=0` 或未配置工作副本池 →「search_code 未启用」；冷克隆超时/失败 →「工作副本不可用」。

### search_knowledge

检索 Argus 知识库（v3.125.0）：与审查管线 R3/R4 的 search_knowledge 同源——组织/仓库级规范文档、历史审查结论、误报判例（Milvus 语义 + 本地 TF-IDF Hybrid，RRF 融合）。

| 参数 | 类型 | 必填 | 缺省 | 说明 |
|---|---|---|---|---|
| `query` | string | ✓ | | 自然语言查询 |
| `top_k` | number | | 5 | 上界 10（服务端钳制） |

返回围栏文本：命中片段按相关度降序，每条带 `score` 与元数据（如 `source`/`repo`）。空命中返回提示文本（非 error）。**命中不等于结论**——知识条目是团队沉淀的参考，不是裁决。未配置知识源（无 `ARGUS_KNOWLEDGE_DIR` 且无 Milvus）时 error：`知识库未启用`。

### search_sem

PR 工作副本**语义检索**（v3.126.0）：按意图/自然语言找代码——结构化摘要 embedding + 进程内余弦，补 `search_code` 词法正则答不了的「哪里处理认证/哪里刷新 session token」（代码里未必有查询词）。ARGUS_SEMSEARCH opt-in，与知识库共用 `ARGUS_EMBEDDING_*` 配置。

| 参数 | 类型 | 必填 | 缺省 | 说明 |
|---|---|---|---|---|
| `platform`/`owner`/`repo`/`number` | string | ✓ | | 定位 PR（工作副本按当前 head 惰性准备，冷克隆预算 30s） |
| `query` | string | ✓ | | 自然语言意图描述 |
| `top_k` | number | | 5 | 上界 10（服务端钳制） |

返回围栏文本：命中按相似度降序，每条带 `file:line`、符号名、`score` 与摘要。**命中是线索不是结论**——引用前须 `get_file` 核实。未启用（`ARGUS_SEMSEARCH=0` 或未配 `ARGUS_EMBEDDING_*`）时 error：`search_sem 未启用`；无命中返回提示文本（非 error）。

### get_capabilities

部署能力面快照（v3.129.0）：`{version, llm, diff_preview, qa, code_search, sem_search, knowledge, fix_agent, fix_push, codegraph, related_repos, ocr}`。无参数、纯读零副作用。调用受限工具（`fix_finding`/`suppress_finding`/`search_sem`/`search_knowledge`）前先看这里，免盲调报错。

### suppress_finding

误报抑制（v3.128.0，写侧）：按 fp 指纹短码把一条意见标记为误报——后续报告自动剔除（行号/措辞漂移由入库指纹覆盖），结论回流知识库与学习环。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo`/`number` | string | ✓ | 定位 PR |
| `fp` | string | ✓ | 指纹短码（6-64 位十六进制） |

返回 `{matched, fingerprint}`（matched=false 表示未命中既有意见但已预插抑制记录）。**一键 = 压掉全家族（v3.133.1）**：同一问题的历史措辞变体由家族传播自动一并抑制（精确侧 0.4+行距±2、模糊侧 0.2+文件相交；同函数不同问题不连坐）——无须逐变体打地鼠；`restore_finding` 同规则对称恢复。**需 admin 档 token**：/mcp 路由只需 ci 档，但本工具在服务端按 scope 透传做 admin 二次判定，非 admin 报 `suppress_finding 需 admin 档 token`。等价 HTTP 端点 `POST /findings/suppress`（同 admin 门）。CLI 无对应子命令（admin 写操作建议直接走 MCP/API）。

**耐久性边界（2026-10-09 实测 + v3.130.0/v3.133.0 两轮修复）**：剔除先按指纹精确匹配，未命中走近重复层——精确意见（带行号）行号 ±2 内的**重措辞复现已由宽松级（相似度 ≥0.4）吸收**；模糊意见（line=0 的多文件聚合/摘要形态）由吸收层（≥0.2 + 文件组件相交）兜底，彻底重述+改头换面的实测案例也不再复活。三层都只对已抑制条目放宽，普通条目仍按 0.6 合并。残余：精确意见的彻底重写（换框架措辞到 0.4 以下）仍会再现——对新条目再 suppress 一次即可（会连带建别名）。

### restore_finding

恢复被误抑制的意见（v3.131.0，`suppress_finding` 反向）：按 fp 指纹短码把 suppressed 条目置回 open，下一轮审查起重新可见。与评论命令 `restore fp:xxx`、HTTP `POST /findings/restore` 三形态同源执行体。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo`/`number` | string | ✓ | 定位 PR |
| `fp` | string | ✓ | 指纹短码（6-64 位十六进制；抑制记录与别名短码均可解析） |

返回 `{matched}`（false=无匹配或短码歧义，不猜测）。**需 admin 档 token**（与 suppress 同门）。

### get_repo_config

某仓库的按仓配置与生效参数只读视图（v3.187.0）：`repo_settings` 表覆盖 + 全局默认合并后的 effective 参数——核查某仓实际按什么预算/上限在跑、诊断预算拦截（任务 failed 且 FailReason 含「预算已耗尽」）前先查这里。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo` | string | ✓ | 定位仓库（platform 须为部署装配过的平台） |

返回 JSON：`found`（是否有按仓覆盖）、`settings`（覆盖集，monthly_token_budget/task_token_budget/max_diff_chars 可选键）、`effective`（三旋钮生效值：覆盖优先，无覆盖即全局默认）、`source`（repo_override|global_default）、`updated_by`/`updated_at`（写入者 token 指纹与时间）。**只读**——写入走 HTTP admin API：`PUT /repo-settings/{platform}/{owner}/{repo}`（body 为覆盖集 JSON，未知字段/非正整数拒绝；空覆盖拒收，移除用 DELETE；PUT|DELETE 需 admin 档 token）。`repo_settings` 与仓库内 `.reviewagents.yaml` 分层：前者是 admin token 写入的可信面（预算/上限可上调下调），后者是半可信文件面（插件选禁/评审规范，只能收窄）；SAST/OCR/LLM endpoint 永远只来自部署 env，任何仓库配置碰不到。

### get_impact

任务改动影响面快照（v3.183.0，blast radius）：分层管线（文件层/import 反向闭包/符号调用方/测试面/历史共变）+ 确定性风险分，零 LLM、可复现。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | 任务 ID |

返回快照 JSON：`changed`（改动文件）、`nodes`/`edges`（影响图；边方向 From=变更源→To=受影响方，rel ∈ import/caller/test_of/coupled）、`packages`（受影响包聚合，含 tests 覆盖数与 exposed 出口标记）、`risk`（level/score/reasons 逐条加分依据）、`coupling`（历史共变对）、`func_diffs`（v3.185.0 函数/方法级图 diff：签名变更/函数增删/调用边 ±，带 before/after 签名原文）、`mermaid` 不在此工具返回（报告评论里）——本工具为结构化数据面，渲染由调用方自行处理。任务 running 早期快照可能尚未生成（无此键），或 `ARGUS_IMPACTVIEW=0` 关闭，均返回提示文本。同数据面另有交互视图：`GET /tasks/:taskId/impact`（Bearer，自包含 HTML）。

### search_findings

本仓历史意见账本检索（v3.138.0）：prints 表是意见的持久身份（跨轮次/跨任务），query 对评语/文件路径 LIKE 匹配（含 % 按通配），last_seen 倒序。

| 参数 | 类型 | 必填 | 缺省 | 说明 |
|---|---|---|---|---|
| `platform`/`owner`/`repo` | string | ✓ | | 定位仓库（受 ARGUS_REPO_ALLOWLIST 约束）|
| `query` | string | ✓ | | 关键词（评语/文件路径）|
| `limit` | number | | 10 | 上界 50（服务端钳制）|

返回围栏文本：每条带 `file:line severity [status ×复现次数] fp:短码` + 评语截断。与 `search_knowledge`（组织级规范/结论）分工：这里查**本仓意见流水账**。「以前怎么裁的」类问题的事实源。检索为关键词 LIKE（参数化）单路——**语义补位曾实装后按实测数据回撤（v3.142.1）**：现役 embedder 对短中文意见文本的同话题/跨话题相似度分布完全重叠（同 0.326~0.552 vs 跨 0.322~0.489，bge 指令前缀亦不可分），任何阈值都会把无关意见排进来。换措辞查不到时请多试几个关键词（文件路径也可作 query）。

### fix_finding

单条意见的沙箱修复：隔离工作区内编码 agent 出 patch + 确定性验证（`ARGUS_FIX_VERIFY_CMD`）。**绝不推送分支。**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `platform`/`owner`/`repo`/`number` | string | ✓ | 定位 PR（platform 词表含 forgejo/gitee） |
| `fp` | string | ✓ | 指纹短码（6-64 位十六进制） |

返回 diff 与确定性验证结论 JSON。服务端未配 `ARGUS_FIX_AGENT` 时 error：`修复功能未启用（ARGUS_FIX_AGENT 未配置）`。修复耗时可达分钟级（`ARGUS_FIX_TIMEOUT` 缺省 10m），客户端超时须相应调大。

## argus-copilot 子命令映射（参考客户端）

```bash
go run ./cmd/argus-copilot -url <url> -token <tok> -timeout <dur> <子命令> [flags]
```

| 子命令 | 对应工具 | flags |
|---|---|---|
| `stages` | get_task_stages | `-task` |
| `findings` | list_findings | `-task`（返回本就是结构化 JSON） |
| `suggest` | get_suggestion | `-task` `-fp` |
| `ask` | ask_argus | `-pr platform/owner/repo#number` `-q`（可选） |
| `review` | review_diff / review_diff_async | `-f` diff 文件（缺省读 stdin）`\|` `-staged`（git diff --staged）`\|` `-uncommitted`（git diff HEAD），三输入源互斥；`-include-untracked` 把未跟踪文件合成 new-file diff 拼入（配 `-staged`/`-uncommitted`）；`-async` 异步提交；`-json` 结构化输出（mode/clean/findings/report）；`-fail-on high\|medium\|low\|none` 定退出码阈值（缺省 high） |
| `poll` | get_report 轮询 | `-task`；`-json`/`-fail-on` 与 review 同契约；自带 10min 上限、3s 间隔 |
| `capabilities` | get_capabilities | 无（能力面快照） |
| `fix` | fix_finding | `-pr platform/owner/repo#number` `-fp` |

review/poll **退出码契约**：`0`=通过（无达阈值意见）/异步提交成功；`1`=有达阈值意见（建议阻断提交）；`2`=错误（执行失败或用法错误）；`4`=空 diff 跳过；`130`=信号中断。hook 里想放行一切照旧 `|| true`，想阻断就保留退出码。`-json` 输出的 `findings` 为 `{file, line, severity, note}` 数组，`report` 保留原始 markdown。

env：`ARGUS_MCP_URL`（缺省 `http://127.0.0.1:8899/mcp`）、`ARGUS_APITOKEN`。单次调用超时 `-timeout` 缺省 **30s**——凡涉及 LLM 的调用（`review` 同步快审 / `ask` 带追问 / `fix`）实测会超（215 深度思考模型单轮 5-60s+），显式调大（如 `-timeout 3m`）；`poll` 自带 10min 上限不吃全局超时。
