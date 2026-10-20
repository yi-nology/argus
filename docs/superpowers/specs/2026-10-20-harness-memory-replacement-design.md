# argus 四库深度替换设计（2026-10-20）

> 指令：以 oekit/oagentkit llms.txt + yi-nology/agent-harness + yi-nology/agent-memory
> 深度优化 argus，全部替换手写重叠面。本文是映射裁定单源。

## 现状基线（v3.149.0）

- oekit 10 模块已迁入（v3.143~148 完成）；oagentkit 12 模块已迁入（v3.149.0）。
- agent-harness / agent-memory 尚未进依赖图。两仓均要求 Go 1.27（本机 1.27.1 ✓），
  tag 已在远端（harness v0.6.2 / memory v0.4.2），proxy 可达 ✓。

## 批次计划

### v3.150.0 oekit+oagentkit 全模块拉最新（前置批）

harness v0.6.2 要求 oagentkit 下限（agent v0.10.0 / llm v0.13.0 / guardx v0.8.2 …），
memory v0.4.2 要求 oekit/pkg v0.17.0；argus 现钉旧线，必须先拉平。

oagentkit 破坏式吸收（versions.json breakingIndex）：

| 变更 | argus 触点 |
|---|---|
| `llm/llm` → `llm`（v0.9.0 包提模块根） | bootstrap/{models,bootstrap,assemble}, config_accessors, runner 等 ~14 文件 |
| `obs/obsx` → `llm/obsx`（v0.12.0） | 1 文件 |
| `rag/rag` → `rag`（v0.6.0 模块根） | 9 文件 |
| `guardx/audit` → `obs/audit`（v0.7.0/v0.8.0） | assemble.go, runner.go |
| `runtime/breaker` 退役（v0.5.0） | 无触点（已在 oekit/concurrency/breaker）✓ |
| `toolgate/invoke` → `guardx/invoke`（v0.12.0） | 无直接触点（toolprior 在用） |

oekit 同步拉新：pkg v0.17.0 / data v0.16.0 / concurrency v0.17.1 / bootstrap v0.6.4 /
core v0.9.2 / hertz v0.7.1 / logx v0.7.1 / metrics v0.1.9 / middleware v0.12.1 / kitx v0.2.1。

### v3.151.0 agent-memory 底座（opt-in）

替换面：internal/memory（文件存储）、R3 注入（LoadIndexWithOrg）、dream 写侧。

| argus 现状 | agent-memory 对应 | 收益 |
|---|---|---|
| 一文件一事实 markdown，**写入零脱敏** | capture 管道 Redactor 必需（PatternRedactor 兵底 + argus 领域规则叠加） | 记忆落库前打码——堵漏报经记忆文件落盘的泄漏面 |
| R3 索引原文注入 prompt | pipeline/recall 八段 + **MemoryBand 注入防伪** | 跨会话不可信内容防伪包裹（9 条攻击载荷钉住） |
| 文件名序索引（description 行扫描） | FTS5 词法召回 + 分桶配额 + 三路丢弃可观测 | 检索质量升级 |
| 无生命周期 | lifecycle gc/consolidate/retention | 记忆不再只增不减 |

形态：`ARGUS_MEMORY_KERNEL=1` opt-in（默认走旧文件面，与 semsearch 同款灰度纪律）；
独立 sqlite 文件（不并 argus.db）；**无模型显式失败**（ErrNoLLM）与 argus 确定性哲学
相容——dream 的确定性信号走手工写入路径，不引 LLM 提炼。
写侧读侧双跑一段后才考虑旧面退场（本批不退场）。

### v3.152.0 harness budget 接入 qa

qa 会话现按**轮数**切窗（conversation.Split maxTurns）+ 滚动摘要；budget 提供
分段计量 + Pressure 分档（75%/90%）+ 压实计划 + 归档接口。滚动摘要逻辑保留为
Summarizer 实现。毛/净移除量分开取（NetRemovedTokens），分段估算不回填 provider 报数。

### v3.153.0 harness checkpoint + commitgate 接入 runner

- R1-R5 阶段快照经 `checkpoint.Committer`（抢序号→落库→仅失败回滚）落 SQLite CAS；
  进程重启 `SeedFromStore` 回灌闸；`BuildPlan` 纯函数恢复判定
  （空洞不采信/终态不自动重开/序号单调）。收益：大 MR ~15min 审查中断后续跑而非重跑。
- verdict 提交经 `commitgate.Guard`（Acquire 令牌 + ReleaseLease 凭令牌回滚），
  堵「两条分支都赢了」——poster stale_skip 是事后发现，本闸是事前独占。

### 后续候选（评估后定，不在本轮承诺）

- **tooldisclose**：qa ask 19 工具全量挂 ReAct → 渐进披露省 prompt；
  先看 agent v0.10.0 agentrun 是否已内置等价面。
- **toolfail**：工具失败分类表（可重试/退避/重发安全正交）——落点在 qa ReAct 的
  MCP 工具错误面；agentrun CallID 配对已覆盖 watchdog 关注点。
- **spill**：poster 截断提示契约化 + 分页取回。
- **ptc**：ask ReAct 多轮塌缩成 1 轮（goja 沙箱）——新能力，独立评审。

### 裁定不做（留痕勿重探）

| 包 | 理由 |
|---|---|
| pii | argus 无个人信息面；凭据脱敏 guardx/logredact 已覆盖 |
| delta | 无流式增量面 |
| drain | oekit/bootstrap 优雅停机已覆盖 |
| pending | QA 轮次存 storage，无多轮挂起态载体需求 |
| reminder / policy / eventbus | 门面/等价面已在用（injectband、toolgate AuditGate、runtime progress.Bus） |
| dispatch | squad 是固定编队，无 agent 间动态派发拓扑 |
| protocol | argus 报告宿主自产（reportview 确定性渲染），非模型输出解析；模型 JSON 走 textx/llmjson |
| offload | 消息内无二进制大对象 |
| archcheck | test/arch 手写守卫 = argus 专属分层事实快照，diff 显式纪律优于 YAML 化 |
| watchdog | oagentkit agentrun CallID 原生配对已覆盖 |

## 门禁纪律

每批：make lint（vet+staticcheck+gofmt）+ make test（-race）+ make arch + make eval
（golden 69）全绿才提交；批次间各自 push。破坏式吸收逐签名对照 go doc，不猜。
