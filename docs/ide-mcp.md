# IDE / 客户端经 MCP 接入 Argus

Argus 的 `/mcp`（streamable HTTP + Bearer）是标准的 MCP server，2026 年主流
IDE 与 agent 客户端都能直接挂载——**argus 不维护原生 IDE 插件**（裁定：维护
两套插件的成本高于 MCP 形态的收益，与"审查左移倒灌给 coding agent"的行业
方向同构）。本文给出常见客户端的开箱配置。

## 前置

| 项 | 值 |
|---|---|
| URL | `http://<argus 地址>:8899/mcp` |
| 鉴权 | `Authorization: Bearer <token>`；建议用 `ARGUS_API_TOKENS` 里的 **ci 档** token（读面全通、可触发审查/修复，无抑制权限） |
| 工具面 | `review_pr` / `requirement_compliance` / `review_diff` / `review_diff_async` / `list_tasks` / `list_agents` / `get_report` / `get_task_stages` / `list_findings` / `get_suggestion` / `ask_argus` / `search_code` / `fix_finding` 共 13 个（全清单与参数见 [skills/argus-mcp/references/tools.md](../skills/argus-mcp/references/tools.md)；`fix_finding` 未配 `ARGUS_FIX_AGENT`、`search_code` 未启用 `ARGUS_CODESEARCH`/工作副本池时调用报错） |

## AI agent 直接驱动（agent skill）

本仓库提供面向 AI agent 的技能文件 [skills/argus-mcp/SKILL.md](../skills/argus-mcp/SKILL.md)：连接方式、十工具工作流、失败降级判定与安全边界的单源。Claude Code 拷贝到 `~/.claude/skills/argus-mcp/`，其他支持 Agent Skills 的客户端（ZCode 等）按各自装载约定放置；MCP 客户端按上文挂载后，skill 负责「何时调哪个工具、失败了怎么办」。

## VS Code / Cursor

VS Code（1.99+）在工作区放 `.vscode/mcp.json`；Cursor 写入 `~/.cursor/mcp.json`：

```json
{
  "servers": {
    "argus": {
      "url": "http://argus.internal:8899/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

之后在 agent 对话里即可："用 list_findings 看看这个 PR 的审查意见"、
"对 fp:ab12cd34 这条跑一下 fix_finding"。

## Claude Code / 其他 CLI agent

```bash
claude mcp add --transport http argus http://argus.internal:8899/mcp \
  --header "Authorization: Bearer <token>"
```

## 命令行（免 IDE）

`cmd/argus-copilot` 是 MCP 参考客户端，IDE 之外的场景直接用：

```bash
go run ./cmd/argus-copilot -url http://argus:8899/mcp -token $TOKEN findings -task <id>
go run ./cmd/argus-copilot -url http://argus:8899/mcp -token $TOKEN fix \
  -pr gitea/acme/app#7 -fp ab12cd34
```

本地 pre-commit 预审（不建任务）：

```bash
git diff --staged | argus-copilot -url ... -token $TOKEN review || true
```

## 安全边界

- `fix_finding` 只落在 argus 侧隔离沙箱，**绝不推送分支**；diff 由人确认后自行应用。
- 误报抑制（`/findings/suppress`）需要 **admin 档** token——IDE 配置里不要放 admin token。
- 仓库数据经 agent 读出后进入 IDE 上下文，与人工打开仓库文件同机密级；对外保密
  仓库请确保 argus 与 IDE 同在内网。
