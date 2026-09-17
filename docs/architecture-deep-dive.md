# Argus 设计深度解析 —— 从架构到实现的完整学习指南

> 日期：2026-09-01（v3.0.5 基线）｜2026-09-08 更新：修正收敛后的代码位置 + 新增第十四章
> （v3.1–v3.4 架构演进）｜基于代码实现梳理（非设计稿）

---

## 目录

- [一、Argus 是什么](#一argus-是什么)
- [二、分层架构](#二分层架构)
- [三、数据流全景](#三数据流全景)
- [四、L4 插拔层 —— 最核心的设计](#四l4-插拔层--最核心的设计)
  - [4.1 统一契约](#41-统一契约)
  - [4.2 四类适配器](#42-四类适配器v32-起-acpx-加入)
  - [4.3 Registry（注册表）](#43-registry注册表)
  - [4.4 Dispatcher（确定性路由 + fan-out 执行）](#44-dispatcher确定性路由--fan-out-执行)
  - [4.5 Merger（合并器）](#45-merger合并器)
- [五、L3 编排层 —— R1→R5 Workflow](#五l3-编排层--r1r5-workflow)
  - [R1 需求解析](#r1-需求解析)
  - [R2 变更理解](#r2-变更理解)
  - [R3 完成度判定](#r3-完成度判定)
  - [R4 专家委派](#r4-专家委派)
  - [R5 汇总裁决](#r5-汇总裁决)
- [六、L5 上下文层](#六l5-上下文层)
  - [6.1 diffprep（Diff 预处理）](#61-diffprepdiff-预处理)
  - [6.2 LLM 客户端](#62-llm-客户端)
  - [6.3 知识子系统（RAG）](#63-知识子系统rag)
- [七、并发模型与可靠性](#七并发模型与可靠性)
  - [7.1 DB 即队列](#71-db-即队列)
  - [7.2 Staleness（过期判定）](#72-staleness过期判定)
  - [7.3 两阶段优雅停机](#73-两阶段优雅停机)
  - [7.4 心跳与崩溃恢复](#74-心跳与崩溃恢复)
- [八、存储设计](#八存储设计)
- [九、回帖系统](#九回帖系统)
- [十、可观测性](#十可观测性)
- [十一、配置派生哲学](#十一配置派生哲学)
- [十二、安全设计](#十二安全设计)
- [十三、关键设计决策总结](#十三关键设计决策总结)
- [十四、v3.1–v3.4 架构演进](#十四v31v34-架构演进)
  - [14.1 v3.1——协议面铺开](#141-v31协议面铺开)
  - [14.2 v3.2——acpx：CLI 编码 agent 进编](#142-v32acpxcli-编码-agent-进编)
  - [14.3 v3.3——agentkit v0.7.x 七项能力迭代](#143-v33agentkit-v07x-七项能力迭代)
  - [14.4 v3.4——agentkit v0.8.0 三原语落地 + 一个"不接"](#144-v34agentkit-v080-三原语落地--一个不接)

---

## 一、Argus 是什么

Argus 是一个**可插拔多 Agent 代码审查平台**，核心形态是 PR 评论 Bot：当开发者提交 PR/MR 时，Argus 自动审查代码变更，检查是否满足需求（验收清单），并给出 `APPROVE / COMMENT / REQUEST_CHANGES` 结论。

**差异化定位**：不只是"代码质量检查"，而是**需求合规驱动**的审查——先解析需求（R1），再理解变更（R2），逐条核对验收标准（R3），最后确定性裁决（R5）。

**核心约束**：编排用确定性 Go 代码，自主性只给阶段内 agent（ReAct 调工具），路由/裁决不走 LLM。

---

## 二、分层架构

```
┌─────────────────────────────────────────────────────────────┐
│ L1 形态层    PR Bot │ CLI │ MCP Server │ A2A │ IM │ 会话    │
├─────────────────────────────────────────────────────────────┤
│ L2 门面层    TaskAPI（异步任务）+ SessionAPI（同步流式）      │
├─────────────────────────────────────────────────────────────┤
│ L3 编排层    Workflow: R1→R5 需求合规审查                    │
├─────────────────────────────────────────────────────────────┤
│ L4 插拔层    Registry → Dispatcher → Merger                 │
├─────────────────────────────────────────────────────────────┤
│ L5 上下文    git-platform-sdk │ 工作副本 │ 知识 │ 工具      │
└─────────────────────────────────────────────────────────────┘
```

**三条设计原则**：

1. **形态是 L1 适配器** —— 加新形态（MCP/A2A/IM）不动核心
2. **Agent 是 L4 插件** —— 加新审查能力不动形态
3. **编排用确定性 Go 代码** —— 路由/裁决/合并不走 LLM，可审计可测试

**源码目录结构**：

```
argus/
  main.go                      # 启动装配：配置 → 存储 → SDK → 插件 → 编排 → worker → HTTP
  router.go / router_gen.go    # hz 生成路由壳（IDL 声明，DO NOT EDIT）
  biz/                         # hz 生成：路由/中间件/handler 壳/DTO
  internal/
    config/                    # 全局配置（env 驱动 + 窗口派生）
    runner/                    # 单任务执行器（组装上下文 → workflow → poster）
    plugin/                    # L4 插拔层：registry / dispatcher / merger / breaker / config
      adapter_builtin/         #   builtin 适配器（进程内 Eino agent）
      adapter_cli/             #   cli 适配器（外部命令行，如 OCR）
      adapter_remote/          #   remote 适配器（远端服务，a2a/mcp）
    workflow/requirement/      # L3 编排：R1→R5（eino ADK agent + 工具）
    context/                   # L5 上下文：sdk_ctx / diffprep / workcopy / githubapp
    storage/                   # GORM 存储：tasks / finding_prints / review_targets
    knowledge/
      skill/                   #   SkillProvider（SKILL.md 方法论注入）
      rag/                     #   KnowledgeService（RAG 检索，本地实现）
    llm/                       # LLM 客户端薄封装（重试/限速/截断/fitInput）
    worker/                    # Worker pool（DB 即队列 + 心跳 + 两阶段停机）
    poster/                    # 回帖服务（staleness 门控 + 三级降级）
    reportview/                # 报告模板渲染
    progress/                  # 进度事件总线（SSE）
    notify/                    # octopus IM 推送
    poller/                    # 轮询兜底任务源
    channel/mcpserver/         # MCP Server（Argus as MCP Server）
    audit/                     # 审计日志
    obs/                       # Prometheus 指标注册
    textutil/                  # 文本截断工具
    toolpool/                  # MCP client 工具池（扩展点）
    service/                   # handler 薄实现（绑定→调编排→统一响应壳）
  test/e2e/                    # 端到端测试（mock 全家桶）
  deploy/                      # docker-compose + k8s kustomize + Grafana + Runbook
  docs/                        # 插件开发指南、GitHub App 配置等
```

---

## 三、数据流全景

```
Webhook 事件 / CLI 任务 / 轮询发现
  │
  ▼
┌─────────────────────────────────────────┐
│ Service (webhook handler)               │  同步路径：过滤 → 幂等 → 落库(pending) → 返回
│  ├─ 签名校验 (platform secret)          │
│  ├─ 事件类型白名单                       │  cr.opened / cr.synchronize / cr.reopened
│  ├─ bot 自审保护                        │  Actor == ARGUS_BOT_USERNAME → skip
│  ├─ draft 默认不审                      │  ARGUS_SKIP_DRAFT
│  └─ 同 head SHA 去重                    │  tasks 表查询 + 事务内落库防 TOCTOU
└──────────────┬──────────────────────────┘
               │ status=pending
               ▼
┌─────────────────────────────────────────┐
│ Worker Pool (DB 即队列)                 │  N 个 goroutine 轮询 tasks 表
│  ├─ ClaimNextPending (原子抢占)          │  pending → running
│  ├─ 心跳续期 (20s)                      │  多副本崩溃恢复
│  └─ 两阶段优雅停机                       │  停领取 → 等 grace → 硬取消
└──────────────┬──────────────────────────┘
               │ runner.Execute(taskID)
               ▼
┌─────────────────────────────────────────┐
│ Runner (单任务执行器)                    │
│  ├─ buildPRContext                      │  从平台 API 组装 PR 上下文
│  ├─ buildDiffIncremental                │  diff-of-diffs 增量审查
│  ├─ postStartMarker                     │  "🔍 审查中，请稍候…"（失败不阻塞）
│  ├─ 仓库级月度预算门控                   │  超限直接 failed
│  └─ run() → Workflow → Poster           │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│ Workflow (R1→R5)                        │
│  R1 需求解析 → RequirementSpec           │  PR 描述 + issue → AC 清单 + Ambiguous
│  R2 变更理解 → ChangeSummary             │  diff 公共前缀摘要（省 token）
│  R3 完成度判定 → ComplianceReport        │  逐 AC: implemented|partial|missing|unverifiable
│  R4 专家委派 → Dispatcher fan-out        │  builtin/cli/remote 插件并行
│  R5 汇总裁决 → 确定性真值表              │  不走 LLM
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│ Merger (合并器)                         │
│  ├─ severity 归一化                     │  High/HIGH → high，越界归 low + 告警
│  ├─ 轮内精确指纹去重                     │  规范化评论哈希 + file
│  ├─ 跨轮次指纹入库 + suppressed 过滤    │
│  └─ 降级注明                            │  插件失败/跳过 → 报告中注明
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│ Poster (回帖)                           │
│  ├─ staleness 门控                      │  有更新任务 → 跳过
│  ├─ CreateReview (行内 + 评审事件)       │  平台策略降级（自审→COMMENT）
│  ├─ 总结评论 (更新→扫描→新建 三级降级)  │
│  └─ review_targets 更新                  │
└─────────────────────────────────────────┘
```

---

## 四、L4 插拔层 —— 最核心的设计

### 4.1 统一契约

```go
// internal/plugin/types.go

type AgentPlugin interface {
    Manifest() AgentManifest                           // 声明"我是什么"
    Invoke(ctx context.Context, task Task) (Result, error)  // 执行"我做什么"
}
```

**设计哲学**：Task 是"能力需求清单+按需注入"——新接一个 agent 只需回答"它吃什么（Needs）、吐什么（Result）"。

```go
type Task struct {
    ID          string
    Kind        string              // requirement_compliance | code_review | visual_check
    PR          PRContext           // 平台/仓库/PR 号/分支/SHA/标签/事件类型
    Diff        *DiffContext        // 按 Needs.Diff 注入（diffprep 清洗后）
    Requirement *RequirementSpec    // 按 Needs.RequirementSpec 注入（R1 产出）
    Artifacts   map[string]string   // PreviewURL / 工作副本目录等开放位
    Budget      *TokenBudget        // 任务级 token 预算累计器（Dispatcher 注入）
}

type Result struct {
    Findings []Finding              // {file, line, severity, comment, by, suggested_fix}
    Verdict  string                 // approve|comment|request_changes（R5 不读此字段）
    Artifact map[string]string      // 插件特有结构化产物
    Notes    string
}
```

**Needs（能力需求清单）**：

```go
type Needs struct {
    Diff            bool  // 注入 diffprep 清洗后的 diff
    RepoFileRead    bool  // 允许通过工具读仓库文件
    WorkingCopy     bool  // 需要工作副本沙箱（浅克隆 + checkout PR head）
    RequirementSpec bool  // 注入 R1 产出的需求清单
    PreviewURL      bool  // 预览地址（前端视觉 agent）
}
```

**AgentManifest（插件清单）**：

```go
type AgentManifest struct {
    Name     string
    Version  string
    Kind     Kind        // builtin | cli | remote
    Protocol Protocol    // kind=remote 时: a2a | mcp
    Skills   []string    // requirement-compliance | code-review | visual-check
    Needs    Needs
    Triggers Trigger     // 路径glob / 标签 / 事件类型
    Cost     string      // fast | standard | heavy（预算裁剪顺序）
    Timeout  time.Duration
}
```

### 4.2 四类适配器（v3.0 三类 + v3.2 acpx）

| 适配器 | 包装对象 | 机制 | 代码位置 |
|---|---|---|---|
| `builtin` | 进程内 Eino ADK agent | requirement-compliance、security 等；支持 `skillRef` 注入 SKILL.md 进系统提示词；v3.3 起通用 Reviewer 可选挂工具表走 ReAct（`Workflow.ReviewTools`） | `internal/plugin/adapter_builtin/` |
| `cli` | 外部命令行（OCR） | 声明 `Needs.WorkingCopy` → 上下文服务浅克隆 → exec + JSON 解析 → Result。进程组管理：`Setpgid` 建进程组、超时先向 `-pid` 发 SIGTERM、grace 后 SIGKILL | `internal/plugin/adapter_cli/` |
| `remote` | 远端服务 | a2a：Agent Card 发现 → task 提交 → 轮询；mcp：薄封装 mark3labs/mcp-go client（v3.1 实装协议子集） | `internal/plugin/adapter_remote/` |
| `acpx`（v3.2） | CLI 编码 agent（codex/kimi/claude…） | agentkit/acpx 驱动：只读沙箱内跑 CLI agent，按 findings JSON 契约回收意见；CLI 未安装时插件自动停用不影响启动 | `internal/plugin/adapter_acpx/` |

**cli 适配器安全设计**：
- 只允许运行 Hub 配置声明的白名单可执行文件
- cli 子进程的环境变量白名单（绝不继承 `ARGUS_*` / `OPENAI_*` 等密钥）
- 工作副本内的文件永不参与任何配置层

### 4.3 Registry（注册表）

```go
// internal/plugin/registry.go

type Registry struct {
    mu    sync.RWMutex
    items map[string]*Registration
    order []string
}

type Registration struct {
    Manifest       AgentManifest
    Plugin         AgentPlugin
    LockRepoConfig bool     // §3.4 红线 3：仓库配置不可关闭
    SkillRefs      []string // 方法论注入引用
    Enabled        bool     // plugins.yaml enabled（缺省 true）
}
```

**启动时从两个来源合并**：

1. **Hub 全局配置**（`plugins.yaml`）：声明全部可用插件及连接参数
2. **业务仓库**（`.reviewagents.yaml`）：`use/disable/routing` 覆盖全局

**信任边界校验**（`ResolveRepoConfig` 函数）：

```go
func ResolveRepoConfig(rc *RepoConfig, reg *Registry) (warnings []string) {
    // 1. 未知名：忽略并告警（只能"选"不能"增"）
    // 2. disable lockRepoConfig 插件：无效并告警
    // 3. routing 只允许引用已注册插件名
}
```

**信任边界四条红线**：

| 红线 | 规则 | 原因 |
|---|---|---|
| 1 | 只从默认分支读 `.reviewagents.yaml` | 防 PR 作者在自己 PR 里 `disable: [security]` |
| 2 | 仓库配置只能"选"不能"增" | 未知名忽略；routing 不得声明新 endpoint（防 SSRF） |
| 3 | `lockRepoConfig` 保护名单 | security 类不可被仓库配置关闭 |
| 4 | 工作副本内一切文件 = 不可信数据 | 只作为 diff/内容进入提示词，永不作为配置加载 |

### 4.4 Dispatcher（确定性路由 + fan-out 执行）

```go
// internal/plugin/dispatcher.go

type Dispatcher struct {
    reg           *Registry
    breakers      *Breakers
    log           logx.Logger
    MaxConcurrent int  // 默认 2，限制同时执行的插件数
}
```

**Select（路由选择）** —— 确定性，无 LLM：

```
事件 × 仓库配置 × Triggers → 插件集合
```

选择逻辑：
1. 全局 `enabled=false` 的插件永不参与
2. Triggers 为空的插件是默认候选；有 Triggers 的仅命中时参与
3. `lockRepoConfig` 插件无视仓库配置，只要触发即参与（红线 3）
4. 仓库 `use` 非空 = 白名单（锁定插件除外）；`disable` 黑名单
5. `routing` 命中（paths/labels）追加其 `use` 引用的插件

**Run（fan-out 执行）**：

```go
func (d *Dispatcher) Run(ctx context.Context, task Task, sel []Selection, budget *TokenBudget) []Outcome {
    // 1. 并发信号量：MaxConcurrent=2（防百炼 burst rate limit 429）
    // 2. 每个插件独立 goroutine：
    //    ├─ 预算门：budget.Remaining() <= 0 且非 lock → skip
    //    ├─ 熔断门：breakers.Allow() = false → skip
    //    ├─ 超时：context.WithTimeout(timeout)
    //    ├─ safeInvoke：防插件 panic 拖垮 worker
    //    └─ 成功/失败：更新熔断器 + 指标
    // 3. wg.Wait() 等全部完成
}
```

**PruneByBudget（预算裁剪）**：

```go
func (d *Dispatcher) PruneByBudget(sel []Selection, budget *TokenBudget) (keep []Selection, pruned []string) {
    // 预留 10% 给锁定插件之外的收尾阶段
    // 按 Cost 降序裁剪非锁定插件：heavy 先裁
    // 锁定插件预算豁免：超预算继续跑 + 告警
}
```

### 4.5 熔断器

```go
// internal/plugin/breaker.go

// 状态机：
//   closed ──(3次失败)──→ open(5min冷却) ──(冷却过)──→ half-open(1次探测)
//     ↑                                                       │
//     └──────────(成功)───────────────────────────────────────┘
//                                                            │
//                              (失败) ──→ open(重新熔断)

const (
    BreakerTripThreshold = 3               // 连续 3 次失败开熔断
    BreakerCooldown      = 5 * time.Minute // 冷却期
)
```

### 4.6 Merger（合并器）

```go
// internal/plugin/merger.go

type Merger struct {
    store PrintStore  // 指纹持久化（storage.Store 实现）
    log   logx.Logger
}
```

**合并流水线**（全部确定性，不走 LLM）：

```
各插件 Result.Findings
  │
  ▼
1. Severity 归一化：High/HIGH → high，越界归 low + 告警
  │
  ▼
2. 轮内精确指纹去重：规范化评论文本哈希 + file
   ├─ 冲突 severity 取高
   ├─ 行号 ±2 容差内采纳新行号（匹配辅助，不入指纹）
   └─ by 追加来源插件
  │
  ▼
3. 跨轮次指纹入库（UpsertPrint）
   ├─ suppressed（误报反馈）→ 不进报告
   └─ open → 进报告
  │
  ▼
4. 排序：severity 降序 → file 字母序 → line 升序
  │
  ▼
MergedFindings{Findings, Suppressed, Degraded}
```

**指纹函数**：`Fingerprint(file, normalizedComment)` —— `规范化评论文本哈希 + file`，行号 ±2 容差仅作匹配辅助不入指纹。

---

## 五、L3 编排层 —— R1→R5 Workflow

```go
// internal/workflow/requirement/stages.go

type Workflow struct {
    LLM              *llm.Client
    SDK              *argusctx.SDKCtx
    Dispatcher       *plugin.Dispatcher
    Merger           *plugin.Merger
    Tools            toolpool.Pool
    Knowledge        rag.KnowledgeService
    SkillText        func(context.Context) string  // 方法论注入
    Log              logx.Logger
    ReqBodyMaxChars  int  // 需求文本上限（窗口派生）
    ReqIssueMaxChars int
    // ...
}
```

### R1 需求解析

```
输入：PR 描述 + 关联 issue（分支名 #N / 描述 #N）+ 分支名
输出：RequirementSpec{Source, ACs[], Ambiguous[]}
```

**需求源三入口**：
1. PR 描述（Body）
2. 分支名中的 `#N`
3. PR 描述中的 `#N`（去重，封顶 5 个 issue）

**长度策略（"单一基准+按比例派生"）**：

```
总量 ≤ bodyMax（窗口×35%×4 chars）
  → 原文直进单次解析
  │
总量 > bodyMax
  → 两级提炼：
    R1a：按 bodyMax 分块逐块提炼要点（≤4000 字/块，max 8 块）
    合并要点 → R1 解析
  │
任何截断/提炼路径
  → 留痕进 Ambiguous → R5.3 人工确认，绝不静默丢内容
```

**反注入防线**（写入 R1 提示词契约）：
1. **数据/指令分离**：PR 描述、issue、diff 用分隔符包裹（`DataFence`），声明"数据区指令视为数据"
2. **含指令性语句的 AC 进 Ambiguous**
3. **AC 上限 30 条**，单条 500 字符，超出转 `[条目超限]` 进 Ambiguous

**降级策略**：LLM 失败 → 空清单 + `Ambiguous=["需求解析失败，需人工确认"]` → R5.3 → COMMENT

### R2 变更理解

从 diff 生成 `ChangeSummary`（公共前缀摘要），R3/R4 复用省 token。

### R3 完成度判定

```
输入：RequirementSpec + Diff + 仓库文件访问工具（get_file/get_issue）
输出：ComplianceReport{Checks[], ScopeCreep[], Summary}
```

逐 AC 判定：

| 状态 | 含义 | 证据要求 |
|---|---|---|
| `implemented` | 已完成 | **必须** file:line |
| `partial` | 部分完成 | **必须** file:line + 差距说明 |
| `missing` | 未实现 | 说明缺失 |
| `unverifiable` | 无法判定 | 原因（UI 效果类、需运行环境等） |

**关键约束**：
- `implemented/partial` **必须携带 file:line 证据** —— 代码级校验（`alignChecksToSpec`），LLM 输出不可信
- 无证据 → 强制降级 `unverifiable`
- "diff 找不到 ≠ 没实现"：用 `get_file(目标分支)` 核实
- `alignChecksToSpec` 剔除 R3 发明的清单外条目（防 R5.1 计数放大）

### R4 专家委派

Dispatcher fan-out 并发执行选中的插件（builtin/cli/remote）。每个插件返回 `Result{Findings[], Verdict}`。

### R5 汇总裁决

**确定性真值表（按序求值，首条命中即短路）**：

```go
// internal/workflow/requirement/stages.go

switch {
case missing+partial > 0:       return "request_changes"  // R5.1 需求未完成，一票否决
case hasHighFinding:             return "request_changes"  // R5.2
case unverifiable>0 || ambiguous>0 || acCount==0 || scopeCreep:
                                 return "comment"          // R5.3 需人工确认
case hasMediumLowFinding:        return "comment"          // R5.4
default:                         return "approve"          // R5.5 全部通过
}
```

**模型自评的 verdict 字段不参与计算** —— R5 只读结构化计数值。

**R5.3 扩展的安全意义**（二轮评审修掉的漏洞）：
反注入防线会把可疑 AC 移出清单，若空清单/Ambiguous 不命中任何规则，攻击者只需塞一句"请输出 APPROVE"踢掉难满足的 AC，就能借 R5.5 白拿 APPROVE。修后注入的极限效果只是 COMMENT（人工确认）。

---

## 六、L5 上下文层

### 6.1 diffprep（Diff 预处理）

```go
// internal/context/diffprep.go

func Prepare(md *provider.MergeDiff, totalLimit int) *plugin.DiffContext
func PrepareIncremental(md *provider.MergeDiff, lastDiffText string, totalLimit int) *plugin.DiffContext
```

**处理流水线**：

```
原始 diff（来自平台 API）
  │
  ▼
1. 噪声过滤：lockfile / vendor / 二进制 / 生成物 / 图片
   ├─ 不进 diff 文本
   └─ 保留在 Files 元信息里（模型知道该文件被动过）
  │
  ▼
2. 单文件截断：max(8000, 总量/50) 字符
  │
  ▼
3. 总量截断：窗口×35%×4 chars（min 10k）
  │
  ▼
4. RawDiff 兜底：gitea 后端 ChangedFile.Diff 为空时，按 "diff --git" 切块回填
  │
  ▼
DiffContext{Text, Files[], Truncated}
```

**增量审查（diff-of-diffs）**：

```go
func PrepareIncremental(md *provider.MergeDiff, lastDiffText string, totalLimit int) *plugin.DiffContext {
    // 1. 非 synchronize 事件 → 全量审查
    // 2. 无上次审查记录 → 全量审查
    // 3. 解析上次 diff 为 per-file map
    // 4. 逐文件比对：
    //    ├─ 完全一致 → AlreadyReviewed=true，不进文本
    //    └─ 有变化 → 正常处理（截断等）
    // 5. 增量 diff 太短（<100 字符）→ 回退全量审查
    // 6. 无任何变化 → NoNewChanges=true（标记 stale，跳过 LLM）
}
```

### 6.2 LLM 客户端

> 位置变更：v3.0.3 起该层收敛进共享库 **agentkit/llm**（`git.example.com/enjoydream/agentkit/llm`），
> Argus 侧不再有 `internal/llm/`——下述机制原样保留，另新增降级链与分阶段路由（见第十四章）。

```go
// agentkit/llm（原 internal/llm/llm.go）

type Client struct {
    Model           model.BaseChatModel
    ModelName       string
    ContextTokens   int           // 模型窗口（输入+输出合计）
    MaxOutputTokens int           // 单次输出预留
    OnUsage         func(stage string, prompt, completion int)
    Budget          *TokenBudget  // 任务级预算累计器
    Limiter         *rate.Limiter // 全局限速器
    MaxRetries      int           // 默认 3
    BaseDelay       time.Duration // 默认 2s
    MaxDelay        time.Duration // 默认 30s
}
```

**Generate（带重试的普通生成）**：

```
请求
  │
  ▼
fitInput（输入自守恒）：总输入超 (窗口-输出预留)×2×0.9 时，截断最长 user 消息留痕
  │
  ▼
重试循环（最多 3 次）：
  ├─ 限速器等待（token bucket）
  ├─ 截断重试：per-request MaxTokens 1.5x 提升
  ├─ 调用 Model.Generate
  ├─ 成功：
  │   ├─ finish_reason="length" → 截断错误，继续重试
  │   └─ 正常 → account() 记账 + 返回
  └─ 失败：
      ├─ classifyLLMError：
      │   ├─ 401/403/api key → 不重试（确定性失败）
      │   ├─ context length/too long → 不重试
      │   ├─ 429/rate limit → 可重试 + 退避下限 5s
      │   ├─ finish_reason=length → 可重试 + 需提升 MaxTokens
      │   └─ 5xx/网络超时 → 可重试
      └─ 指数退避 + jitter：base=2s, ceil=30s, 429 下限 5s
```

**三道防线处理 LLM 输出超长**：

| 防线 | 机制 | 代码位置 |
|---|---|---|
| 1 | 检测 `finish_reason="length"` → 截断错误 | `Generate()` |
| 2 | 截断重试时 `model.WithMaxTokens(1.5x)` per-request 提升 | `Generate()` |
| 3 | `GenerateJSON` 截断重试追加"请精简输出"指令 | `GenerateJSON()` |

**GenerateJSON（生成并解析 JSON）**：

```go
func (c *Client) GenerateJSON(ctx context.Context, stage string, msgs []*schema.Message, out any) error {
    // 最多 2 次尝试：
    // 第 1 次：Generate → ExtractJSON → json.Unmarshal
    // 第 2 次（解析失败）：
    //   ├─ 截断导致 → 追加"请精简输出"指令
    //   └─ 其他 → 追加"错误信息 + 请重新输出合法 JSON"
    //   → 把原始输出 + 错误回喂重试
}
```

**fitInput（输入自守恒）**：

```go
func (c *Client) fitInput(msgs []*schema.Message) {
    // limit = (ContextTokens - MaxOutputTokens) * 2 * 9 / 10
    // total = 所有消息 rune 长度之和
    // 超限时：截断最长的 user 消息（复制后截断，避免原地修改污染重试）
    // 中文按 2 chars/token 保守估算（4 会低估超窗）
}
```

### 6.3 知识子系统（RAG）

> 位置变更：已收敛进共享库 **agentkit/knowledge/rag**；Argus 侧 `main.go` 只做模式装配。

```go
// agentkit/knowledge/rag

type KnowledgeService interface {
    // 模式一：预检索（确定性）—— workflow 阶段在拼 prompt 前取 topK 片段
    Retrieve(ctx context.Context, query string, topK int, filter Filter) ([]Chunk, error)
    // 模式二：工具化（自主）—— 包成 search_knowledge(query) 挂进 ReAct agent
    AsTool() tool.BaseTool
}
```

**两种实现，启动时按配置选择**：
- **本地模式**（`rag.NewLocal`，缺省）：knowledge/ 目录放 markdown，启动时索引
  （词元重叠打分，按 `# / ##` 标题分块）；目录不存在回退 Noop，不阻塞启动；
- **Milvus 向量模式**（v3.0.4 集成，`ARGUS_MILVUS_ADDR` 非空启用）：OpenAI 兼容
  Embedder（`ARGUS_EMBEDDING_*`）+ Milvus Store；启动时把 knowledge/ 目录索引进
  向量库，Milvus 不可达自动回退本地模式。

**知识飞轮（v3.3）**：人工 suppress 的误报结论在 Milvus 模式下向量化回流知识库——
R3 `search_knowledge` 命中历史误报即带抑制语境，同型误报率随使用下降。

### 6.4 Skill 方法论注入

> 位置变更：已收敛进共享库 **agentkit/skill**（`skill.NewFileProvider`）。

```go
type Provider interface {
    Resolve(ctx context.Context, ref Ref) (*Skill, error)
}

type Ref struct {
    Name    string
    Version string
    Source  string  // file | git | http | registry
}
```

**注入方式**：SKILL.md 内容拼进系统提示词尾部（带 checksum，防内容漂移难排查）。

**10 分钟周期刷新**：改 skill 文件后无需重启，下一个任务自动加载新内容。

```go
// runner.skillText()
// mutex 保护 → 10 分钟周期刷新 → 用 Background ctx（本地文件读取，不需要任务级取消）
```

---

## 七、并发模型与可靠性

### 7.1 DB 即队列

```go
// internal/worker/worker.go

type Pool struct {
    Store *storage.Store
    Run   func(ctx context.Context, taskID string) error
    N     int  // worker 数量（ARGUS_MAX_CONCURRENT_TASKS，默认 2）
}
```

**核心流程**：

```
webhook/TaskAPI → 落库(pending) → 返回（不 spawn goroutine）
                        │
          N 个 worker 轮询 ──→ ClaimNextPending(pending→running) → Execute
```

- `ClaimNextPending`：原子抢占（SQLite 单写串行天然安全，MySQL 用 `SELECT ... FOR UPDATE SKIP LOCKED`）
- 空转轮询：500ms 间隔
- 失败日志：runner 正常情况下自行终结任务，此处兜底

### 7.2 Staleness（过期判定）

```go
// poster.Post() 中的第一步

stale, err := p.Store.HasNewerNonFailedTask(ctx, platform, owner, repo, number, taskID, createdAt)
```

**单条件判定**：存在同 PR 更晚创建的非 failed 任务 ⇒ 本任务 stale

- findings/指纹照常入库
- **不更新总结评论、不覆盖 review_targets**
- 数据全来自 tasks 表（head_sha/created_at），不依赖 review_targets

**设计意图**：SHA2/SHA3 连续 push 时，慢的旧任务后完成不得覆盖新结果——PR Bot 经典 bug。

### 7.3 两阶段优雅停机

```go
func (p *Pool) Stop(grace time.Duration) {
    p.once.Do(func() {
        // 阶段一：取消领取循环（不再拉新任务）
        p.cancelLoop()

        select {
        case <-p.done:      // 在途任务已全部自然完成
        case <-time.After(grace):
            // 阶段二：硬取消执行上下文
            p.cancelRun()
            // 任务保持 running（不写终态），依赖心跳过期重置回 pending
        }
    })
}
```

**资源关闭顺序**（ekit bootstrap）：

```
注册序：[store, worker, http]
关闭逆序：http 先停 → worker 排水 60s → store 最后关
```

### 7.4 心跳与崩溃恢复

```go
const (
    HeartbeatInterval = 20 * time.Second
    StaleRunningAfter = 90 * time.Second  // ≥3 个间隔的安全余量
)
```

**heartbeatLoop**（独立于 loopCtx）：
1. 每 20s 续一次本实例 running 任务心跳（`TouchRunningHeartbeats`）
2. 重置心跳已死的 running 任务回 pending（`ResetRunningToPending`）
3. 启动时也会执行一次重置（main.go 中 `store.ResetRunningToPending`）

**多副本安全**：RunnerID = uuid[:16]，心跳按 RunnerID 区分归属。

---

## 八、存储设计

```go
// internal/storage/iface.go

type TaskStore interface {
    GetTask(ctx context.Context, id string) (*Task, error)
    CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error)
    FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string, tokensUsed int, summaryCommentID int64) error
    ClaimNextPending(ctx context.Context) (*Task, bool, error)
    ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error)
    TaskCounts(ctx context.Context) (pending, running int64, err error)
    PruneTasks(ctx context.Context, cutoff time.Time) (int64, error)
    ListTasks(ctx context.Context, status string, limit int) ([]Task, error)
    SuppressPrint(ctx context.Context, ...) (matched bool, err error)
    MonthlyTokensByRepo(ctx context.Context, since time.Time) ([]RepoTokenUsage, error)
    // ...
}
```

**七张核心表（tasks/finding_prints/review_targets/qa_sessions/leader_leases/pattern_signals/print_aliases）**：

| 表 | 字段 | 解决什么 |
|---|---|---|
| `tasks` | id, event_id(唯一索引), platform, owner, repo, number, head_sha, status, kind, verdict, report, result_json, fail_reason, tokens_used, summary_comment_id, created_at, updated_at, heartbeat_at | 异步任务持久化 + webhook 幂等 + 同 SHA 去重 + 心跳 |
| `finding_prints` | platform, owner, repo, number, fingerprint(联合唯一), status(open/suppressed), severity, line, times_seen, regret_count, task_id | Merger 指纹跨轮次持久化 + 误报反馈 + regret 误报历史（R3.5 剔除计数，Merger 跨轮标注可信度） |
| `review_targets` | platform, owner, repo, number(联合唯一), last_summary_comment_id, last_head_sha | 总结评论更新定位 + 同 SHA 去重查询源 |

**幂等键 = 平台 delivery ID**（不是 SDK 的 `NormalizedEvent.ID`，后者每次接收重新生成）：
- Handler 在 `ParseWebhookEvent` **之前**从原始请求头取 delivery（`X-Gitea-Delivery` / `X-GitHub-Delivery` / `X-Gitlab-Event-UUID`）
- header 缺失则 event_id 置 NULL（唯一索引允许多个 NULL，置空字符串会被第二条缺 header 的 webhook 误判 duplicate）

**Status 词表钉死**：`pending / running / done / failed / stale`

**自动迁移**：GORM AutoMigrate，SQLite 单文件起步，dialect 可切 MySQL（DSN 即可）。

---

## 九、回帖系统

```go
// internal/poster/poster.go

type Poster struct {
    SDK         *argusctx.SDKCtx
    Store       *storage.Store
    Version     string
    BotUsername string
    Log         logx.Logger
}
```

### 回帖流程

```
1. staleness 门控
   └─ HasNewerNonFailedTask → stale → 跳过（findings/指纹已入库）
  │
  ▼
2. 评审记录（CreateReview）
   ├─ verdict 映射：request_changes→REQUEST_CHANGES, comment→COMMENT, approve→APPROVE
   ├─ 行内 findings：file 必须在 diff 文件列表内（预校验，防 LLM 幻觉文件导致 422）
   ├─ 评审幂等锚点：发前按 task_id 查 ListReviews（防崩溃重跑重复）
   └─ 平台策略降级：PR 作者自审 → 自动降级 COMMENT → 再降级无行内意见评审
  │
  ▼
3. 总结评论（三级降级）
   ├─ ① review_targets.last_summary_comment_id → UpdateIssueComment
   ├─ ② 扫描既有评论的 HTML 注释头（严格正则 + 作者校验）
   └─ ③ 新建 CreateIssueComment
  │
  ▼
4. review_targets 更新（last_summary_comment_id + last_head_sha）
```

### 报告模板

```markdown
<!-- argus:report task_id={id} head={sha} ver={version} -->
## 📋 Argus 需求复核报告（需求源: {issue#N|PR描述}）

### 验收清单完成度: {n}/{N} {✅|⚠️|⛔}
- {✅|⚠️|❌|❓} **AC-1** {描述} — {状态}（{证据}）

### ⚠️ 需求外改动（scope creep）
- {file}: {说明}

### 🔍 代码审查意见
- `{file}:{line}` **[HIGH]** {comment} — by {plugin}

### 结论: {⛔ 需求未完成 | ⚠️ 需人工确认 | 💬 有意见 | ✅ 通过}
> task `{task_id}` · 插件: {name@ver,...} · 耗时 {t}s · tokens {n}
```

---

## 十、可观测性

### 结构化日志

每条带任务上下文字段：

```
task_id, event_id, platform, owner/repo#number, stage(R1..R5|plugin:<name>), duration_ms, tokens
```

### Prometheus 指标

| 指标 | 标签 | 用途 |
|---|---|---|
| `argus_tasks_total` | platform, kind, status | 吞吐与失败率 |
| `argus_stage_duration_seconds` | stage | R1-R5 耗时分布 |
| `argus_plugin_duration_seconds` | plugin | 插件健康（熔断依据） |
| `argus_plugin_failures_total` | plugin | 插件失败计数 |
| `argus_llm_tokens_total` | stage, model | 成本核算 |
| `argus_findings_total` | severity, plugin | 审查产出分布 |
| `argus_tasks_pending` | - | 队列深度 |
| `argus_tasks_running` | - | 执行中任务数 |

### 健康检查

| 端点 | 用途 |
|---|---|
| `GET /healthz` | 存活探针（Hertz 无默认，自写一行） |
| `GET /readyz` | 就绪探针：DB 连通 + LLM 端点可达（30s TTL 缓存） |
| `GET /metrics` | Prometheus（ekit metrics.Registry） |

### 审计锚点

报告评论带 HTML 注释头 `<!-- argus:report task_id=... head=... ver=... -->`（人不可见），即使 DB 记录损坏也能靠扫描评论定位更新目标。

---

## 十一、配置派生哲学

**核心思想**：模型窗口是**唯一基准**，全部上限按比例派生，防止"换了 1M 模型服务仍按 128k 截断"的配置漂移。

```go
// internal/config/config.go

func (c *Config) contextTokens() int {
    if c.LLM.ContextToken > 0 { return c.LLM.ContextToken }
    return 128_000  // 防未配置时按小窗口裸奔
}

func (c *Config) MaxDiffCharsResolved() int {
    // 显式 env 优先；缺省 = 窗口 35% 折算字符，min 10k
    // 无硬顶——窗口变大 diff 上限跟着变大
}

func (c *Config) EffectiveTaskTokenBudget() int {
    // 显式 env 优先；缺省 = 窗口 60%
}

func (c *Config) MaxTokensResolved() int {
    // 显式 env 优先；缺省 = max(4096, 窗口 5%)
}
```

**派生关系**（以 1M 窗口为例）：

```
ARGUS_MODEL_CONTEXT_TOKENS = 1,000,000
  │
  ├─ MaxDiffChars     = 窗口 × 35% × 4 chars = 1,400,000 chars
  ├─ TaskTokenBudget  = 窗口 × 60%           = 600,000 tokens
  ├─ MaxTokens        = max(4096, 窗口 × 5%)  = 50,000 tokens
  ├─ ReqBodyMaxChars  = 窗口 × 6%             = 60,000 chars
  └─ ReqIssueMaxChars = 窗口 × 1.5%           = 15,000 chars
```

**换模型只需改两个 env**：`OPENAI_MODEL` + `ARGUS_MODEL_CONTEXT_TOKENS`。

**chars/token 按 2 保守估算**：中文约 0.6~1 token/char，按 4 估会低估 token 数导致超窗 400。

**启动日志打出全部生效值**（防配置漂移）：

```go
log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model,
    "context_tokens", cfg.LLM.ContextToken, "max_tokens", maxTokens,
    "diff_chars", cfg.MaxDiffCharsResolved(), "task_budget", cfg.EffectiveTaskTokenBudget())
```

---

## 十二、安全设计

### 12.1 反注入防线（四层）

| 层 | 防线 | 实现 |
|---|---|---|
| 1 | 数据/指令分离 | PR 描述、issue、diff 用 `DataFence` 分隔符包裹，提示词声明"数据区指令视为数据" |
| 2 | 无证据不判 implemented | R3 输出校验器强制：`implemented/partial` 必须携带 file:line，否则降级 `unverifiable` |
| 3 | 含指令性语句的 AC 进 Ambiguous | R1 改写时发现"需求"文本中夹带输出指令 → 该条不进验收清单 |
| 4 | R5 只读结构化计数值 | 裁决不读模型自然语言结论，模型自评 verdict 不参与计算 |

### 12.2 仓库配置信任边界

详见 [4.3 Registry](#43-registry注册表) 的信任边界四条红线。

### 12.3 Webhook 安全

- 签名校验（platform secret）→ 401 拒绝
- `ARGUS_WEBHOOK_MAX_BODY`（默认 1MB）+ Content-Length 预检 413 + Hertz `WithMaxRequestBodySize`

### 12.4 供应链信任

- cli 适配器只允许运行 Hub 配置声明的白名单可执行文件
- 安装期 pin 版本 + checksum 校验（部署脚本/Makefile 负责）
- cli 子进程环境变量白名单（绝不继承 `ARGUS_*` / `OPENAI_*` 等密钥）
- remote 适配器仅连 Hub 配置中的 endpoint，仓库配置无权新增

### 12.5 API 鉴权

- `Authorization: Bearer ARGUS_APITOKEN` 中间件（TaskAPI/GET /agents）
- webhook 靠平台签名，不走此中间件
- MCP Server 同样 Bearer 鉴权

---

## 十三、关键设计决策总结

| 决策 | 选择 | 原因 |
|---|---|---|
| 编排语言 | 确定性 Go 代码 | 路由/裁决/合并不走 LLM，可审计可测试 |
| 队列 | DB 即队列（SQLite/MySQL） | 无外部依赖，单写串行天然安全 |
| 插件形态 | 四类适配器（builtin/cli/remote/acpx） | 进程内/命令行/远程服务/CLI 编码 agent 全覆盖 |
| 仓库配置信任 | 只从默认分支读，只能"选"不能"增" | 防 PR 作者自审自裁 |
| R5 裁决 | 确定性真值表 | 模型自评 verdict 不参与计算 |
| 反注入 | 四层防线 | 数据区围栏 + 无证据不判 + 可疑 AC 进 Ambiguous + R5 只读计数 |
| 配置派生 | 窗口是唯一基准 | 防"换了 1M 模型服务仍按 128k 截断" |
| 熔断 | 3 次失败 → 5 分钟冷却 → 半开探测 | 防插件故障拖垮整体 |
| 增量审查 | diff-of-diffs | synchronize 事件只审新变更 |
| 回帖降级 | 更新→扫描→新建 三级降级 | review_targets 损坏也能自愈 |
| 停机 | 两阶段（停领取 → 等 grace → 硬取消） | 避免把跑到 90% 的审查硬杀后重跑 |
| 心跳 | 20s 续期 + 90s 过期重置 | 多副本崩溃恢复 |
| Token 记账 | 优先读响应 usage，缺省 chars/4 估算 | 兼容不同 LLM 提供商 |
| 限速 | token bucket（golang.org/x/time/rate） | 防 burst rate limit 429 |
| 指纹去重 | 规范化评论哈希 + file | 行号 ±2 容差仅作匹配辅助不入指纹 |
| 幂等键 | 平台 delivery ID（非 SDK event ID） | SDK 的 ID 每次接收重新生成 |

---

## 十四、v3.1–v3.4 架构演进

前十三章描述的是 v3.0.5 基线骨架——这套骨架（L1 门面 / L3 确定性编排 / L4 插拔 / L5 上下文）
至今未变，演进全部发生在骨架之上。以下按版本梳理增量，细节以 [CHANGELOG](../CHANGELOG.md) 为准。

### 14.1 v3.1——协议面铺开

骨架不动，L1/L4 各加几个适配面：

- **Argus as MCP Server**（`internal/channel/mcpserver`）：`/mcp` streamable http + Bearer，
  任意 MCP 客户端可发起审查/取报告——L2 门面的又一协议适配，验证了"形态是 L1 适配器"铁律；
- **查询面补齐**：`GET /reports/:ref`（按 head SHA 查最近成功报告，CI 门禁联动）、
  `GET /usage/monthly`（仓库月度 token 用量）、`GET /events`（SSE 进度流）；
- **remote 插件协议子集实装** + **cli `delegate` 模式**（OCR 只出筛选规则，审查循环由
  builtin agent 自己跑）；
- **GitHub App 认证**（替代 PAT）；
- **1M 上下文窗口单一基准**：diff 上限/任务预算/输出上限全部从 `ARGUS_MODEL_CONTEXT_TOKENS`
  派生，防"换了 1M 模型服务仍按 128k 截断"。

### 14.2 v3.2——acpx：CLI 编码 agent 进编

`kind: acpx` 第四类适配器：把 codex/kimi/mimo/claude 等 CLI 编码 agent 当 R4 审查专家——
agentkit/acpx 在只读沙箱里跑 CLI，按 findings JSON 契约回收意见；CLI 未安装时插件自动停用，
不影响启动。这让"外部最强模型"无需服务化即可进编队。

### 14.3 v3.3——agentkit v0.7.x 七项能力迭代

| # | 能力 | 架构落点 |
|---|---|---|
| P0 | 成本记账 | obsx OnUsage 回收全部 eino 调用真实 usage（Generate 直连 + ReAct RawModel 两路径）；任务级+全局 CostTracker；报告元信息"成本 ≈ $X" |
| P1 | 多副本横向扩展 | `leader_leases` 表租约选主（条件 UPDATE 原子"判-占-续"）门控 poller；workcopy 按实例隔离 `WorkRoot/instance-<id>/`；前置条件 MySQL |
| P2 | Reviewer ReAct 化 | builtin Reviewer 可选挂工具表（复用 R3 工具基础设施），agentrun 驱动，失败落回单轮 GenerateJSON |
| P3 | 交叉审查编队 | plugins.yaml `squads:` 段：paths/labels 触发成员追加进分派；Merger 对同文件 ±5 行"仅一方报告"意见标注分歧 |
| P4 | 知识飞轮 | suppress 成功 → 误报结论回流 Milvus 知识库 → R3 检索命中带抑制语境（闭环） |
| P5 | Q&A ReAct + ask_argus | `@argus ask` 升级 ReAct（get_diff/get_file/search_knowledge）；`qa_sessions` 表多轮会话（一 PR 一会话，6 轮 FIFO + 24h 过期）；mcpserver 第五工具 `ask_argus` |
| P6 | 分阶段模型路由 | agentkit `llm.StageRouter`：`ARGUS_MODEL_{R1,R2,R3,R35,ROUTER}` / `ARGUS_REVIEWER_MODEL` / `ARGUS_QA_MODEL` 按阶段覆盖模型与窗口——大窗口吃 R1 长输入、快模型跑 R2、强模型保 R3 |

### 14.4 v3.4——agentkit v0.8.0 三原语落地 + 一个"不接"

- **R3.5 报告反思（reflection）**：Merger 合并去重后、R5 裁决前，终审编辑按误报特征 Rubric
  （文件越界/行号非法/纯风格/无证据定位/重复/测试代码误判）走"生成→批判→修订收敛"循环；
  只删/修不增（按 id 回写，编造 id 丢弃；逐字段校验回退），解析失败/模型故障保留原 findings
  ——审查不因反思故障而失败。默认关闭（`ARGUS_REFLECTION_ROUNDS` 1~3）。
  剔除的指纹由 runner 入库 `finding_prints.regret_count`（误报历史）；Merger 跨轮读取，
  对复现（`times_seen>=2`）与高 regret（`regret_count>=2`）的意见在报告 comment 上标注
  可信度——只标注不自动 suppress，持久抑制仍只来自人工反馈通道。
- **评论意图路由（router）**：`@argus` 自由文本先分类再分派——review（要求重审）→ 建全量
  复审任务并回执任务号；其余一切回落 Q&A。短命令关键词零分类直达，歧义长句才走一次快模型
  分类；低置信（<0.6）视为 ask。默认关闭（`ARGUS_COMMENT_ROUTER=1`，重审烧预算需显式 opt-in）。
- **编队黑板共识（blackboard）**：`squads.mode: consensus`——成员不进 fan-out 盲审，改在
  共享黑板按轮互看，只补充他人未覆盖的新问题，一整轮无人补充即收敛（上限 2 轮）。收敛发现
  按成员归属构造独立 Outcome 进 Merger，去重/抑制/跨轮合并管线全复用。默认模式（互盲独立）不变。
- **明确不接 Plan-and-Execute**：P&E 适用前提是"步骤可预规划、计划本身有价值"；R1→R5 已是
  固化确定性编排，在其上叠 P&E 是杀鸡用牛刀——架构纪律优先于能力覆盖。

### 14.5 演进中的纪律（为什么骨架没动）

四个版本的增量全部符合 v3.0 定下的两条铁律：**形态是 L1 适配器**（MCP Server、SSE、
GitHub App 都是门面新适配面）、**agent 是 L4 插件**（acpx、编队、反思都是插件/编排层的
新成员，不改变调度与裁决语义）；R5 真值表始终只读结构化计数值，新增能力（反思/共识/路由）
全部默认关闭或保守启用——行为可预期、可回退。

### 14.6 v3.5——review 闭环：证据门 + 模式级后悔学习环

v3.4.1 的 regret 只到**单指纹标注**，R3.5 仍是「批改」而非「无证据不得发表」。  
完整设计见 [review-closed-loop-design.md](review-closed-loop-design.md)。

- **负控自验证门**：R3.5 输出协议增加 `evidence`/`counterexample`，Go 侧确定性后验
  （`ARGUS_REFLECTION_EVIDENCE=off|soft|strict`，默认 off 行为不变）；
  soft 缺证据强制降 low，strict 剔除并记 regret。
- **模式级后悔学习环**：`pattern_signals`（repo×plugin×pattern_key）累计
  posted/regret/suppress/restore，Beta 置信度驱动 Merger 标注、**仅 low** 条目
  HardFloor 闸门、R4 提示词注入——**不自动 suppress**，持久抑制仍只来自人工反馈。

默认全关、opt-in；实现细节与测试计划见设计文档 §7/§6。

**v3.5.1 补齐活检与 dream**：`argus_layer_exec_total{layer}` 钉死 9 层打点，
告警抓「有成功任务但某层零执行」；`internal/dream` 周期去重 `pattern_signals`
并把低置信模式导出到 Milvus / knowledge 目录（`ARGUS_DREAM_INTERVAL`）。

---

## 附录：环境变量全表

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ARGUS_ADDR` | `:8899` | 监听地址 |
| `ARGUS_DB_DSN` | `argus.db` | SQLite 文件；集群部署切 MySQL DSN |
| `ARGUS_APITOKEN` | - | TaskAPI/GET /agents 的 Bearer token |
| `ARGUS_BOT_USERNAME` | - | bot 账号名（自审保护 + 扫描兜底校验） |
| `ARGUS_SKIP_DRAFT` | `true` | 默认不审 draft PR |
| `ARGUS_MAX_CONCURRENT_TASKS` | `2` | worker 数量 |
| `ARGUS_MAX_DIFF_CHARS` | 派生 | diff 总量上限（显式 env 覆盖公式） |
| `ARGUS_TASK_TOKEN_BUDGET` | 派生 | 任务 token 预算 |
| `ARGUS_REPORT_META` | `true` | 报告元信息行 |
| `ARGUS_PLUGINS` | `plugins.yaml` | 全局插件声明 |
| `ARGUS_MODEL_CONTEXT_TOKENS` | `128000` | 模型上下文窗口（唯一派生基准） |
| `OPENAI_MAX_TOKENS` | 派生 | 单次生成上限 |
| `OPENAI_BASE_URL` | - | LLM 端点 |
| `OPENAI_API_KEY` | - | LLM 密钥 |
| `OPENAI_MODEL` | - | 模型名 |
| `ARGUS_GITEA_URL/TOKEN/SECRET` | - | Gitea/Forgejo 平台 |
| `ARGUS_GITHUB_TOKEN/SECRET` | - | GitHub 平台 |
| `ARGUS_GITHUB_APP_ID/PRIVATE_KEY/INSTALLATION_ID` | - | GitHub App 认证 |
| `ARGUS_GITLAB_URL/TOKEN/SECRET` | - | GitLab 平台 |
| `ARGUS_LLM_RATE_LIMIT` | `0` | 全局 LLM 请求速率限制（rps） |
| `ARGUS_LLM_RATE_BURST` | =MaxConcurrent | 限速器突发容量 |
| `ARGUS_WEBHOOK_MAX_BODY` | `1048576` | Webhook 请求体上限（字节） |
| `ARGUS_TASK_RETENTION_DAYS` | `0` | 任务保留天数（0=永久） |
| `ARGUS_REPO_MONTHLY_TOKEN_BUDGET` | `0` | 仓库月度 token 预算 |
| `ARGUS_POLL_INTERVAL` | `0` | 轮询间隔（0=关闭） |
| `ARGUS_POLL_REPOS` | - | 轮询目标（platform:owner/repo） |
| `ARGUS_KNOWLEDGE_DIR` | `knowledge` | RAG 知识库目录（Milvus 模式下启动时索引进向量库） |
| `ARGUS_SKILLS_DIR` | - | Skill 文件目录 |
| `ARGUS_WORK_ROOT` | `/tmp/argus-work` | 工作副本沙箱根目录（多副本按 `instance-<id>/` 隔离） |
| `ARGUS_DEBUG` | `false` | 留存阶段产物 |
| `ARGUS_MILVUS_ADDR` / `ARGUS_MILVUS_COLLECTION` | - / `agentkit_knowledge` | Milvus 向量知识库（非空启用；失败回退本地） |
| `ARGUS_EMBEDDING_BASE_URL/API_KEY/MODEL/DIM` | - | Embedding 端点（OpenAI 兼容，Milvus 模式必配） |
| `ARGUS_LLM_COST_PROMPT_PER_1K` / `_COMPLETION_PER_1K` | `0` | 成本费率（$/1k token，报告"成本 ≈ $X"） |
| `ARGUS_MODEL_R1/R2/R3/R35/ROUTER` | - | 阶段模型覆盖（P6；`_CONTEXT_TOKENS` 后缀配各阶段窗口） |
| `ARGUS_REVIEWER_MODEL` / `ARGUS_QA_MODEL` | - | Reviewer / Q&A 专用模型出口 |
| `ARGUS_REFLECTION_ROUNDS` | `0`（关闭） | R3.5 报告反思轮数（1~3，>3 钳制） |
| `ARGUS_REFLECTION_EVIDENCE` | `off` | 证据门：`off\|soft\|strict`（v3.5） |
| `ARGUS_REGRET_LEARNING` | `0`（关闭） | 模式级后悔学习环总开关（v3.5） |
| `ARGUS_REGRET_MIN_SAMPLES/SOFT_FLOOR/HARD_FLOOR` | `3` / `0.35` / `0.20` | 模式置信度消费阈值 |
| `ARGUS_REGRET_INJECT_FLOOR/TOP_N` | `0.35` / `10` | R4 历史误报提示词注入 |
| `ARGUS_DREAM_INTERVAL` | `0`（关闭） | dream 整合周期（Go duration；需学习环开启） |
| `ARGUS_COMMENT_ROUTER` | `false` | 评论意图路由（review=重审任务 / ask=问答） |
| `ARGUS_ROUTER_MIN_CONFIDENCE` | `0.6` | 意图分类置信度下限，低于回落 ask |
| `ARGUS_OCTOPUS_URL/CHANNEL_ID/USER/TOKEN` | - | octopus IM 推送 |

---

> 本文档基于 Argus v3.0.5 源码梳理（2026-09-08 修订：收敛位置修正 + 第十四章补 v3.1–v3.4
> 演进），覆盖从架构分层、数据流、插拔机制、编排逻辑、并发模型到配置哲学的完整设计。
