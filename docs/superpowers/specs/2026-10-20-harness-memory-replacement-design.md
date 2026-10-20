# argus 四库深度替换设计（2026-10-20）

> 指令：以 oekit/oagentkit llms.txt + yi-nology/agent-harness + yi-nology/agent-memory
> 深度优化 argus，全部替换手写重叠面。本文是映射裁定单源。
>
> **落地终态**：v3.149.0（agentkit→oagentkit 在途迁移收口）→ v3.150.0（oekit+oagentkit
> 全模块拉最新）→ v3.151.0（agent-memory 底座 opt-in）→ v3.152.0（harness budget
> 接入 qa）→ v3.153.0（agent-memory 生命周期 GC 日级 ticker）→ v3.154.0（R4 逐插件
> checkpoint，同任务重试续跑闭环；commitgate·toolfail·tooldisclose·promptx·spill
> 裁定不接，理由在下）。全部批次四门禁绿（golden 69）已 push。

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

### v3.153.0 harness checkpoint + commitgate 接入 runner（**终态裁定：不接/部分翻案**）

实施前评估改判（2026-10-20，实证在案）：

- **commitgate 不接**：它是进程内闸，而 argus 终态提交走 `FinishTaskFenced`
  （DB 条件更新 + 执行归属 fencing，v3.8.0）——DB 闸跨进程天然更强，
  进程内闸在多副本下管不住「两条分支都赢了」。poster stale_skip（R5.4）
  是其上语义层，两者已闭合。
- **checkpoint 部分翻案**：评估时判「产物中途落库是缺失前置」——错。
  stage_artifacts 自 v3.12.0 就在（R1/R2 产物即时落库 + 同任务重试回流）。
  真正的洞是 **R4（LLM 主体成本）不在 checkpoint 面**：进程崩溃 → 同任务
  重试 → R4 整批重跑。**但 harness checkpoint.BuildPlan 语义不匹配 R4**：
  BuildPlan「连续前缀」为有序步骤链设计，R4 fan-out 插件彼此独立、乱序
  完成，前缀规则会把合法完成集作废（失序多跑）。故 R4 续跑复用既有
  stage_artifacts 机制扩展（v3.154.0），BuildPlan 保持不接。
- **v3.153.0 实装内容改为 agent-memory 生命周期接线**（GC 日级 ticker，
  命名空间与 dream 供给面同源）。

### v3.154.0 R4 逐插件 checkpoint（续跑真闭环）

- Dispatcher 新增 `OutcomeHook` + `RunWithHook`（Run 签名不变委派 nil hook）：
  产物落定（含 best-of-N 聚合成功）即回调；失败/跳过/取消不落。
- workflow dispatchR4：`Select → restoreR4Outcomes 回流 → PruneByBudget →
  RunWithHook(落键 ckpt_r4/<plugin>)`；返回 sel = 复用集+执行保留集
  （G4 覆盖裁判口径不变）。
- 并发安全：r4CkptHook 持任务级 ckptMu（MergeTaskStageArtifacts 读-改-写
  合并不加锁会丢键，并发测试钉住）。
- 回流失败模式：键损坏=当没命中回执行集重跑（checkpoint 永不做正确性决策）。
- 编队共识（RunConsensus）本批不接 checkpoint——重试重跑（正确性不变，
  节省面留后续）。

- **toolfail 不接**：模型侧指引 argus 已有等价面（plugin.ReminderNote 工具
  错误指引逐工具在案）；失败分类表的消费方在 oagentkit agentrun 内部重试
  编排（上游方向，非 argus 侧）。
- **tooldisclose 不接**：argus 内部 ReAct 工具表个位数（get_file/search_*
  等），全量披露开销可忽略；19 工具面是 argus 对外 MCP server 契约
  （给外部 AI），不是内部消费面。
- **watchdog 不接**：oagentkit agent v0.10.0 agentrun CallID 原生配对
  （声明↔结果精确配对）已覆盖。
- **spill 不接**：TruncNote 换写此前轮次已裁定不做；截断全文取回面已有
  get_report/MCP 通道。
- **promptx 不接**：argus prompt 是逐插件审查任务型定制（golden 钉住），
  sysprompt Builder 面向 agent 系统提示词分节场景，替换=全插件改写净收益≈0。
- **ptc 候选专项**（非替换）：程序化工具编排塌缩 ReAct 轮次，新能力评审另立。

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
