# 架构总览

Argus 是单二进制审查平台，但通用能力几乎全部下沉到四个共享库——argus 本体只做审查领域编排。这是 v5.0.0「四库深度收敛」定下的形态：**领域归 argus，通用归共享库**。

## 一张图：Argus 与四个共享库

<div class="diagram">
<div class="diag-cap"><b>职责分层</b> · ARGUS + SHARED LIBRARIES</div>
<div class="arch-map">

<div class="arch-node top">
<div class="arch-badge">审查领域层</div>
<h4>Argus 本体（github.com/yi-nology/argus）</h4>
<p>R1–R5 确定性流水线 · 多专家插件编队 · 真值表裁决 · 影响面视图 · 评论指令交互 · 任务/存储/评测</p>
</div>

<div class="arch-stems"><span></span><span></span><span></span><span></span></div>

<div class="eco-grid">
<div class="eco-cell">
<div class="eco-tag">接入层</div>
<div class="eco-name">go-git-platform</div>
<div class="eco-role">git 平台 SDK · yi-nology</div>
<ul>
<li>7 平台统一 provider（gitea→工蜂）</li>
<li>PR/MR · 评论 · review · diff</li>
<li>webhook 签名校验与事件归一</li>
<li>App/PAT TokenSource · 条件请求</li>
</ul>
<div class="eco-consume">argus 消费面：sdkctx 装配 · poster 回帖 · poller 轮询 · webhook 准入</div>
</div>
<div class="eco-cell">
<div class="eco-tag">底座层</div>
<div class="eco-name">oekit</div>
<div class="eco-role">工程底座族 · 12 模块</div>
<ul>
<li>logx 日志 · core · hertz HTTP</li>
<li>data+gormx 存储三方言（SQLite/MySQL/PG）</li>
<li>concurrency 租约 · middleware 鉴权信封</li>
<li>pkg 工具 · metrics/tracing 观测</li>
</ul>
<div class="eco-consume">argus 消费面：横切全栈（存储方言 · HTTP 信封 · 租约 · 日志）</div>
</div>
<div class="eco-cell">
<div class="eco-tag">能力层</div>
<div class="eco-name">oagentkit</div>
<div class="eco-role">agent 能力族 · 12 模块</div>
<ul>
<li>llm 韧性客户端（重试/降级/限速）</li>
<li>agent 编队/黑板共识 · tools/acpx</li>
<li>textx 词法索引 · 文本截断</li>
<li>rag · guardx · queue/obs · mcpkit</li>
</ul>
<div class="eco-consume">argus 消费面：R3/R4 的 LLM 与工具 · 图索引 · 检索 · 评测管线</div>
</div>
<div class="eco-cell">
<div class="eco-tag">内核层</div>
<div class="eco-name">goagent-kernel</div>
<div class="eco-role">agent 执行内核 · yi-nology</div>
<ul>
<li>ReAct 轮次 · 工具预算</li>
<li>ptc 程序化工具编排（goja 沙箱）</li>
<li>microcompact/spill 上下文管理</li>
<li>Result.Describe 诊断归因</li>
</ul>
<div class="eco-consume">argus 消费面：ask_argus 问答 · ptc 工具编排 · golden 评测 harness</div>
</div>
</div>

<div class="arch-exit">
向下对接：<b>平台世界</b>（7 种 git 平台） · <b>LLM 网关</b>（任意 OpenAI 兼容，含内网网关） · <b>存储</b>（SQLite / MySQL / PostgreSQL） · <b>工作副本</b>（浅克隆沙箱） · 记忆内核 <b>agent-memory</b>（yi-nology，评审记忆/学习环）
</div>

</div>
</div>

一句话读图：**go-git-platform 是「手」**（与 git 平台世界对话），**oekit 是「地基」**（工程横切件），**oagentkit 是「大脑皮层」**（LLM/工具/检索能力），**goagent-kernel 是「执行小脑」**（agent 循环的运行时），**Argus 是「审查大脑」**——只写领域逻辑，四库在下面各管一摊。

## 四库定位对照

| 库 | 角色 | 归属 | 供给什么 | 在 Argus 中的地位 |
|---|---|---|---|---|
| **go-git-platform** | git 平台 SDK | yi-nology | 7 平台统一 provider：PR/评论/review/webhook/token | 全部进出平台的通道；换平台不换调用面 |
| **oekit** | 工程底座族 | oekit（12 模块） | 日志/HTTP/存储方言/租约/观测/鉴权信封 | 三方言存储、多副本租约、API 信封的地基 |
| **oagentkit** | agent 能力族 | oagentkit（12 模块） | LLM 客户端/编队/词法索引/检索/护栏/评测件 | 审查智能的全部通用件；argus 只做领域接线 |
| **goagent-kernel** | agent 执行内核 | yi-nology | ReAct 循环/工具预算/ptc 沙箱/上下文管理 | 问答（ask_argus）与评测 harness 的执行引擎 |

旁支成员：**agent-memory**（yi-nology）承载评审记忆与学习环；**go-gitcode**（yi-nology，indirect）是 GitCode 后端的 SDK 基座。它们同属共享库层，图上归入「向下对接」带。

## 一次审查的旅程：每一段用谁的力

<div class="diagram">
<div class="diag-cap"><b>数据流</b> · 从平台事件到回帖</div>
<div class="flow-row">
<div class="flow-step"><b>① 平台事件</b><small>webhook 签名校验 / poller 轮询兜底</small><em class="layer l-ggp">go-git-platform</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>② 任务队列</b><small>DB 即队列 · 幂等/去重/心跳</small><em class="layer l-oekit">oekit/data</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>③ R1 需求解析</b><small>AC 清单化 · 写作面</small><em class="layer l-oagent">oagentkit/llm</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>④ R2 diff 深读</b><small>工作副本准备 · 清洗回填</small><em class="layer l-argus">argus diffprep</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑤ R3 完成度判定</b><small>工具核实 · 图检索 · 词法索引</small><em class="layer l-oagent">textx · llm</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑥ R3.5 反思</b><small>批判→修订 · 剔除误报嫌疑</small><em class="layer l-oagent">oagentkit/llm</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑦ R4 专家插件</b><small>安全/SAST/OCR/SQL 编队互盲</small><em class="layer l-oagent">agent 黑板</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑧ R5 真值表裁决</b><small>APPROVE / COMMENT / REQUEST_CHANGES</small><em class="layer l-argus">argus 确定性</em></div>
<div class="flow-arrow"></div>
<div class="flow-step"><b>⑨ 报告与回帖</b><small>TL;DR + 行内意见 + 图 diff</small><em class="layer l-ggp">go-git-platform</em></div>
</div>
</div>

值得注意的是 ②⑧ 两段**不走 LLM**：任务队列是确定性存储编排，裁决是真值表短路——这两段是「可复现」承诺的锚点。⑤⑦ 的工具与检索由 oagentkit/textx（词法索引）、semsearch（语义索引，argus 适配）供给；问答/修复闭环走 goagent-kernel 的 ReAct 循环。

## Argus 本体怎么组织

内部 43 个包按 L0–L5 分层（完整表见仓库 `ARCHITECTURE.md`，由 `make arch` 门禁守住依赖方向）：

<div class="diagram">
<div class="diag-cap"><b>内部分层</b> · L0 → L5</div>
<div class="layer-stack">
<div class="layer-row hot"><span class="lay-tag">L5 上下文</span><div class="lay-body"><b>上下文供给</b><small>go-git-platform 平台数据 · 工作副本浅克隆沙箱 · 知识库（Milvus/本地 RAG）</small></div></div>
<div class="layer-row hot"><span class="lay-tag">L2 编排</span><div class="lay-body"><b>确定性编排</b><small>workflow/requirement（R1→R5 流水线）· runner 任务执行 · worker DB 队列</small></div></div>
<div class="layer-row"><span class="lay-tag">L3 能力</span><div class="lay-body"><b>领域能力</b><small>service 门面 · storage · impactview 影响面 · poster 回帖 · qa 问答 · semsearch 检索</small></div></div>
<div class="layer-row"><span class="lay-tag">L4 插件</span><div class="lay-body"><b>核心扩展点</b><small>plugin：Registry（插件注册）→ Dispatcher（路由/编队）→ Merger（合并/抑制）</small></div></div>
<div class="layer-row"><span class="lay-tag">L1 形态</span><div class="lay-body"><b>接入适配</b><small>webhook / poller · TaskAPI · MCP / A2A · CLI 副驾——形态是适配器，加接入不动核心</small></div></div>
<div class="layer-row"><span class="lay-tag">L0 底座</span><div class="lay-body"><b>纯底座（零 internal 依赖）</b><small>config · langcap 语言矩阵 · codesearch · semsearch · storage 方言——make arch 门禁守方向</small></div></div>
</div>
</div>

两条铁律贯穿始终：**形态是 L1 适配器**（加接入方式不动核心）、**agent 是 L4 插件**（加审查 agent 不动形态）；**路由/裁决/合并不走 LLM**（自主性只给阶段内的 ReAct 工具调用）。

## 为什么这么切

- **换库不伤领域**：平台 API 改版、LLM 换代、日志/存储底座升级，都发生在共享库层——argus 的领域代码（R1–R5、真值表、影响面）不受波及。
- **通用能力一处修全局受益**：词法索引（textx）、韧性 LLM 客户端（oekit/oagentkit 系）在多项目复用，修一次全员升级。
- **防漂移有门禁**：平台词表（config.SupportedPlatforms ↔ SDK registry）、语言矩阵（internal/langcap ↔ 各能力面）、MCP 描述词表——三处一致性都有 CI 测试钉死，架构图上的每一层都有对账单。

仓内更细的架构叙述（包级依赖矩阵、信任分层、数据面）见 [ARCHITECTURE.md](https://github.com/yi-nology/argus/blob/main/ARCHITECTURE.md) 与 [docs/architecture-deep-dive.md](https://github.com/yi-nology/argus/blob/main/docs/architecture-deep-dive.md)。
