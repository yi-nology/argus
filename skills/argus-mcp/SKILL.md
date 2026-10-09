---
name: argus-mcp
description: This skill should be used when the user asks to "用 Argus 审查这个 PR/MR", "trigger an Argus review", "跑一下需求复核 / requirement compliance check", "看看 Argus 的审查意见 / findings", "ask Argus about this review", "按 fp 短码修复这条意见 / fix this finding", "提交前用 Argus 预审 diff / pre-commit review", or when an Argus MCP server (/mcp endpoint) is connected and the agent needs to drive PR auto-review, read structured findings, or execute sandboxed fixes through it.
---

# Argus MCP 驱动指南

Argus 是 PR 自动审查 bot：多 agent 编队审查（R1 需求解析 → R2 变更理解 → R3 完成度判定 → R3.5 反思 → R4 专家委派 → R5 确定性裁决），报告回帖到 PR。它把全部审查能力以 MCP 工具暴露在 `/mcp`（streamable HTTP + Bearer），任何 MCP 客户端或本 skill 的调用方都能直接驱动。本文件是工作流与判定规则；参数级细节（每个字段的类型/默认值/错误语义）见 [references/tools.md](references/tools.md)。

## 连接

| 项 | 值 |
|---|---|
| 端点 | `http://<argus 地址>:8899/mcp`（`ARGUS_ADDR` 缺省 `:8899`） |
| 传输 | MCP streamable HTTP（POST 一问一答） |
| 鉴权 | `Authorization: Bearer <token>`；token 来自服务端 `ARGUS_API_TOKENS`（三档）或旧单 token `ARGUS_APITOKEN`（等价 admin） |

token 分三档，**驱动审查/修复用 ci 档**：`read` 只读、`ci` 可建任务与调全部 MCP 工具、`admin` 全权（含误报抑制）。不要在 agent 配置里放 admin token；仅配了单 token `ARGUS_APITOKEN` 的部署（admin 等价）照常用，但注意它本可建 ci 档隔离。token 可带仓库约束（服务端 `token:scope@platform:owner/repo` 形态）与调用频率限制（`ARGUS_API_RATE_RPM`，超限 429）——被分配到的 token 只能触达其约束范围内仓库，越界调用报「仓库不在 ARGUS_REPO_ALLOWLIST 允许清单内」。

已装 MCP 客户端时的最小配置（其余客户端见 `docs/ide-mcp.md`）：

```bash
claude mcp add --transport http argus http://argus.internal:8899/mcp \
  --header "Authorization: Bearer <ci 档 token>"
```

没有 MCP 客户端时，用参考客户端 `argus-copilot`（env：`ARGUS_MCP_URL`/`ARGUS_APITOKEN`）：

```bash
go run ./cmd/argus-copilot -url http://argus:8899/mcp -token $TOKEN <子命令>
```

## 工具面速查（18 个，常驻注册）

| 工具 | 用途 | 必填 | 关键约束 |
|---|---|---|---|
| `review_pr` | 发起审查，可阻塞等终态 | platform/owner/repo/number | kind 缺省 requirement_compliance；wait 缺省 true；等待上界 10 分钟 |
| `requirement_compliance` | 需求复核固定形态 | 同上 | 等价 `review_pr(kind=requirement_compliance)` |
| `review_diff` | 本地 diff 同步快审，不建任务不落库 | diff | ≤120KB 超限截断；服务端未配 LLM 时报错 |
| `review_diff_async` | diff 异步预审任务，秒回 task_id | diff | ≤48KB（落库面）；`get_report` 轮询闭环；提交方 hook 首选 |
| `list_tasks` | 任务清单枚举（倒序摘要面） | 无 | status/limit 可选；找 task_id 的巡检入口 |
| `list_agents` | 插件清单 | 无 | 三参全传附带 `.reviewagents.yaml` 视图与信任边界告警 |
| `get_report` | 按 task_id 取状态/verdict/报告 | task_id | 轮询用 |
| `get_task_stages` | R1→R5 阶段时间线 | task_id | 「审得全不全」可观测 |
| `list_findings` | 结构化意见直出 | task_id | 含 fp 短码与 suggested_fix，**勿解析 markdown 报告** |
| `get_suggestion` | 单条意见的修复建议内容 | task_id/fp | fp 为 6-64 位十六进制短码 |
| `ask_argus` | 最新审查结论问答 | platform/owner/repo/number | number 纯数字；question 可选 |
| `search_code` | PR 工作副本受控全文检索 | platform/owner/repo/number/pattern | glob/ignore_case 可选；`ARGUS_CODESEARCH=0` 或无工作副本池时报错 |
| `search_knowledge` | 知识库语义检索（规范/历史结论/误报判例） | query | top_k 可选（缺省 5 上界 10）；与 R3/R4 审查面同源；未配知识源时报错 |
| `fix_finding` | 单条意见沙箱修复 | platform/owner/repo/number/fp | 绝不 push；未配 `ARGUS_FIX_AGENT` 时报错 |
| `search_sem` | 工作副本语义检索（按意图找代码） | platform/owner/repo/number/query | top_k 可选；`ARGUS_SEMSEARCH`+`ARGUS_EMBEDDING_*` 双门，未启用时报错 |
| `suppress_finding` | 误报抑制（fp 短码） | platform/owner/repo/number/fp | **需 admin 档 token**（scope 透传二次判定，ci 档报权限错）；回流知识库+学习环；重措辞复现由宽松级吸收（详见 tools.md 耐久性边界） |
| `restore_finding` | 恢复误抑制意见（suppress 反向） | platform/owner/repo/number/fp | **需 admin 档 token**；抑制错了用它撤销，下一轮起重新可见 |
| `get_capabilities` | 能力面快照（部署开了什么） | 无 | 纯读零副作用；调受限工具前先看这里 |

## 标准工作流

### 1. 触发审查并拿结论

- 常规：`review_pr`（wait 缺省 true），阻塞返回 `{task_id, status, verdict, report}`。中小 PR 够用（默认等 180 秒）。
- 大 MR（实测可到 ~15 分钟）：`review_pr(wait=false)` 立即拿 `{task_id, status}`，然后轮询 `get_report(task_id)` 直到 `done/failed/stale`，间隔 15-30 秒即可。
- `timeout_seconds` 服务端钳制上界 600 秒；需要更长等待只能走 wait=false + 轮询。

### 2. 读结构化意见

拿到 task_id 后用 `list_findings` 取结构化清单（file/line/severity/comment/by/fp/suggested_fix），逐条有 `fp` 短码可供后续 `get_suggestion`/`fix_finding`/`ask_argus` 引用。要看审查过程是否完整用 `get_task_stages`。**不要让模型解析 markdown 报告**——结构化字段就是为此存在。手里没有 task_id 时用 `list_tasks` 枚举（可按 status 过滤）再定位。

### 3. 追问

`ask_argus` 不带 question 返回该 PR 最近一次审查结论 JSON；带 question（如「为什么这条是 high」）基于最近报告回答。服务端未配 LLM 或该 PR 无审查记录时返回明确文本，照实转告用户。

### 4. 修复一条意见

动手前可先 `search_code` 在 PR 工作副本内检索相关调用方/相似实现，核实影响面。然后 `fix_finding(platform, owner, repo, number, fp)` 同步执行：隔离沙箱内编码 agent 出 patch + 确定性验证（`ARGUS_FIX_VERIFY_CMD`），返回 diff 与验证结论。**argus 绝不推送分支**——把 diff 呈给用户确认后，由用户或调用方自行应用。报错「修复功能未启用」说明服务端没配 `ARGUS_FIX_AGENT`，直接转告，勿重试。

### 5. 提交前本地预审

`review_diff(diff=完整 diff 文本)` 单轮快审，返回 markdown 意见列表（每行 `- 文件路径:行号 [high|medium|low] 说明`）或「✅ 未发现问题」。典型来源 `git diff --staged`。它不建任务、不落库、不回帖，适合 pre-commit 与「先自查再提 PR」。用参考客户端跑时另有 CLI 侧增强：`review -staged -include-untracked` 自组含未跟踪文件的 diff、`-json` 结构化输出、`-fail-on` 定退出码阈值（hook 阻断面）——细节见 argus-precommit skill。

### 6. 标记误报与查看能力面

确认某条意见是误报且持有 admin 档 token 时，`suppress_finding(platform, owner, repo, number, fp)` 一步闭环：后续报告自动剔除该意见，结论回流知识库与学习环（记忆飞轮）。ci 档调用报「需 admin 档 token」属预期，转告用户即可。新接手一个部署时先 `get_capabilities` 看清 fix/检索/知识库哪些开着，再决定工作流怎么走。

## 失败与降级判定

- `review_pr` 返回 `{task_id, status, note:"等待超时…"}`：任务还在跑，转 `get_report` 轮询，不要重复建任务。
- `failed` 终态：error 文本含 fail_reason，原样给用户。
- `stale` 终态：审查完成但 PR 已有更新提交、报告未回帖——结论仅对旧 head 成立，向用户说明。
- 「任务不存在」：task_id 写错或任务已被清理，重新 `review_pr`。
- fp 校验失败（须 6-64 位十六进制）或 number 非纯数字：修正入参重试。
- `review_diff` 报「模型未配置（ARGUS_LLM_*）」、`ask_argus` 报「无法回答」：服务端 LLM 缺位，转告用户。

## 安全边界

- `fix_finding` 只落 argus 侧隔离沙箱，**永不推送分支**；应用 diff 是调用方/人的职责。
- 误报抑制有 `suppress_finding` 工具与 `POST /findings/suppress` 端点两形态，**都要 admin 档 token**（工具级 scope 透传二次判定，ci 档调用会明确报权限错）——确认是误报再调，它回流知识库与学习环；抑制错了用 `restore_finding`（或 `POST /findings/restore`）撤销。
- **token 落地纪律**：MCP 客户端配置文件（如 `~/.claude.json`）与 shell env 里的 token 是明文——勿提交进 git、勿贴进对话/issue/截图；外部 AI 一律用 ci 档最小权限，勿复用 admin token。
- 仓库数据经 agent 读出后进入调用方上下文，与人工打开仓库文件同机密级；保密仓库确保 argus 与调用方同在内网。

## 参考文件

- **[references/tools.md](references/tools.md)** — 全部 18 工具的参数表、返回结构、错误语义、argus-copilot 子命令映射（含 review/poll 退出码契约）。入参拿不准时先查它。
