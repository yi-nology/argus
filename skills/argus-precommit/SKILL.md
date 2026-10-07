---
name: argus-precommit
description: This skill should be used when the user asks to "提交前帮我自查一下代码", "用 Argus 预审这段 diff", "run a pre-commit review", "check my staged changes before committing", "装一个提交前 AI 审查钩子 / set up the Argus pre-commit hook", "提交后轮询预审结果", or wants a review-fix-recheck loop on local changes before pushing.
---

# Argus 提交前自查（pre-commit 预审）

在代码进入 PR 之前，用 Argus 对本地暂存 diff 做预审，把问题消灭在提交前。两种形态：

- **同步快审**（`review_diff`）：不建任务不落库，调用即阻塞拿结果——交互式逐段自查用。
- **异步任务**（`review_diff_async`，v3.123.0）：秒回 task_id，服务端排队执行，提交方稍后轮询——hook/CI 里不阻塞提交流程，结果可追溯。

与完整审查的区别：两者都是单轮 LLM 快审，**没有 R1-R5 编队、没有专家插件、没有确定性裁决**；异步任务同样不回帖（无 PR 语义），报告只落库。

## 异步闭环（提交方轮询，推荐用于 hook）

1. 提交时把 staged diff 推给 argus：`review_diff_async(diff)`，立即返回 `{task_id, status, note}`。diff 上限 **48KB**（任务落库面），更大请分文件/分目录多次提交。
2. 提交方继续工作；稍后轮询 `get_report(task_id)`（建议 15-30 秒间隔）直到 `done/failed/stale`。`done` 的 `report` 字段即意见列表（每行 `- file:line [severity] 说明`）或「✅ 未发现问题」。
3. 参考客户端一键闭环（env：`ARGUS_MCP_URL`/`ARGUS_APITOKEN`）：

```bash
# 提交 hook 里：异步提交，不阻塞（|| true 保证审查故障不挡提交）
git diff --staged | argus-copilot -url … -token $TOKEN review -async || true
# 提交方随后轮询（自带 10min 上限、3s 间隔），输出报告
argus-copilot -url … -token $TOKEN poll -task <task_id>
```

`poll` 内部就是循环 `get_report`——没有 copilot 时用任意 MCP 客户端等价复刻。

## 同步快审

调用 `review_diff`，`diff` 传完整文本（≤120KB，纯内存不落库），阻塞返回意见列表或「✅ 未发现问题」。适合「提交前最后确认一段改动」的交互场景。

## 处置意见（两形态通用）

- **high**：修完再提交。按意见改代码，重新取 diff 送审（异步则再走一遍闭环），直到清零或降级为可接受。
- **medium**：逐条判断——真问题就修；确认为误报时向用户说明理由后放过，不要静默忽略。
- **low**：呈给用户决定，不阻塞提交。
- 预审意见**没有 fp 短码**（不走完整审查管线），无法用 `fix_finding` 自动修复——要修复走人工或由你直接改代码。若用户想对「已进 PR 的意见」跑沙箱修复，那是 argus-mcp skill 的 `fix_finding` 工作流（PR 维度，有 fp）。

## 终止与边界

- 重审返回「✅ 未发现问题」或仅剩用户接受的意见即完成；连续两轮意见不收敛时停下向用户汇报，不要无限循环。
- 预审是单轮快审，覆盖面弱于完整审查；diff 上下文有限，涉及其它文件的改动可能漏报，不确定的结论当疑问句处理。
- 服务端未配 LLM（`ARGUS_LLM_*`）时两形态都报「模型未配置」，预审不可用，转告用户，勿重试。
- 保密仓库确保 argus 与本机同在内网（diff 会出本机）。
