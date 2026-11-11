# Skills · 让 AI 直驱 Argus

Argus 附带一组 **Agent Skills**——装进你的 AI 客户端（Claude Code / ZCode / Cursor 等），AI 就知道怎么驱动 Argus：发起审查、读结构化意见、分诊处置、执行修复。配合 MCP 24 工具，实现「审查左移倒灌给 coding agent」。

## 两类技能，别装错

| 类型 | 技能 | 装到哪 | 作用 |
|---|---|---|---|
| **AI 客户端技能**（本页主角） | `argus-mcp` · `argus-triage` · `argus-precommit` | AI 客户端的 skills 目录 | 教外部 AI「何时调哪个工具、失败了怎么办」 |
| **服务端方法论技能** | `ocr-grading` 等 | 服务端 `ARGUS_SKILLS_DIR` | 审查 agent 内部注入（R4 插件用），与 AI 客户端无关 |

## 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash
```

默认装到 `~/.claude/skills`（Claude Code）。其他目标：

```bash
curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash -s -- --target zcode   # ~/.zcode/skills
curl -fsSL .../install-skills.sh | bash -s -- --target /path/to/skills           # 自定义目录
```

内网/离线：仓库检出后直接 `./scripts/install-skills.sh`（自动走本地复制，不联网）。脚本幂等，重复执行即升级到 main 最新版。

| 装完后你会得到 | 一句话 |
|---|---|
| **argus-mcp** | MCP 驱动主指南：连接方式、24 工具工作流、失败降级、安全边界；附 `references/tools.md` 参数级参考 |
| **argus-triage** | 意见分诊：逐条「修复 / 驳回（误报抑制）/ 上报」+ 「以前怎么裁的」历史核查 |
| **argus-precommit** | 提交前预审：同步快审（`review_diff`）与异步闭环（`review_diff_async` + 轮询） |

## 接入 MCP（一次配置）

技能负责「怎么用」，MCP 负责「连得上」。把 Argus 挂成 MCP server：

| 客户端 | 配置 |
|---|---|
| **Claude Code** | `claude mcp add --transport http argus http://<argus 地址>:8899/mcp --header "Authorization: Bearer <ci 档 token>"` |
| **Cursor / VS Code** | mcp.json 写 `{"servers": {"argus": {"url": ".../mcp", "headers": {"Authorization": "Bearer <token>"}}}}`，详见 [ide-mcp](https://github.com/yi-nology/argus/blob/main/docs/ide-mcp.md) |
| **ZCode / 其他 Agent Skills 客户端** | 按各自 MCP 装载约定挂 streamable HTTP 端点；技能放该客户端的 skills 目录 |
| **自建 agent** | 直接 POST `/mcp`（streamable HTTP 一问一答），或用参考客户端 `argus-copilot` |

**token 用 ci 档**（`ARGUS_API_TOKENS` 三档：read 只读 / ci 可建任务与全部工具 / admin 全权含误报抑制）——不要把 admin token 放进 agent 配置。

## AI 怎么用：一条工作流走通

<div class="diagram">
<div class="diag-cap"><b>AI 协作流</b> · 从一句话到闭环</div>
<div class="flow-row">
<div class="flow-step"><b>① 一句话</b><small>「用 Argus 审查 acme/app 的 PR #7」</small><em class="layer l-argus">你 → AI</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>② 发起审查</b><small>review_pr(wait=true) 阻塞到终态</small><em class="layer l-ggp">argus-mcp 技能</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>③ 取结论</b><small>get_report / list_findings 结构化意见</small><em class="layer l-oagent">勿解析 markdown</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>④ 分诊处置</b><small>search_findings 查史 → 修复/抑制/上报</small><em class="layer l-kernel">argus-triage 技能</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑤ 修复闭环</b><small>fix_finding 沙箱出 patch → 验证 → 复审</small><em class="layer l-ggp">argus-mcp 技能</em></div>
</div>
</div>

典型对话就是这么短：

> **你**：用 Argus 审查 obs/deploy-sql 的 MR 212，然后把意见处理掉
> **AI**：（调 `review_pr` → `get_report` 拿 TL;DR → `list_findings` 逐条 → `search_findings` 查史 → 高价值的 `fix_finding`，误报 `suppress_finding` 需要你确认 admin 操作 → 汇报处置清单）

## 常用场景速查

| 你说 | AI 会做 | 用到的技能 |
|---|---|---|
| 「用 Argus 审查这个 PR/MR」 | review_pr 等终态 → 报 TL;DR 与意见 | argus-mcp |
| 「看看 Argus 的审查意见」 | list_findings 结构化直出（fp/severity/suggested_fix） | argus-mcp |
| 「这批意见处理一下」 | 逐条三问（报过吗/修还是驳/要不要上报） | argus-triage |
| 「这个问题以前报过吗」 | search_findings 查历史账本与抑制记录 | argus-triage |
| 「按 fp 修这条意见」 | fix_finding 沙箱出 patch + 确定性验证 | argus-mcp |
| 「提交前帮我预审」 | review_diff 同步快审 / review_diff_async 闭环 | argus-precommit |
| 「问一下 Argus 这个设计为什么这么改」 | ask_argus 基于报告+仓库上下文多轮问答 | argus-mcp |
| 「装一个提交前审查钩子」 | 走 review_diff_async 的 hook 形态 | argus-precommit |

## 服务端方法论技能（顺带说明）

`skills/ocr-grading` 一类是 **Argus 服务端**的技能：放 `ARGUS_SKILLS_DIR` 目录后，审查 agent（R4 插件）会在审查中自动注入方法论——比如 OCR 文档评分规约。它们服务于审查质量，不需要装进 AI 客户端。

## 安全边界

- token 分三档，agent 侧只配 **ci 档**；误报抑制等写操作要 admin，AI 会报权限错并提示升级。
- 被审代码不能操纵评审：argus 侧有反注入防线（评审对象的指令性文本不会进入裁决链）。
- `fix_finding` 绝不推送分支——patch 只返回给你审阅采纳。
- token 可带仓库约束（`token:scope@platform:owner/repo`）与频率限制，给 agent 的 token 建议上仓库约束。

有问题或缺技能，欢迎 [提 issue](https://github.com/yi-nology/argus/issues)。
