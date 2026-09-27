# argus 高内聚低耦合靶向治理设计（v3.45.0–v3.49.0）

日期：2026-09-27
状态：已按批实施（每批一提交，门禁：make lint + make test + make eval + make e2e）

## 0. 勘察结论（三路并行深查，2026-09-27）

对 internal 全部 27 包做了依赖图测量（fan-in/fan-out/LOC）与语义勘察。总体判断：

**分层纪律健康，无需推倒**：依赖方向是干净 DAG（service 不 import runner/workflow；
workflow 不碰 config/storage；plugin 不 import storage/obs）；装配单点（bootstrap.Assemble
→ System）；dispatcher/merger/R5 真值表是高质量确定性引擎；worker/channel/sink/notify/
poller 是干净的防腐层；除 obs→plugin 观测钩子与 biz 全局 Svc 外无全局可变单例。

**fan-in 冠军 plugin（22 包依赖）的真相**：不是行为上帝包——引擎面（Dispatcher/Merger）
只有 runner 与 workflow/requirement 两个消费者；高 fan-in 的推手是「领域类型的 canonical
归属」+「解环杂物抽屉」。问题在归属，不在行为。

真实坏味道归为四类（全部有 file:line 实锤）：

### A. 纪律口子（正确性风险）
1. `runner.go` 预算耗尽路径与 stale 判定路径用 `_ = r.Store.FinishTask(...)` 把
   fenced 标志与错误全部丢弃——实施时核实 `FinishTask` 本身已委托
   `FinishTaskFenced`（store.go:540），fencing 无缺口；真实问题是这两条路径的
   僵尸写入/DB 错误**静默无痕**，与 :384 主失败路径的观测口径不一致。修复 =
   统一为 fenced 感知写法（观测修复，非行为变更）。
2. `service.go` import gorm，两处 `errors.Is(err, gorm.ErrRecordNotFound)` 判 404
   ——全仓唯一的 ORM 错误语义泄漏点。
3. `store.RunnerID` 为空时 `TouchRunningHeartbeats` 静默 no-op（store.go:297）、
   fencing 退化为只校验 status——装配疏漏会静默降级全部防线。修复 = 心跳路径
   显式报错拒绝执行（Open 签名不动：单测桩依赖空值例外分支）。
4. `main.go:118/136` 两次 ResetRunningToPending（90s 窗与 30s 窗各一次），30s 窗
   是 90s 窗的超集且更晚执行，第一次完全被覆盖，冗余易误读。修复 = 删第一次。

### B. 隐式契约与多轨词表（改一处要动四处的根源）
5. 阶段名三轨制：`plugin.LayerR1..R5` 常量（hooks.go）与裸串 `"R1"`（stages.go 指标
   与 LLM stage 标签、service.TaskStages 自带词表、bootstrap/models.go map 键）并存，
   缺 R35 常量；新增阶段要同步 4 处。
6. debug 裸键 `"r1_spec"/"r2_summary"/"r3_raw"/"r3_checks"`（workflow.go）被
   `service.TaskStages` 当跨包契约消费——workflow 改键或 debug 关闭即静默失真。
7. `plugin.LearningConfig` 字面量在 assemble.go / main.go 三处重复构造——阈值字段
   变更需同步三处，漂移风险。
8. runner 既持整坨 `*config.Config` 深取 33 处，又拷贝一份 learning/reflection 字段
   ——同一配置两份事实来源。

### C. 归属错位（plugin 抽屉化）
9. `pattern.go`（学习环数学 PatternKey/Confidence/PatternSamples）寄居 plugin，
   消费方 storage/dream/health/exportdata 全是反馈域，无一与插件语义相关；
   `LearningConfig/PatternState` 同属此域。
10. reminder/fence/sanitize 等 prompt 卫生原语寄居 plugin——**裁定保留**：它们服务
    适配器的 agent prompt 管线（提醒信封注入 system prompt、数据栅栏保护注入内容），
    与 plugin 的适配器域语义连续，迁移收益不抵 import 面扰动。文档化即可。
11. ReviewSettings/DescribeSettings 寄居 plugin——**裁定保留**：ResolveRepoConfig
    信任边界在此，拆出会动四方且无行为收益。文档化即可。

### D. 编排根过胖（runner fan-out 15，单文件 1279 行）
12. ocrdeleg(11 符号)/codegraph/sastscan/memory 四个「dispatch 前置 enrich 步骤」
    以直接 import + 内联调用散在 runner.go:1023-1140，约 120 行。
13. 学习环记账（regret/pattern）裸调 storage 约 12 方法，无消费侧窄接口（对照
    merger 有 PrintStore/PatternStore 先例）。
14. `requirement.Workflow` 八个公有字段由 runner 事前赋值、bootstrap ReviewTools
    钩子再写 RepoGuide——输入通道三分（Run 参数/事前字段/钩子），全靠约定。
15. service 装配分裂在 main.go:330-335（System 不完整）；runnerRef 延迟绑定闭包
    无装配后防护（assemble.go:122-145）。

## 1. 目标架构（锚定现状的收口，不是重排）

三条不变量：
- 依赖 DAG 与分层方向不变；config 保持叶子位；plugin 不 import storage/obs。
- 确定性引擎（dispatcher/merger/bestofn/squad_consensus/R5 真值表）零行为变更。
- 对外契约零变更：MCP 工具面、webhook 语义、sink payload、DB schema、指标标签值。

四个收口方向：
1. **契约显式化**：阶段词表与 debug 键单源化，service↔workflow 隐式契约改常量消费。
2. **归属归位**：学习环域独立成 `internal/learning` 叶子包，配置单源。
3. **依赖倒置到组合根**：enrich 前置步骤接口化，runner 面向窄接口，构造移 bootstrap。
4. **装配收口**：System 成为完整对象图，时序契约加启动期断言。

批次划分（每批独立可回滚，golden 68 条全绿为闸）：

| 批次 | 版本 | 内容 | 触碰面 |
|---|---|---|---|
| B1 | v3.45.0 | 纪律口子：fenced 观测统一、storage.ErrNotFound、心跳 fail-fast、复位去重 | runner/storage/service/main |
| B2 | v3.46.0 | 契约单源化：阶段词表统一（含 R35、LLM stage 标签族）、debug 裸键常量化 | plugin/workflow/runner/service/bootstrap |
| B3 | v3.47.0 | learning 归位：internal/learning 叶子包（pattern.go+LearningConfig+PatternState），LearningConfig 单源化 | plugin/storage/dream/health/exportdata/bootstrap/main |
| B4' | v3.48.0 | 装配收口：service 进 System、runnerRef 不变量自检（B4 原方案改判不做，见 §2） | bootstrap/main |

## 2. 关键裁定记录

- **runner enrich 步骤接口化：改判不做（实施复核，v3.48.0 在案防重探）**。读实现后
  实锤：injectOCRDelegate/injectSAST/injectCodeGraph/reviewHints 是组合点上的内聚
  胶水——统一的「config 闸门 + 工作副本池取还 + logredact 告警降级」模式，且
  injectOCRDelegateInDir 已有独立测试缝。倒置的两种落法都更糟：让工具包实现
  EnrichStep 则 ocrdeleg/sastscan/codegraph 反向依赖 workflow 包（工具叶子的依赖面
  变宽）；把实现搬 bootstrap 则组合根膨胀 ~200 行运行期逻辑（破坏装配根单一职责）。
- **runner 学习环记账窄接口：不做**。runner 对 storage 实际调用面 14 个方法，横跨
  任务生命周期（GetTask/FinishTaskFenced/SetTaskHeadSHA/MergeTaskStageArtifacts/
  SetTaskDebug/RepoMonthlyTokens）、学习环记账（RecordPrintRegret/GetPrintState/
  PatternRegret/ListLowConfidencePatterns）、评审锚定（GetLastReviewedDiff）三类——
  为它建接口是 header-interface 反模式；merger 两方法窄接口的先例不可平移。
- **prompt 卫生三件（reminder/fence/sanitize）不迁**：与适配器 prompt 管线语义连续，
  迁移是纯 import 扰动。在 plugin 包文档注释中写明归属理由，防后人误判。
- **runner 的 notify/sink/progress 接口化不批**：三个都是单调用 fire-and-forget 依赖，
  接口化只加间接层不降真实耦合；真正的胖点是 enrich 步骤（B4）。
- **requirement.Workflow 八字段 Options 化不批（本轮）**：动 runner+bootstrap+harness
  三方契约，收益是形式美感；当前三通道由 golden harness 钉死语义，回归风险大于收益。
  留待下次触碰该结构时顺手做（写在此处防遗忘）。
- **storage.TaskStore 僵尸接口：删除**（修正初稿「保留改注释」的裁定）：全仓
  grep（含 biz/test/e2e）证实零消费者，接口注释声称的四个使用方（runner/service/
  poster/worker）实际全部持具体 `*storage.Store`；本项目既有纪律是 deadcode 全清，
  保留死抽象比删除更伤内聚。`*Store` 的真实解耦由消费侧窄接口承担
  （plugin.PrintStore/PatternStore、dream.Store、health.Store、worker.LeaseStore）。
- **config 整坨注入不批（本轮）**：runner 33 处深取的收窄需要 RunOptions 大改，
  与 B3 的 LearningConfig 单源是同一问题的两个切片；先落单源（B3），整体收窄留待
  下次功能性触碰 runner 配置面时一并做。

## 3. 验收口径

每批：`make lint`（staticcheck+gofmt）+ `make test`（全量单测）+ `make eval`
（golden 68 条打桩回归）+ `make e2e`（双打桩场景集）全绿方可提交。
指标标签值（layer/stage/kind）在 B2 中保持字节级不变，防 Grafana 断线。

## 4. 五轮：生产实测驱动的增量优化（v3.49.0–v3.50.0，2026-09-27）

背景：v3.48.0-215 上线后对真实 MR（obs/demo-insights-agent#144）做了全链路
生产验证（见部署记录），暴露两个设计内实况，本轮转化为优化：

- **v3.49.0 code_review R2 checkpoint 补齐**：RunReviewOnly（code_review/
  visual_check）此前直接调 SummarizeChange，不读写 checkpoint——任务重试时
  R2 每次全量重跑（实测 ~5k tokens/次）。抽 `summaryWithCheckpoint` 统一入口
  （命中复用零 LLM / 未命中调 LLM 成功即落库 / 失败降级不落库），Run 与
  RunReviewOnly 共用，消重复 + 补语义。复用语义与 Run 一致：重试时 runner 从
  DB 重载 StageArtifacts 进 Artifacts。单测覆盖三路径（未命中落库/命中复用/
  LLM 失败降级）。
- **v3.50.0 comment 链路 Bus 事件对齐**：HandleComment 回帖路径此前无任何
  Bus 事件，SSE 侧只见任务不见问答（勘察在案的观测口径分裂）。新增
  `comment.replied` 事件（status ok/error），挂 Q&A/意图路由共用出口
  postCommentReply 与错误出口 postQAError；事件类型消费方无过滤逻辑，
  纯增量安全。
- **复核定案不改**：ctxfit 校准单槽（代码注释有辩护记录"多模型分桶待有真实
  需求再开"，无真实错配证据不动）；指纹措辞漂移（设计 §3.2 既定权衡：
  键可复算优先于跨措辞收敛）。
