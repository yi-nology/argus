---
name: argus-precommit
description: This skill should be used when the user asks to "提交前帮我自查一下代码", "用 Argus 预审这段 diff", "run a pre-commit review", "check my staged changes before committing", "装一个提交前 AI 审查钩子 / set up the Argus pre-commit hook", or wants a review-fix-recheck loop on local changes before pushing.
---

# Argus 提交前自查（pre-commit 预审）

在代码进入 PR 之前，用 Argus 的 `review_diff` 工具对本地暂存 diff 做单轮快审，把问题消灭在提交前。与完整审查的区别：**不建任务、不落库、不回帖**——没有 task_id、没有 fp 短码、没有报告页，纯粹一次性意见输出。

## 前置

MCP 客户端已挂载 argus（配置见 argus-mcp skill 或 `docs/ide-mcp.md`；token 用 ci 档）。服务端未配 LLM（`ARGUS_LLM_*`）时 `review_diff` 报「模型未配置」，此时预审不可用，转告用户，勿重试。

## 标准工作流

### 1. 取 diff

```bash
git diff --staged          # 暂存区；未暂存用 git diff
git diff --staged -- <dir> # 只审某个子目录
```

diff 超过 120KB 会被服务端按字节截断——大改动**分文件/分目录多次送审**，不要整包硬塞。

### 2. 送审

调用 `review_diff`，`diff` 参数传完整 diff 文本。返回两种形态：

- 意见列表：每行 `- file:line [severity] 说明`（severity ∈ high/medium/low）；
- 「✅ 未发现问题」。

### 3. 处置意见

- **high**：修完再提交。按意见改代码，然后回到第 1 步重审，直到清零或降级为可接受。
- **medium**：逐条判断——真问题就修；确认为误报时向用户说明理由后放过，不要静默忽略。
- **low**：呈给用户决定，不阻塞提交。
- `review_diff` 的意见**没有 fp 短码**（不落库），无法用 `fix_finding` 自动修复——要修复走人工或由你直接改代码。若用户想对「已进 PR 的意见」跑沙箱修复，那是 argus-mcp skill 的 `fix_finding` 工作流（PR 维度，有 fp）。

### 4. 循环终止

重审返回「✅ 未发现问题」或仅剩用户接受的意见即完成；连续两轮意见不收敛时停下向用户汇报，不要无限循环。

## git hook 一键化

`.git/hooks/pre-commit`（`chmod +x` 后生效；`|| true` 保证审查故障不阻塞提交）：

```bash
#!/bin/sh
git diff --staged | argus-copilot review || true
```

`argus-copilot` 是 argus 仓库的 MCP 参考客户端（env：`ARGUS_MCP_URL`、`ARGUS_APITOKEN`；源码 `cmd/argus-copilot`，`review` 子命令缺省读 stdin）。hook 输出是给人看的意见列表；AI 驱动的闭环（读意见→改→重审）用 MCP 工具调用而非 hook。

## 边界

- 预审是单轮 LLM 快审，覆盖面弱于完整审查（无 R1-R5 编队、无专家插件、无确定性裁决）——它是提交前的廉价兜底，不是审查的替代。
- diff 上下文有限，涉及其它文件的改动（调用方/接口契约）可能漏报；不确定的结论当疑问句处理。
- 保密仓库确保 argus 与本机同在内网（diff 会出本机）。
