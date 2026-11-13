---
name: argus-triage
description: This skill should be used when the user asks to "分诊/处理这批审查意见", "triage these review findings", "看看哪些是误报", "这个问题以前报过吗/怎么裁的", "清理一下 findings", "把误报压掉/恢复被压的意见", or when an Argus MCP server is connected and the agent needs to work through a batch of review findings (fix / dismiss / escalate), query finding history, or manage false-positive suppression.
compatibility: ">=6.9.0 <8.0.0"  # 适配的 argus 服务版本区间（P3-01 #26）；skills 随 argus 仓 tag 发布
---

# Argus 审查意见分诊（triage）

一批审查意见的逐条处置工作流：修复 / 驳回（误报抑制）/ 上报用户，以及「以前怎么裁的」历史核查。连接方式、token 三档与工具参数细节见 **argus-mcp** skill（本 skill 假定 `/mcp` 已连通）；动手前先 `get_capabilities` 看清 fix/检索/知识库启用态，免盲调报错。

## 标准分诊流

1. **定位任务**：手里有 task_id 直接用；没有则 `list_tasks(status=done)` 枚举，或已知 PR 用 `ask_argus` 拿最新结论。
2. **拉结构化意见**：`list_findings(task_id)` → file/line/severity/comment/fp/suggested_fix。**不要解析 markdown 报告**——结构化字段就是为此存在。
3. **逐条三问**：
   - *以前报过吗？* → `search_findings(query=关键词)`：status=open/suppressed、times_seen=复现次数。suppressed = 人工已判过误报（含家族变体）；「【上轮已提出，仍成立】」标记 = 记忆飞轮认定的跨轮复现，优先处理。
   - *是真问题吗？* → severity 处置规则：**high** 必修——给用户修复方案（`get_suggestion(fp)` 取建议；`fix_finding` 沙箱修复出 diff，**绝不自动应用**）；**medium** 逐条判断，确认误报必须向用户说明理由；**low** 呈现给用户决定，不阻塞。
   - *是误报吗？* → `suppress_finding(fp)`（**需 admin 档 token**；一键会家族传播——同问题的全部措辞变体一并压制）。抑制错了用 `restore_finding(fp)` 对称恢复。ci 档调用报「需 admin 档」是预期行为，转告用户，勿重试。
4. **汇总回报**：每条意见的处置（修/驳/上报）+ 一句话理由。**不要把「已抑制」当成「已解决」**——抑制只是不再报告，问题本身可能还在。

## 判定纪律

- **fp 短码是意见的持久身份**（6-64 位十六进制，list_findings/报告 `fp:` 后），suppress/restore/fix/suggest 全用它引用。
- **抑制耐久性**：同问题的措辞变体由家族吸收（精确侧 0.4 / 模糊侧 0.2，行距 ±2 或文件组件相交）；彻底重写的新形态仍可能再现——对新条目再 suppress 一次即可（自动建族）。
- **429** = 触发每 token 限速（缺省 120 次/分钟），退避重试，勿 hammer。
- **「仓库不在 ARGUS_REPO_ALLOWLIST 允许清单内」** = 该 token 权限范围外的仓库，转告用户找部署方扩清单，勿重试。
- 分诊结论必须给用户看理由；**不要静默批量抑制**——抑制进学习环，误压会教坏后续审查。

## 边界

- `fix_finding` 只落 argus 侧沙箱、回显 diff，**永不推送分支**；应用与否是用户/调用方的决定。
- suppress/restore 是信任边界写操作（admin 档）；外部 agent 默认持 ci 档，被拒属预期。
- 历史账本（search_findings）查的是**本仓**意见流水；组织级规范/结论用 `search_knowledge`。
