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

返回 `{tasks: [...]}` 数组，每条：`task_id/status/kind/platform/owner/repo/number/created_at`（Unix 秒），`head_sha`/`verdict` 非空才带。created_at 倒序。巡检工作流（list_tasks → list_findings → 分诊）的入口；非法 status 在工具层直接报错，不落 HTTP 码。

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
| `fix` | fix_finding | `-pr platform/owner/repo#number` `-fp` |

review/poll **退出码契约**：`0`=通过（无达阈值意见）/异步提交成功；`1`=有达阈值意见（建议阻断提交）；`2`=错误（执行失败或用法错误）；`4`=空 diff 跳过；`130`=信号中断。hook 里想放行一切照旧 `|| true`，想阻断就保留退出码。`-json` 输出的 `findings` 为 `{file, line, severity, note}` 数组，`report` 保留原始 markdown。

env：`ARGUS_MCP_URL`（缺省 `http://127.0.0.1:8899/mcp`）、`ARGUS_APITOKEN`。单次调用超时 `-timeout` 缺省 **30s**——凡涉及 LLM 的调用（`review` 同步快审 / `ask` 带追问 / `fix`）实测会超（215 深度思考模型单轮 5-60s+），显式调大（如 `-timeout 3m`）；`poll` 自带 10min 上限不吃全局超时。
