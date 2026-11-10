# Argus 架构地图

> 三份文档的分工：**design.md** 是设计契约（§ 引用的出处）；**docs/architecture-deep-dive.md** 是历史演进分析；
> 本文件是**当前实现分层事实**——与 [test/arch](test/arch/arch_test.go) 可执行守卫互为镜像：文档给人读，
> 测试给门禁跑，两者不一致时以能跑的为准，并修正另一方。

## 分层总览

```text
                     ┌─────────────────────────────┐
                     │  main (argus)               │  进程生命周期：配置/信号/停机 + HTTP 接线
                     └──────┬──────────────────────┘
                            │
  ┌─────────────────────────▼───────────────────────────┐
  │ L1 组合根  bootstrap                                │  装配一切、不被任何人 import（组合根单向性）
  │  Assemble(对象图) + StartSubsystems(后台子系统)     │  选主/worker 池/运维 ticker/dream/health/poller
  └──┬───────┬───────┬───────┬───────┬───────┬─────────┘
     │       │       │       │       │       └────────────┐
  ┌──▼────┐ ┌▼─────┐ ┌▼─────┐ ┌▼─────┐ ┌▼──────────┐ ┌──▼──────────┐
  │ runner │ │service│ │poller│ │dream │ │ channel/  │ │ worker      │  L2 编排 / 入口
  │        │ │       │ │      │ │      │ │ mcpserver │ │ (异步消费)  │
  └──┬─────┘ └┬──────┘ └┬─────┘ └┬─────┘ └┬──────────┘ └──┬──────────┘
     │        │         │        │        │★零 internal   │
     │        │         │        │        │ 依赖(窄接口)  │
  ┌──▼────────▼─────────▼────────▼────────▼───────────────▼──────────┐
  │ L3 能力层                                                        │
  │  workflow/requirement(R1-R5 编排)   poster(回帖)   qa(问答)      │
  │  argusctx(sdkctx*/diffprep/githubapp)  storage(持久化,gorm)       │
  │  reportview(渲染) toolpool(mcp) sastscan sink notify progress    │
  │  ocrdeleg/ocrshim/gitdeepen(OCR 链) health(体检) memorykernel    │
  └──┬───────────────────────────────────────────────────────────────┘
     │
  ┌──▼───────────────────────────────────────────────────────────────┐
  │ L4 核心域  plugin                                                 │  契约单源：Task/Outcome/Registry/
  │  + domain(域类型) learning(学习模型)                             │  Hook* 词表/manifest 全在这里
  └──┬───────────────────────────────────────────────────────────────┘
     │
  ┌──▼───────────────────────────────────────────────────────────────┐
  │ L0 纯底座  config(配置) obs(指标)            │  横切件：config→learning、obs→plugin（钩子接线）
  └──────────────────────────────────────────────────────────────────┘
```

## 依赖规则（机器可读快照：`test/arch/arch_test.go` 的 `layering` 白名单）

| 层 | 包 | 允许的 internal 依赖 | 禁止 |
|---|---|---|---|
| L0 纯底座 | codesearch / semsearch / learning / progress / gitdeepen / reposettings | （无） | 一切业务包 |
| L0 纯底座 | goedges | semsearch（NodeKey 键契约单源） | 其余 |
| 影响面 | impactview | gitdeepen, plugin, semsearch, storage | 编排/入口（包装接线在 runner） |
| 域类型 | domain | learning | — |
| 引擎面 | dispatch | domain, learning, plugin | runner/service/poster/workflow…（一切上层） |
| L4 核心域 | plugin | domain | runner/service/poster/workflow…（一切上层） |
| 横切 | config | learning | 业务包 |
| 横切 | obs | plugin（钩子接线：指标实现注入 `plugin.Hook*`，L4 无 prometheus 依赖） | 其余 |
| 横切 | notify | notify/octopus, obs | 业务包 |
| 自有协议 | notify/octopus | （无） | 一切 |
| L3 能力 | storage | domain, learning | plugin/runner/service…（数据层不反向依赖域） |
| L3 能力 | reportview | dispatch, plugin | 其余（纯渲染，无 IO） |
| L3 能力 | argusctx | config, plugin | runner/service/workflow/storage… |
| L3 能力 | qa | codesearch, argusctx, plugin, semsearch, storage | 编排层互依 |
| L3 能力 | dream | learning, memorykernel, plugin, storage | 编排层互依 |
| L3 能力 | poster | argusctx, dispatch, plugin, reportview, storage | 编排层互依 |
| L3 能力 | poller | argusctx, domain, obs, progress, storage | 编排层互依 |
| L3 能力 | worker | storage | 编排层互依 |
| L3 能力 | health | learning | 编排层互依 |
| L3 能力 | toolpool / sink | plugin | 编排层互依 |
| L3 能力 | sastscan | dispatch, plugin | 编排层互依 |
| L3 能力 | ocrdeleg | gitdeepen, plugin | 编排层互依 |
| L3 能力 | ocrshim | gitdeepen, plugin, plugin/adapter_cli | 编排层互依 |
| L3 能力 | fixverifier | plugin | 编排层互依 |
| L3 能力 | commentcmd / memorykernel / writing | （无） | 编排层互依 |
| L2 编排 | workflow/requirement | codesearch, argusctx, dispatch, domain, obs, plugin, semsearch, toolpool | runner/service/storage/poster |
| L2 编排 | runner | 能力层全集（config, argusctx, dispatch, domain, fixverifier, impactview, learning, memorykernel, notify, ocrdeleg, plugin, poster, progress, reposettings, reportview, sastscan, semsearch, sink, storage, toolpool, workflow/requirement, writing） | service/bootstrap/channel |
| L2 编排 | service | commentcmd, domain, config, argusctx, health, dispatch, impactview, memorykernel, obs, plugin, progress, qa, reposettings, semsearch, storage, writing | runner/workflow/poster/bootstrap |
| L1 组合根 | bootstrap | 一切 | **被任何包 import** |
| 入口适配 | channel/mcpserver | （无，零 internal 依赖） | service/runner（靠 Core/Copilot/ReviewQA 消费侧窄接口注入） |
| 适配器 | plugin/adapter_{acpx,builtin} | dispatch, plugin | 反向依赖（plugin 不得 import 适配器） |
| 适配器 | plugin/adapter_{cli,remote} | plugin | 反向依赖（plugin 不得 import 适配器） |

**变更流程**：新增/调整任何 internal 依赖 → 必须先改 `test/arch` 白名单并跑 `make arch`
→ CHANGELOG 记档 → 同步本表。测试失败即"架构变更未显式化"。

## 架构不变量（评审时的硬检查项）

1. **契约单源**：plugin.Task 八字段输入契约、WorkflowOutput、Verdict/Layer 词表、
   Hook* 观测钩子、AgentManifest——只定义在 plugin，跨包引用不得克隆。
2. **消费侧窄接口**：跨层依赖一律"谁消费谁定义"（plugin.WorkflowRunner、
   mcpserver.Core、PrintStore→domain.PrintStore）。接口的实现数 >1 才配接口；
   单实现且无测试替身用途的用具体类型。
3. **插件适配器单向**：adapter_* → plugin 单向；新形态适配器（如 sandboxed CLI）
   落 plugin/adapter_* 子包，不进核心域。
4. **组合根单点**：一切对象图组装在 bootstrap（Assemble/BuildModels/BuildRegistry/
   StartSubsystems），main 只剩进程生命周期（v3.63.0 闭环）；任何包不得反向
   import bootstrap。
5. **观测旁路**：L4 纯逻辑不依赖 prometheus——业务包调 `plugin.Report*`（nil 安全），
   obs 装配期接线实现；指标词表（stage/reason label）与告警规则对齐，改值断 Grafana。
6. **数据防线上移**：半可信输入（diff/issue/模型输出）进提示词必须过 DataFence，
   输出面必须过 fence.EscapeUntrusted——单一出处，禁止本地转义克隆。

## 守卫

```bash
make test    # 全量测试（-race，与 CI test job 同参）
make lint    # 静态三合一：vet + staticcheck + gofmt（与 CI lint job 同参）
make arch    # 分层白名单 + 组合根单向性 + 包文档完整性（CI arch job）
make eval    # golden 编排语义回归
```

命令口径单源在 Makefile；CI 只编排 job 与矩阵环境。本地四连过 = 流水线绿。
