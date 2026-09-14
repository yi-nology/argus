# Changelog

## v3.6.1 (2026-09-15) — 半开探测时限按插件超时自适应（agentkit v0.10.1）+ runner 单测补课

### 半开探测并发双探测修复（深挖 P0 最后一项工程遗留）
- agentkit v0.10.1：`breaker.NewBreakers` 增加变参 `Option` 与 `WithProbeTimeout`
  （既有调用方零改动）——半开探测时限此前硬编码 1min，小于插件超时（600s）时，
  探测在途 1min 后即被并发放行第二个探测，慢而健康的插件被并发双跑
- argus `NewDispatcher` 按注册表最长插件超时 + 30s 余量设置探测时限（无注册表
  信息时回退缺省）；`Breaker.Allow/Success/Failure` 语义不变，回归测试
  `TestWithProbeTimeout` 覆盖时限内不放行/超时放行/零值回退/缺省时钟源就位
- 至此深挖清单的"agentkit 侧待修"仅剩指纹算法设计一项（涉 finding_prints 存量
  迁移，需独立决策）

### runner 单测补课（深挖工程事实收尾）
- `buildWorkflow` Client 回退装配：限速器/窗口接线断言（限速器修复的接线回归）、
  无 R3 覆盖时 R3ChatModel 留空语义、StageLLMs → StageRouter 装配
- `skillText` 惰性解析 + 10 分钟缓存 + checksum 头（§9.1）

## v3.6.0 (2026-09-15) — 深挖还债二批：证据门语义化、失败静默面清零、存储与观测加固

与 v3.5.9–v3.5.13 并行的另一实施线（两会话分头认领同一深挖清单，本条覆盖以下文件）。
`go vet` 干净，全部单测 + e2e 21 场景通过。

### 防线语义化（requirement）
- **证据门交叉校验** `EnforceEvidenceScoped`：证据引用的路径必须落在变更文件集内，
  编造的 `src/fabricated.go:1`（格式合法）降级 unverifiable；R3 重试路径同门
- **R3.5 strict 证据门**要求 evidence 至少含一个路径样 token，纯文字证据不再放行；
  修复 `extractPathLike` 对 `a.go:12`（无斜杠、带行号单 token）的漏识别
- **R3.5 反思输入包围栏**（reflection.go）：findings 线格式/变更文件清单/变更摘要
  三块全部过 DataFence——此前注入可经插件 comment 驱使终审编辑清空 findings
- **注入正则审批域豁免**：命中段自身含「审批/审核/会签通过」视为业务描述放行，
  审批类 AC 不再误伤进 Ambiguous→R5.3；真注入照常拦截
- **AC 同 ID 去重**（alignChecksToSpec）：保留更保守状态（missing>partial>…），
  detail 并入，防重复状态扭曲 R5.1 与报告双计

### LLM 链路降级纪律（requirement）
- **R1 提炼块级回退**：digestSegment 单块失败只回退该块（截到块长），不再整段
  塞回 8× 预算原文击穿两级提炼的控量目标
- **R1 接入 llmjson 宽容解析**：直调 Generate + Unmarshal（R3/R3.5 已迁移），
  全角标点/尾逗号不再把整条 PR 打成解析降级→R5.3
- **R2 失败降级继续**：摘要置空 + Degraded 注明，不再是全链路唯一单点失败
- **ReAct 迭代上限随清单派生**（12+超10条每条+1，封顶 24）；重试前查任务预算，
  预算耗尽跳过重试（防 token×2 重跑）；task.Budget 提前注入

### 插件系统（plugin）
- **熔断不记任务级取消**：teardown(err, parent) 区分 ctx 取消/终止与插件自身故障
  （含测试）；信号量等待可取消；MaxConcurrent 接入 `ARGUS_PLUGIN_MAX_CONCURRENT`
- **共识三态**（squad_consensus）：全员无法贡献显式记 `consensus_stalled`（非收敛）；
  跑过后失败的成员按 Err 回填不再静默（含测试）
- **交叉标注拆 By**：Merge 拼接的 `"a,b"` 拆开参与 replicated 判定，合并条目
  （专家意见一致的证据）不再被误标「另一专家未复现」；多成员归属即视为已复现
- **围栏中和留痕**：DataFence 中和发生即计数（注入攻击信号观测面）
- **插件跳过原因指标**（circuit/budget/canceled）
- WithSquads 静态校验：未知 mode / paths+labels 双空 / 无成员编队启动期告警

### 装配与配置（bootstrap/config）
- **plugins.yaml 解析失败 fail-fast**：区分「文件不存在」（回退默认）与「存在但
  坏 YAML」（启动报错）；单次加载共用（沿用 v3.5.9 收敛点）
- **cli delegate 模式启动期停用**（运行期必报错，不再注册成可用插件白烧熔断）
- **remote protocol 白名单**（a2a|mcp），拼错启动即停用注明
- **部分配置交叉告警** `Config.WarnOnly`：Milvus 半配（缺 embedding 三件套）、
  fallback 三缺一——静默降级显式留痕（含测试）

### 存储与运行时（storage/poster/qa/mcpserver/main）
- **ListTasks 列裁剪**：列表接口不再拉 report/result_json/debug_json LONGTEXT
  （保留 artifacts_json 元数据），百倍 IO 削减
- **SQLite 连接池 1→4**（WAL 读写并发）：大查询/大 DELETE 不再队头阻塞心跳读，
  误重置双跑窗口收窄；幂等/去重正确性来自唯一索引与事务，不依赖连接数=1
- **ClaimNextPending**：MySQL 走 `FOR UPDATE SKIP LOCKED` 单语句取+占，败者
  重试取下一条而非空转 500ms
- **PruneTasks 分批删除**（每批 500，防长事务占住写连接）
- **保留策略扩面**：qa_sessions（7d/retention）与 finding_prints（last_seen 180d/3×
  retention）周期清理，此前单调膨胀
- **poster ① 级 404 自愈**：缓存评论被删（IsNotFound）清定位直走扫描/新建，
  不再每轮撞一次失败更新；新增 `ClearReviewTargetComment`（Upsert 的 >0 才写
  语义清不掉）
- **Q&A 乐观锁** `SaveQASessionCas`：并发评论丢轮次防护 + 冲突重读重放；
  GetQASession 错误不再静默
- **mcpserver ask_argus 必填校验**：漏参报参数错误而非伪装「无审查记录」
- **extractQuestion 切片对齐**：先 TrimSpace 原文再取偏移，前导空白不再切出
  乱码/切进多字节字符
- **HTTP 显式超时**：ReadTimeout 30s / IdleTimeout 120s（不设写超时保护 SSE 流）

### 观测（obs）
- 新增 6 指标：`argus_verdicts_total{platform,kind,verdict,rule}`、
  `argus_fence_neutralized_total`、`argus_plugin_skipped_total{reason}`、
  `argus_comment_route_total{route,via}`、`argus_task_create_deduped_total{reason}`、
  `argus_sse_connections`

### 杂项修复
- dream：Milvus 成功同样写本地 markdown 副本（降级载体不再陈旧）
- sdkctx：CreateNote 的 noteID 解析失败留痕（ID=0 会破坏幂等更新锚点）
- R2 提示词 300 字 → 1200 字（与 stages 截断常量对齐）
- e2e：TriggerEventsFilter 场景重号 Scenario10→22；stack_test 注释与部署镜像
  tag（compose/k8s v3.0.1→v3.6.0）对齐；README 补 `ARGUS_PLUGIN_MAX_CONCURRENT`

### 评估后不采纳（留痕勿翻案）
- merger 位置合并加语义相似闸门：与 v3.5.x 拍板并单测固化的「同位置不同措辞=
  同一问题」归并语义冲突，不采用
- dream 去重键改全量 hash：前缀聚类是有测试固化的刻意近重复设计，收紧会使其
  变成死代码；仅修本地副本陈旧问题

## v3.5.13 (2026-09-14) — 收尾：深挖清单"agentkit 侧"两项实为已修 + README 版本口径修正 + poster 单测补课

### 盘点更正：熔断两项已在 argus 侧修复（清单归属标注有误）
- 深挖清单"agentkit 侧待修"中的两项实际在 `internal/plugin/dispatcher.go` 且已带
  注释与测试修复：任务级取消/终止（teardown）不计熔断连续失败（滚动发布不再误熔
  断 lock 插件，`TestRun_CanceledNotCountedAsFailure`）；等待执行槽位可取消（任务
  终止后排队 goroutine 不再傻等）。真正的 agentkit 侧遗留仅剩两项：半开探测窗口
  （breaker.probeTimeout 硬编码 1min，未暴露 Option，< 600s 插件超时会并发双探测）
  与指纹算法设计（不含行号 + 逐字敏感双向失灵，改动涉 finding_prints 存量迁移）——
  均需独立立项，暂不动

### README 版本口径漂移修正（深挖工程事实）
- "当前版本 v3.5.5（agentkit v0.9.5）" → v3.5.12（agentkit v0.9.8），并注明版本
  口径以 CHANGELOG 头为准；两处"仓库 HEAD 已 v3.4.0"过时陈述更新，补各版本段
  一句话索引

### poster 单测补课（深挖工程事实"runner/poster/bootstrap 零单测"）
- `inlineCommentBody`：严重级大写、不可信评语中的报告头标记中和（防劫持）、
  suggestion 围栏渲染/含 ``` 退化/超长省略
- `reportHeaderRe` 严格匹配：字段齐全且独占一行，行中前缀/尾随文本/缺 ver 不命中
  （总结评论更新目标的防伪边界）

## v3.5.12 (2026-09-14) — 深挖 P0 修复批次：假 APPROVE 双守门 / acpx 假成功 / 评论异步化 / 双跑防线 / 证据行号（8 项）

四路深挖清单（~40 条）的 P0 实锤逐项落地。对照当前代码盘点：#1（R3.5 反思输入
DataFence）已由并行会话修复、#6（plugins.yaml 双装载吞错）已随 v3.5.9 单次加载
修复，其余 8 项本批次完成。vet 干净，全部单测 + e2e 通过（场景 20 适配轮询断言）。

### #2 零插件产出禁入 APPROVE（R5.0 守门）
- skills 词表不匹配/插件全被禁用/全数熔断跳过时，空 findings 是"没人审"而非
  "没问题"——此前直落 R5.5 静默 APPROVE，未审查变更放进主干
- finish 统一守门：ran==0 → Degraded 注明"未审查状态下给出" + APPROVE 强制降级
  COMMENT（rule=R5.0）；RunFull/RunReviewOnly 双路径覆盖

### #5 大 PR 截断透出
- diffprep 的 Truncated 标记此前全仓零消费，审查覆盖不完整无任何痕迹；
  finish 统一在 Degraded 注明"仅覆盖保留部分"

### #3 acpx 输出不可解析计熔断（不再假成功）
- parseFindings 返回 (findings, error)：完全不可解析 = 失败上抛 → dispatcher 记
  breaker 失败 + Outcome 降级注明；合法空数组 = 真零 findings 照常成功
- 此前解析失败返回 nil findings 记 Success——坏 agent 永不熔断、每轮假装干净

### #7 webhook event_id 落穿幻影 task
- store 在唯一冲突时回查返回 ("event_id", 既有ID)，但 service 只判了
  last_head_sha/in_flight 两个分支——并发窗口内的重复投递照常返回未入库的新
  task.ID（幻影）+ 发幽灵 SSE 事件。补 event_id → duplicate 分支

### #8 评论处理异步化
- HandleComment（ReAct 可达数分钟）原同步跑在 webhook handler：平台 10s 断连
  取消 ctx 杀掉 ReAct，平台重试又撞 30s 节流 → 用户永无回复
- 改 async.GoSafe + context.Background（与请求生命周期解耦；delivery 幂等与
  per-PR 节流在 HandleComment 内部，重复投递安全）；场景 20 断言改轮询式

### #9 重审关键词疑问守门
- "这个重新审查的逻辑对吗"（11 rune、无否定词）原会命中"重新审查"直接全量
  重审烧 token；新增疑问标记守门（吗/？/?/为什么/怎么/怎样/如何/什么/难道），
  问句交 LLM 意图分类裁决；补 3 个测试用例

### #4 get_file 输出带行号
- 证据门要求 file:line 定位，而无行号的原文等于逼模型编造行号；R3 工具表与
  Q&A 工具表的 get_file 统一逐行加 4 位宽行号前缀（行号|内容，先于截断），
  工具描述同步声明；附 numberLines 单测

### #10 多副本同 SHA 双跑防线
- 事务内 in-flight 检查是单实例纪律，租约过期窗口内两副本可同时读到 0 →
  双任务双烧 token。最终防线落库：
  - SQLite：status IN (pending,running) 部分唯一索引（不挡 TaskAPI 手工对 done
    任务的同 SHA 重审）；历史在飞重复致建索引失败 → 告警退化，收敛后重启自愈
  - MySQL：无 partial index，CreateTaskForWebhook 事务内对 review_targets 行
    FOR UPDATE 串行化（首审 PR 无锚行的残余竞窗仅影响 PR 首审，可接受）
  - 唯一冲突幂等回查：event_id → 在飞（in_flight）→ 最近历史（last_head_sha）
- 附 TestSameSHAGuard（首建/在飞去重/索引冲突/done 豁免/幂等）

## v3.5.11 (2026-09-14) — llmjson 沉淀 agentkit v0.9.8：宽容解析入口上收共享库（无行为变化）

`internal/llmjson`（v3.5.3 引入的三级宽容解析入口，架构评判认定的正确抽象）按
bianque 同款沉淀惯例上收 `agentkit/llmjson`（agentkit v0.9.8，已推 origin），argus
改为直接消费、内部包删除。语义逐行原样：`Unmarshal` 三级尝试（ExtractJSON 快路径
→ jsonrepair.Repair 语法修复 → ParseLenient 全链兜底），全败错误携带严格/宽容两路
原因供回喂重试。消费点五处随迁：builtin/acpx/remote 三适配器 + requirement
R3/R3.5/反思修订；`go vet` 干净，全部单测 + e2e 通过。

## v3.5.9 (2026-09-14) — 深入修复：全局限速器覆盖主链 + 插件清单单次加载 + Artifacts 键常量化

### 修复：ARGUS_LLM_RATE_LIMIT 未覆盖主模型链（限速形同虚设的缺口）
- 历史行为：全局限速器只挂到"非降级路径的阶段覆盖 Client"上——主 Client（无降级
  配置）与主/阶段 Resilient 降级链都没挂，配置了限速时占大头的 R1-R5 主链调用完全
  不受限，与配置注释"LLM 调用前阻塞等待"的语义不符
- 现在四条路径统一到达：主 Client、主 Resilient 链、阶段覆盖 Client（原有）、
  阶段覆盖 Resilient 链（agentkit llm.Resilient/Client 均有导出 Limiter 字段，
  WithBudget 预算注入副本自动继承）
- 未配置 ARGUS_LLM_RATE_LIMIT 时行为零变化（limiter 为 nil）；已知边界：ReAct
  RawModel 旁路（ADK 直调 BaseChatModel）不经 Client/Resilient，仍不受限——
  需 agentkit 侧支持，另行立项
- 回归测试 TestBuildModelsLimiterWiring：降级配置下主链/阶段链 Resilient 均挂
  limiter、非降级配置主 Client 挂 limiter

### 改进：plugins.yaml 单次加载
- 原 BuildRegistry 与 Assemble 的 MCP 工具表段各调一次 LoadPluginsFile（重复解析
  + 告警可能双发）；收敛为 bootstrap.loadPluginsFile 统一加载一次，registry 与
  MCP/编队段共用
- BuildRegistry 新增 Deps.Plugins 预加载入口（nil = 自行加载，独立调用方如 e2e
  保持原语义）；缺文件回退内置默认的行为不变

### 卫生：Task.Artifacts / Result.Artifact 键常量化
- plugin 包新增 ArtifactChangeSummary / ArtifactComplianceReport / ArtifactRule /
  ArtifactTrigger，替换 7 处 "change_summary" 字面量（workflow 写入、builtin/remote
  适配器与编队共识读取、compliance 产物透传）及 comment_router 的 trigger 元数据键
  ——stringly-typed 键名拼错编译器不再沉默

## v3.5.8 (2026-09-14) — 装配统一：模型解析器收敛 P6 组合，main 回归纯生命周期（无行为变化）

架构还债清单的最后一项（装配分裂）：原 main.go `runApp` 560 行把 LLM 主模型、
P6 六类阶段覆盖、failover 包装、知识子系统、插件/runner 装配与进程生命周期混在
一起，与 bootstrap 形成两处装配点。`go vet` 干净，全部单测 + test/e2e 通过。

### bootstrap.BuildModels：模型栈单一解析点（models.go）
- `Models` 结构一次解析全部角色出口：React/Generate/Fallback、R1/R2（StageLLMs）、
  R3/Reflect/QA/Router（直调出口）、ReviewerGen/ReviewerReact——原 6 个分散的
  模型覆盖字段（R3Model/FallbackModel/ReflectModel/ReactModel/ReviewerLLM/StageLLMs）
  的组合逻辑收敛为一个具名抽象，P6 环境变量族有了唯一归属
- 不变量代码化：Generator 路径走 Resilient/Client；所有 BaseChatModel 直调出口
  统一包 FailoverModel（Resilient.RawModel 只返回主模型，ReAct 旁路靠 Failover
  补齐主备切换）；阶段覆盖继承主 BaseURL/APIKey
- 构造失败返回 error（原 panic 语义等价：bootstrap.Run → 进程退出）；
  未配置 OPENAI_* 不是错误（空栈 + Ready()==false）
- 新增单测：全角色解析 / 无 LLM 空栈 / 最小配置（无降级无覆盖，ReviewerReact
  与 React 同源）/ 限速器注入 / ptrNonNeg

### bootstrap.Assemble：装配产物 System（assemble.go）
- SDK/工作副本（按实例隔离）/技能 → 模型栈 → 知识子系统（Milvus > 本地 RAG >
  Noop 三级装配，fallbackLocalRAG/indexKnowledgeDir 自 main 迁入）→ 总线/推送 →
  插件注册（runner 延迟解引用环收在装配内部）→ MCP 工具表 → 成本记账 →
  dispatcher/merger/poster/runner
- `System` 是纯共享引用集合，不做任何启停；生命周期（资源注册、选主、worker
  排水、ticker、poller、HTTP）仍归 main
- bootstrap.Deps 的 WorkflowFactory/ReviewTools 字段类型切到 plugin 契约
  （v3.5.7 别名的落地使用）

### main.go：777 → 307 行
- 只剩：配置校验 → ekit bootstrap（信号/停机）→ 存储开关与 instanceID →
  Assemble → 资源注册（store/poll-leader/worker/http 逆序关闭）→ worker/ticker/
  poller 启停 → service+HTTP 接线 → readyz/metrics/MCP 路由
- 装配期日志 key 全部原样保留（argus.boot.llm_* / stage_model / milvus_* 等），
  运维观测不受影响；已知历史行为原样保留（限速器仅作用于非降级路径的阶段覆盖
  Client，与主 Client 不一致——待后续单独立项修）

## v3.5.7 (2026-09-14) — 架构维护：契约上提消依赖倒刺 + Q&A 拆包 + 工程卫生（无行为变化）

架构评审（分层/依赖方向/门面职责）落地为结构维护，纯移动/等价重构：`go vet` 干净，
全部单测 + test/e2e 通过（发布版与本地 SDK 双配置各验一轮）。

### 契约上提（消三处依赖方向倒刺）
- 任务 kind 词表 canonical 定义自 `workflow/requirement` 上提 `plugin.TaskKind*`
  （taskkind.go）——service/poller 不再为拿常量反向依赖 L3 编排层；requirement 保留
  `Kind*` 别名兼容包内引用
- `WorkflowOutput`（原 requirement.Output）、`WorkflowRunner`/`WorkflowFactory`/
  `ToolsHook`/`ReflectSummary` 上提 `plugin`（contracts.go）——runner 装配 factory 时
  不再 import 具体适配器 adapter_builtin；新旧两处均保留类型别名，既有调用方零改动
- `DataFence` 上提 `plugin.DataFence`（fence.go）——提示词注入防线的统一实现由
  workflow / builtin 插件 / Q&A 三方共用，canonical 归属插件契约层；
  adapter_builtin 随之不再依赖 workflow/requirement

### Q&A 拆包（service 回归薄门面）
- PR 评论问答引擎（ReAct + 单轮降级 + 多轮会话 + 报告追问，约 300 行）自 service
  拆出 `internal/qa`——门面层不再持有 LLM 编排逻辑，eino schema/toolutils 与
  agentrun/llm/toolprior 等导入随之离开门面层
- Service 保留 `LatestReview`/`AnswerAboutReview` 委托方法（mcpserver 接口兼容）；
  节流与 delivery 幂等仍留在 HandleComment；`renderHistory` 截断测试随迁 internal/qa

### 工程卫生
- bootstrap 默认插件配置不再硬编码个人机器路径（`/Users/demo-user/bin/ocr` → 按 PATH
  解析 `ocr`；LookPath 失败 → 插件停用且 `/agents` 可观测，与原缺失语义一致）
- .gitignore 补 `/argus` 本地二进制与 `*.db.bak-*` 数据库备份
- go.mod 的 `replace => ../agentkit`、`../../git-platform-sdk` 刻意保留：试点依赖
  本地 SDK 补丁（见 v3.5.6），移除会使无本地检出的环境构建到未打补丁的发布版；
  待补丁上游化后再切 go.work

## v3.5.6 (2026-09-14) — git.example.com (Forgejo) 真机复验：gitea 后端四缺陷修复 + 合并器同位置归并 + TaskAPI 幂等

真实试点复验中发现并当场修复验证（PR#11/#13/#14 全链路：轮询 → 审查 → 官方 REQUEST_CHANGES → 报告原地更新）。
SDK 侧修复在 `git-platform-sdk` 分支 `fix/gitea-pilot-findings`（argus go.mod replace 指向本地检出）。

### 修复：git-platform-sdk gitea 后端（五个，均有实机证据）
- `ListCRs` state 过滤值 `"opened"` 不被 Forgejo 认可（未知值返回**全部**状态）→
  已关闭 PR 被轮询器重新审查；新增 `mapListState` 归一为 open/closed
- `convertPR` 丢弃 `pr.Draft` → `ChangeRequest.Draft` 恒 false，draft 跳过在轮询与
  webhook 两条路径全部失效（实测 draft PR 被完整审查）；补映射
- `AllPages` 无上限 × Forgejo issue-comments 列表忽略 `page` 参数（page=1/2 返回同一
  内容，curl 实证）→ poster 定位旧报告时**无限分页循环、worker 永久卡死**。注意
  v3.5.5 的 `bound()` 兜底对此无效：循环内每个请求都成功返回，ctx 取消传递不进
  gitea SDK；50 页上限是唯一有效兜底
- `clientRoundTripper` 直连 `Transport.RoundTrip` 绕过 `http.Client.Timeout`（30s
  默认值在该路径为死代码，gitea SDK 方法签名不带 ctx）→ 服务器停摆时永久阻塞
  （SIGQUIT 栈实证 13-16 分钟悬空）；请求 ctx 超时封顶 + ResponseHeaderTimeout
- `GetFileContent` 404 无法被 `provider.IsNotFound` 识别（gitea SDK 把 API 错误压平
  成纯 message 字符串，状态码只留在随错误返回的 *Response 上）→ 可选文件缺失时
  观测面出现虚假失败告警；从 *Response.StatusCode 分类修复（实机验证：
  `.reviewagents.yaml` 缺失时 `loaded=false` 且零告警）

### 改进：argus 侧防线
- poller：非 `opened` 状态的 CR 不建任务（平台 state 过滤失灵的兜底）
- poster：报告定位扫描步 60s 超时 + 失败 WARN 留痕，降级新建评论而非卡死队列
- TaskAPI 幂等创建（`CreateTaskDirectIfIdle`）：同 PR 已有 pending/running 任务时
  返回既有任务（`argus.api.task_create_deduped`），杜绝手工触发与轮询竞态双审；
  done 后可重触发，强制重审语义保留
- Merger 同位置归并：不同专家对同一 `file:line` 的不同措辞发现归并为一条（severity
  取高、by 并列、意见拼接去重）——实机 PR#11 correctness/security 各报一条相同的
  切片越界，旧逻辑按 file+comment 精确指纹拦不住（单测覆盖，实机待下轮复验）

### 真机实测结论（Forgejo，修复后）
- 轮询首扫：draft 跳过、closed 零重建，仅 open PR 建任务
- PR#13 需求复核：正确关联 issue #12，AC-1 implemented/AC-2,3 missing 均带 file:line
  证据；推送夹带文件后重审，scope creep 段准确列出 `utils/misc.go`（此前漏检系测试
  素材 `git commit -am` 未暂存新文件，非程序缺陷）
- 官方 blocking 评审 `official:true` ×2；push 新提交 20s 建任务、92s 完成审查，
  总结评论**原地更新**（同 comment id，head 换新、发现并入、零重复评论）

## v3.5.5 (2026-09-14) — GitHub 真机实测：SDK 调用兜底超时 + issue_comment Actor/Repo 补齐

两个缺陷均在 GitHub 真机实测中发现并当场修复验证（私有仓库 argus-smoke-gh 全链路：
TaskAPI 审查 → 评论区 @ 触发重审 → Q&A → 节流 → 原地更新，全部实测通过）。

### 修复：SDK 调用无兜底超时（poster/worker 可永久挂起）
- 弱网/VPN 下 TCP 连接可能停滞且不触发传输层错误——上一轮 git.example.com 实测中
  poster 的总结评论更新因此在无超时的 HTTP 调用上无限等待（LLM 长推理同理）
- `SDKCtx` 全部 11 个平台委托方法统一经 `bound()` 兜底：入参 ctx 无 deadline 时限时
  `ARGUS_SDK_TIMEOUT`（默认 120s；负值关闭；已有 deadline 的 ctx 原样透传）
- 单测 `TestSDKCtxBoundTimeout`：挂死 provider 50ms 内超时返回 / 外部 deadline 优先 /
  负值关闭三语义

### 修复：GitHub issue_comment 缺 Actor/Repo（评论链路静默早退）
- SDK 的 GitHub 后端对 issue_comment 走 `default` 分支只置 Type，**不填 Actor/Repo**；
  HandleComment 在 `ev.Repo == nil` 处静默返回——GitHub 评论路径此前对真实 webhook
  从未生效（e2e fake provider 自填 Repo 掩盖了缺口；Forgejo 侧因 `pull_request.url`
  缺失同样不可用，见 v3.5.4）
- 新增 `normalizeCommentEvent`：以原始负载为准补齐（repository.full_name/owner+name →
  Repo；sender.login/user.username/login → Actor；已有值不覆盖），评论处理与 SDK
  归一化程度解耦
- 单测 `TestNormalizeCommentEvent`：full_name 切分 / 显式字段优先 / 不覆盖已有值 /
  user.username 兜底 / 空负载不 panic

### 真机实测结论（GitHub，gh CLI + 私有仓库）
- TaskAPI 全量审查：140s 完成，R1 拆出 4 条 AC 带 file:line 证据、AC-4 missing →
  R5.1 一票否决；GitHub 拒绝 PR 作者提 blocking review → poster 自动降级 COMMENT
  并注明原判定（与 Forgejo 一致）
- 评论触发（本地中继按真实 issue_comment 形状 HMAC 签名投递）：重审关键词 → 建任务 +
  回执；歧义长句 → LLM 意图分类 route=ask (0.95)；30s per-PR 节流生效；bot 回帖
  凭 argus 标记 + @ 前缀匹配双保险无循环
- 总结评论多轮**原地更新**（3 轮审查始终 1 条报告评论，body 随最新任务刷新）

## v3.5.4 (2026-09-14) — GitLab note 评论触发打通 + git-platform-sdk v0.61.0

### GitLab note 评论触发（生产 GitLab 上 @argus 链路打通）
- **缺口**：SDK 把 GitLab note 事件归一化为 `cr.note`，而 `isCommentEvent` 只认
  GitHub `issue_comment` 与 Gitea/Forgejo 空-Type 兜底探测——`cr.note` 落入事件
  白名单检查后不在表内被静默丢弃。生产内网 GitLab（4 仓库）上 @argus 一直无响应
- `isCommentEvent` 接受 `cr.note`；新增 `parseCommentMeta` 把两种评论负载形状折叠为
  同一 `commentMeta`（GitLab：`object_attributes.note` / `merge_request.iid`；
  GitHub/Gitea：`comment.body` / `issue.number`），HandleComment 单入口不再感知平台差异
- **非用户意图的注释静默忽略**：GitLab 系统注释（`system: true`，改指派/标签等自动
  生成）与非 MR 评论（`merge_request.iid` 为 0，commit/snippet/issue 评论）不进处理链；
  bot 防循环 / delivery 幂等 / 30s per-PR 节流沿共用路径照常生效
- **修复 Gitea/Forgejo PR 评论误判（实测确认的真 bug）**：Gitea v1.22（试点实例兼容基线）
  的 `PullRequestMeta` 只有 `merged/merged_at/draft/html_url`——**没有 `url` 字段**；
  旧代码以 `PullRequest.URL == ""` 判定"是否 PR"，在 Forgejo 上恒判为普通 issue 而
  静默丢弃。即评论路径（Q&A/suppress/restore/重审）此前在试点实例上从未真正生效，
  仅 GitHub 形状可用。现改 `json.RawMessage` 只判 `pull_request` 字段存在性
- `scripts/setup-webhook.sh` 事件清单加 `issue_comment`——此前只注册
  `pull_request_only`，Forgejo/Gitea 根本不投递 PR 评论事件
- ⚠️ **GitLab 侧运维要求**：仓库 webhook 需勾选 Note events（SDK `CreateWebhook`
  未设 NotesEvents，现有手工注册的 webhook 请在 UI/API 上确认补勾）

### 依赖
- git-platform-sdk v0.52.0 → v0.61.0（forgejo 独立后端、评论列表分页补全、
  UpdateIssueComment 等；逐后端核对了评论事件归一化行为，Gitea/GitHub 语义无变化）
- gorilla/websocket v1.5.1 → v1.5.3（govulncheck GO-2026-6278，make check 恢复绿）

### 测试
- service 单测：`isCommentEvent` 三平台事件判定七例；`parseCommentMeta` 七形状
  （GitLab MR/系统注释/commit note、GitHub issue_comment、Gitea 无-url 元数据、
  普通 issue、坏负载）
- e2e 场景 20 `TestScenario20_GitLabNoteComment`：MR note 重审关键词快路径
  （建任务 platform=gitlab + 回执任务号）→ 30s 节流负控 → bot 自评负控 →
  系统注释负控，四连断言；stack 支持 commentRouter 装配（gitlab fake provider +
  Service 接 Model）

## v3.5.3 (2026-09-14) — agentkit v0.9.5 对齐：宽容解析 + ReAct 重试守卫 + 错误脱敏

依赖基线 agentkit v0.9.4 → v0.9.5（`replace => ../agentkit` 本地开发不变），把新版
能力落到 review 主链路。

### LLM 输出宽容解析（agentkit jsonrepair）
- 新增 `internal/llmjson`：`llm.ExtractJSON` 快路径 + `jsonrepair.Repair`/
  `ParseLenient` 兜底——全角结构标点（，：）、非法转义、尾逗号、未闭合/截断
  （中文模型输出高频损坏形态）
- 接入全部 6 处解析点：R3 合规判定（含解析失败回喂重试轮）、R3.5 反思、
  builtin/remote/acpx findings。此前这类半损坏输出整轮判死（R3 全部 AC 降级
  unverifiable / R3.5 丢弃修订稿 / acpx findings 清空）
- 单测：快路径不回归、七类损坏形态逐一回收、顶层数组、非 JSON 仍拒绝

### ReAct 失败回喂重试（agentkit agentrun v0.9.4）
- R3 / builtin Reviewer / Q&A 三处 `RunWithEvents` → `RunWithEventsAndRetry`：
  首轮失败带中断提示整体重跑一次
- 副作用守卫默认生效：首轮已调用变更类工具则不整体重跑（重复副作用风险），
  错误如实上抛交既有降级链（R3 → 全 AC 人工确认；builtin → invokeSingle 单轮）
- 既有 `ToolsFactory` 按尝试重建工具表，与重试天然搭配（限流计数按尝试重置）

### 任务失败信息脱敏（agentkit logredact）
- `task_failed` 错误链落日志 / 入库（`FinishTask`）/ IM 推送（`announceTerminal`）
  三路消费前统一 `logredact.Redact`——工作副本与平台 SDK 错误可能携带
  `https://user:token@host` 形态的带凭据 URL，此前原样入库并推送

## v3.5.2 (2026-09-13) — e2e 闭环 + 标注重放修复

### 修复
- **学习环标注被 R3.5 抹掉**：Merger 阶段追加的「历史置信度低」后缀会按 id 回写时
  被模型修订稿覆盖。R3.5 之后由 `Merger.AnnotateLowConfidence` 幂等重放
- **nil map panic**：`decodeArtifacts` 返回 nil 时注入 `review_hints` 会 panic
  （学习环开启且有低置信样本的第二轮才触发）

### e2e（§6.4 落地）
- `TestScenario17_RegretLearningLoop`：两轮任务——轮1 R3.5 剔除风格噪声 → regret 落库；
  轮2 同模式重新发表并带低置信标注；真问题负控不标注
- `TestScenario18_LearningHardFloorDropsLow`：预热高 regret 模式后，low 条目被 HardFloor 丢弃
- `TestScenario19_EvidenceGateStrictDrops`：strict 证据门缺 evidence 直接剔除

## v3.5.1 (2026-09-13) — 机制活检 + dream 长期记忆整合

补齐 review 闭环的优先级 3/4（设计 §8 挂点）。

### 机制活检（liveness probe）
- 新指标 `argus_layer_exec_total{layer}`：层被真正走到时 +1
- 打点：`diffprep` / `R1` / `R2` / `R3` / `R35` / `R4` / `R5` / `merger` / `poster`
  - R35 仅在 `ARGUS_REFLECTION_ROUNDS>0` 时注册 series（未开反射不误报）
  - poster 成败/stale 都算「执行过」
- Prometheus 规则：
  - `ArgusLayerSilent`：30m 内有 done 任务但某层 increase==0 → 静默失效
  - `ArgusReflectGateIdle`：R35 跑了很多但证据门零动作（可能 Rubric/模型未生效）

### dream 长期记忆整合
- `internal/dream`：周期遍历 `pattern_signals`
  1. **去重**：同 plugin + `pattern_text` 前 80 rune 相同 → 计数并入样本更多的一行
  2. **导出**：低置信模式（samples≥MinSamples 且 confidence<SoftFloor）写成知识
     - Milvus 主路径 `dream/{platform}/{owner}/{repo}`
     - 失败降级 `{KnowledgeDir}/dream-{owner}-{repo}.md`（下次启动索引）
- 配置：`ARGUS_DREAM_INTERVAL`（Go duration；0=关闭；需同时 `ARGUS_REGRET_LEARNING=1`）
- 建议：`ARGUS_DREAM_INTERVAL=6h` 或 `24h`

### 测试
- dream：去重合并（keep=样本更多）、本地 markdown 导出、无低置信跳过

## v3.5.0 (2026-09-13) — Review 闭环：证据门 + 模式级后悔学习环

从 OFC 借鉴的两条机制落地（设计：`docs/review-closed-loop-design.md`），
把「一次性 reviewer」变成「越用越准的 reviewer」。**默认全关，opt-in**。

### 负控自验证门（R3.5 证据协议增强）
- `reflectFindingWire` 新增 `evidence` / `counterexample`：保留条目必须引用可核对位置 +
  给出反例假设；提示词与 Rubric 第 8/9 条同步
- 确定性后验门（Go，不走 LLM）：缺 evidence/counter、evidence 路径不在变更清单
  → 按模式处置
- `ARGUS_REFLECTION_EVIDENCE=off|soft|strict`（默认 `off`，行为同 v3.4）
  - `soft`：强制降为 low + 标注「证据门」，不剔除、不记 regret
  - `strict`：剔除并记 regret（指纹级 + 模式级）
- 指标：`argus_reflect_dropped_total{reason}` / `argus_reflect_degraded_total{reason}`

### 模式级后悔学习环
- 新表 `pattern_signals`（repo × plugin × pattern_key）：posted/regret/suppress/restore 计数
- `plugin.PatternKey`：确定性 normalize（剥路径/hex/数字/反引号）+ sha256 前 16 hex，
  不含 file/line，可离线复算、可单测
- `plugin.Confidence`：Beta 先验 (2,2)，读时计算，不落列
- 消费点（`ARGUS_REGRET_LEARNING=1` 才生效）：
  - Merger：SoftFloor 标注「本仓库同类意见历史置信度低」；HardFloor **只丢 low**
  - R4 前注入 `ArtifactReviewHints`（builtin Reviewer 拼进提示词）
  - 信号写入：Merger 成功发表 → posted；R3.5/证据门剔除 → regret；
    人工 suppress/restore → suppress/restore
- `finding_prints` 补 `plugin` 列（反馈路径归属学习信号）
- 阈值：`ARGUS_REGRET_MIN_SAMPLES/SOFT_FLOOR/HARD_FLOOR/INJECT_FLOOR/INJECT_TOP_N`

### 红线不变
- 持久 suppress 仍只来自人工反馈；学习环不自动 suppress
- R5 裁决不走 LLM；证据门发生在 R5 之前
- medium/high 不被统计一票否决（HardFloor 只作用于 low）

### 顺带修复
- `deploy/migrations/schema.sql` 补 GORM 已有但 schema 缺失的 `finding_prints.regret_count`
- `prints_test.go`：首次 suppress 不存在指纹的 `matched` 语义与实现/文档对齐（应为 false）

### 测试
- pattern_key 稳定性/插件隔离；置信度方向与 HardFloor 场景
- pattern_signals 存储往返 + 仓库隔离 + 小样本不消费
- 证据门 off/soft/strict + 路径越界 + 完整证据通过
- Merger 学习闸门：只丢 low、关闭旁路、MinSamples 负控、PatternPosted

## v3.4.2 (2026-09-13) — ReAct Failover + 评论词表守门 + 审计脱敏（复刻 agentkit v0.9.4）

从 agentkit（bianque 生产装配沉淀）复刻三块到 Argus，并顺带修掉一处既有接线缺口：

### ReAct 主路径 Failover（agentkit/llm.FailoverModel）
- 既有 `OPENAI_FALLBACK_*` → Resilient 只覆盖 Generator 直连（R1/R2、Reviewer 单轮）；
  ReAct 主路径（ADK ChatModelAgent 直调 BaseChatModel）拿到的是 `RawModel()`，主模型挂掉无备可切
- 配置 fallback 时：R3 / QA ReAct / Reviewer ReAct / Router / R3.5 统一包 `FailoverModel`
  （主失败且 ctx 存活才切；`OnFailover` 打 `argus.llm.failover` 日志）
- **修：`ARGUS_MODEL_R3` 此前未接到 ReAct**——`Workflow.R3ChatModel` 只被 reflection 回退使用，
  `runAgent` 一直用 `LLM.RawModel()`。现阶段覆盖真正生效于 R3 ReAct

### 评论意图路由否定守门（agentkit/router.KeywordHit）
- `matchReviewKeyword` 由裸子串改为 KeywordHit/KeywordHitBoundary：
  「**不要**重新审查」不再误触发全量重审；拉丁词（rerun/re-review/recheck）加词边界

### 审计日志脱敏（agentkit/logredact）
- `audit.Logger.Log` 字段值统一 `logredact.Redact`：token=/Bearer/连接串/PEM 私钥落盘前打码
- Argus 全部审计动作（task.create / finding.suppress / qa_reply…）自动受益

### 顺带修复（主 worktree 既有破损，阻塞编译/测试）
- `poster.inlineCommentBody` 未使用变量导致编译失败
- `service_test.go` suppress/restore 两测函数体交叉错位
- `storage/prints_test.go` 错误 import 路径 `git.n.xiaomi.com/...`
- `merger_regret_test.go` 未使用变量

### 测试
- comment_router：否定守门用例（「不要重新审查，先修 bug」）
- agentkit audit：RedactFields 凭据/PEM/普通字段分层断言
- go build + vet + service/workflow/plugin 全量测试通过

## v3.4.1 (2026-09-13) — regret 跨轮学习环（复现/可信度标注）

R3.5 反思只做单轮统计，误报历史不沉淀；`GetPrintState` 有接口有实现却无调用方。
本版把「产出 vs 终审」的差值变成跨轮信号：

- R3.5 剔除的指纹经 `ReflectSummary.RemovedPrints` 由 runner 入库
  `finding_prints.regret_count`（`Store.RecordPrintRegret`，行缺失时创建）
- Merger 跨轮查 `GetPrintState`：`times_seen>=2` 标注「跨轮复现 N 次」，
  `regret_count>=2` 标注「历史被 R3.5 反思剔除 N 次，可信度低」
- 标注只影响报告展示（入库后改写，不污染指纹与 prints 原文）；suppressed
  条目照旧剔除，持久抑制仍只来自人工反馈通道
- 单测：正常标注 / suppressed 不复活（负控）/ GetPrintState 失败不阻塞
- **`@argus restore fp:xxxxxxxx` 误抑制恢复**（同版顺带补齐）：suppress 反向操作——
  被误杀的指纹置回 open，后续轮次重新可见；短码前缀解析、歧义不猜、不回流知识；
  含存储往返 + 负控单测（不存在前缀/前缀歧义不 matched）

## v3.4.0 (2026-09-07) — 接入 agentkit v0.8.0 架构原语（reflection / router / blackboard）

P6 已完成 agentkit v0.8.1 `llm.StageRouter` 分阶段模型路由（独立提交）。本版补齐 v0.8.0
三个架构原语的领域落地，全部特性默认关闭/保守启用，行为可预期：

### R3.5 报告反思（agentkit/reflection）
- workflow 挂点：Merger 合并去重后、分歧标注与 R5 裁决前——被剔除的意见连带分歧标注一起
  消失，R5 真值表基于修订后 findings 求值
- 终审编辑角色 + 误报特征 Rubric（文件越界/行号非法/纯风格/无证据定位/重复/测试代码误判），
  生成→批判→修订收敛循环（`reflection.Refine`）
- 防幻觉纪律：修订协议按 id 回写，只删/修不增（编造 id/重复 id 丢弃）；逐字段校验回退
  （file 越界回原值、severity 非法回原值、line 负数回原值）；解析失败/模型故障保留原
  findings——审查不因反思故障而失败
- 反思是逐轮统计性质量闸门，不写 suppress（持久抑制只来自人工反馈，防自动化误杀）；
  指纹库语义不变
- 配置：`ARGUS_REFLECTION_ROUNDS`（0=关闭缺省，1~3 启用，>3 钳制）+ `ARGUS_MODEL_R35`
  阶段覆盖（缺省回退 R3 模型）。报告脚注注明剔除/修正计数
- 已知边界：反思调用经 reflection 包内部 llm.Client（stage=reflection:draft/critique），
  不计任务 token 预算；成本经 obsx OnUsage 回流 + 费率记账可见

### 评论意图路由（agentkit/router）
- 挂点：`HandleComment` suppress 分流之后——`@argus` 自由文本先分类再分派：
  - `review`（要求重新审查/再审一遍/重跑评审）→ `CreateTaskDirect` 建全量复审任务
    （无同 SHA 去重——重审语义就是同 head 再来一轮，跨轮指纹/抑制照常生效）+ 回执评论带任务号
  - 其余一切（含低置信、分类失败、词表外）→ Q&A（P5 路径原样保留）
- 确定性优先：短命令（≤30 rune）命中重审关键词直达，零分类调用；歧义长句才走一次快模型
  GenerateJSON 分类（router.Classify，决策 route/confidence/reason 全量落日志）
- `MinConfidence`（`ARGUS_ROUTER_MIN_CONFIDENCE`，默认 0.6）之下的 review 视为 ask——
  误触发重审的代价远高于多答一句；分类模型 `ARGUS_ROUTER_MODEL` 覆盖（缺省 QA → 主模型）
- 默认关闭（`ARGUS_COMMENT_ROUTER=1` 开启）——重审是烧预算的强动作，显式 opt-in

### 编队黑板共识模式（agentkit/blackboard）
- plugins.yaml `squads:` 段新增 `mode: consensus`：编队成员不进 fan-out 盲审，改在共享
  黑板上轮转互看（`blackboard.Convene`）——每轮只补充他人未覆盖的新问题，一整轮无人补充
  = 共识收敛（上限 2 轮，比 agentkit 缺省 3 收紧一档）
- 互看通道：`Task.Artifacts["squad_board"]` 逐轮重建增量视图，builtin Reviewer（ReAct+
  单轮两路径）与 acpx 适配器提示词装配消费；指令在数据围栏外（防注入纪律不变）
- 成员失败转黑板 note + 降级注明，不中止召集（Convene 的 Act 报错会中止整轮，Argus 侧
  全部兜住）；预算/熔断门与 fan-out 同语义
- 收敛发现按成员归属构造独立 Outcome 进 Merger——指纹去重/抑制过滤/跨轮合并全部复用既有
  管线；consensus 成员不参与交叉分歧标注（互看后无盲审分歧，`ActiveSquadMembers` 排除）
- 默认编队模式（互盲独立）行为不变；单成员 consensus 编队告警并按共识流程跑（无互看对象）

### 明确不接：Plan-and-Execute
- `agentrun.PlanAndExecute` 的适用前提是"步骤可预规划、计划本身有价值、流程在探索期"；
  Argus 的 R1→R5 是已固化的确定性编排（agentkit README 对 Graph Workflow 的"该用"场景
  正是这个）——在其上叠 P&E 是典型的"杀鸡用牛刀"，架构纪律优先于能力覆盖

### 测试
- reflection：修订/剔除计数、幻觉 id 拒收、file 越界回退、severity 归一化、解析失败与模型
  故障保底（`workflow/requirement/reflection_test.go`）
- router：关键词快路径边界（30 rune）、置信度门、词表外回落、模型故障回落（`service/comment_router_test.go`）
- consensus：互看去重收敛、成员失败降级、预算跳过、未注册成员、重复召集不串状态、独立模式
  回归（`plugin/squad_consensus_test.go`）；`go vet` + 全量 `go test` + race 全绿

## v3.3.0 (2026-09-07) — 基于 agentkit v0.7.1/v0.7.2 的能力迭代

### P0 成本记账
- obsx OnUsage（agentkit v0.7.2）回收全部 eino 模型调用的真实 usage——覆盖 Generate 直连与 ReAct RawModel 两条路径
- 任务级 + 全局 CostTracker；报告元信息行新增 `成本 ≈ $X`（ARGUS_LLM_COST_PROMPT_PER_1K / ARGUS_LLM_COST_COMPLETION_PER_1K 配置费率）

### P2 builtin Reviewer ReAct 化
- Reviewer 可选挂载工具表钩子（复用 R3 的 get_file/get_diff/search_knowledge/use_skill/MCP 工具基础设施，Workflow.ReviewTools 导出）
- agentrun.RunWithEvents 驱动，ToolsFactory 按尝试重建工具表（toolprior 限流计数按尝试重置）；ReAct 失败自动落回单轮 GenerateJSON——审查不因工具链故障而失败
- 工具轨迹（tool_call/tool_result）落结构化日志

### P3 交叉审查编队
- plugins.yaml 新增 `squads:` 段：paths/labels 触发 → 成员追加进本轮分派（squad: 原因可观测）
- Merger.AnnotateCrossReview：编队成员对同一文件 ±5 行"仅一方报告"的意见标注分歧，报告头显示编队注记

### P5 Q&A 对话式审查助手
- `@argus ask` 升级为 ReAct 模式（agentrun + 工具表：get_diff/get_file/search_knowledge，toolprior 三层约束 + 数据区围栏）——"这条 why"可引用 file:line 证据，不再只对着摘要猜
- 多轮会话：`qa_sessions` 表（一 PR 一会话，6 轮封顶 FIFO + 24h 过期重置），追问带历史语境
- ReAct 失败自动落回单轮直调（原路径保留为降级兜底）
- mcpserver 新增 `ask_argus` 工具：无追问返回某 PR 最新审查结论（verdict/findings/报告摘录）；带 question 基于报告回答追问——IDE/其他 agent 可直接消费 Argus 评审能力

### P6 分阶段模型路由
- agentkit v0.8.1 `llm.StageRouter`：stage→Generator 多路复用（精确优先/最长前缀/缺省链，UsedTokens 聚合）
- config 新增阶段覆盖：`ARGUS_MODEL_R1/R2/R3`、`ARGUS_REVIEWER_MODEL`、`ARGUS_QA_MODEL`（+ 各自 `_CONTEXT_TOKENS`）——继承主 BaseURL/APIKey，配置全局降级时覆盖阶段各自构建降级链
- R1/R2 经 StageRouter 进 workflow（"R1" 命中 "R1a"，任务预算在路由包装时注入）；R3 覆盖 ReAct 专用模型（Workflow.R3ChatModel）；Reviewer/QA 走专用出口
- 大窗口模型吃 R1 长输入、快模型跑 R2 摘要、强模型保 R3/R4 质量——按阶段分层配模型，调用点零改动

### P1 多副本横向扩展
- agentkit v0.7.3 `worker.LeaderElector` + 存储 `leader_leases` 表租约实现（条件 UPDATE 原子"判-占-续"）
- poller 选主门控（非 leader tick 跳过；失联 ≤ttl 自动换主，优雅停机主动让位）
- 实例身份 `argus.boot.instance` 日志；workcopy 按实例隔离（`WorkRoot/instance-<id>/`，防跨实例 Sweep 误删）
- deploy/k8s/README-scaling.md 多副本指南（前置 MySQL、容量公式、验证清单）；04-deployment.yaml 注明单副本档与切换路径

### P4 知识飞轮
- SuppressFinding（HTTP + MR 评论 `/argus suppress fp:xxxxxxxx`）成功后误报结论回流知识库（Milvus 模式；内容经 safejson 转义）
- R3 search_knowledge 检索命中历史误报即带抑制语境——同型误报率随使用下降

## v3.0.3 — 2026-08-31（ekit v0.17.3 升级 + bootstrap 生命周期接管）

ekit v0.17.0 → v0.17.4（v0.17.1/2 app-lifecycle 顺序死锁修复 + v0.17.3 timedWait
ctx 取消支持 + WorkerManager + v0.17.4 hlog 递归防御 panic + RunServer 文档修正）。main 装配接入 `bootstrap.Run`：

- main 拆为 main/runApp：runApp 用 `app.Context()` 贯穿全部子系统，资源按
  [store, worker, http] 注册 → 停机逆序执行（http 5s 排水 → worker 两阶段 60s grace →
  store 最后关）；prune/queue-depth ticker 换 `WorkerManager.AddTicker`（ctx 取消
  自动退出，无需手动管理 ticker 生命周期）；`h.Spin()` + 手工 wp.Stop/cancel 换
  `ekithertz.RunServer` + `AddResource`；
  ShutdownTimeout=90s（覆盖 http 排水 + worker grace + 硬取消余量；k8s 终止宽限 90s 内）。
- **bootstrap 语义**：userFn 返回 = 主动关机——runApp 末尾 `<-ctx.Done()` 阻塞；
  SIGTERM 由 bootstrap 统一承接（实测：启动存活 → SIGTERM → cancel → http →
  worker → store → 1s 退出，关闭序列与设计一致）。
- **修复 hlog 桥接无限递归（P0）**：`hlog.SetLogger(ekithertz.NewLogger(logx.WithService(...)))`
  是死循环——logx.WithService 走 logx 默认 hlog 后端，装回 hlog 后 adapter↔logx
  互调无界递归（实测：主 goroutine 卡死 + 100% CPU + SIGTERM 无响应）。
  必须传 slog 实例：`ekithertz.NewLogger(logx.NewSlogLogger(...))`。
  ⚠️ ekit AGENTS.md 的示例即此雷，待上游修正。
- e2e fake 适配并行 WIP：runner postStartMarker 的"审查中"占位评论（锚点 `argus:start`）
  在 fake 中独立计数（markerN），不计入 created/comments——summary 断言语义不变。

## v3.0.3 — 2026-08-31（ekit 化：共享库能力全面收编）

工作区共享库 ekit v0.17.0 的能力替换手写实现；不改业务语义，E2E 与单测全绿。

### 行为修复

- **trace_id 恒空**：所有 API 响应壳此前硬编码 `"trace_id": ""`。main 注册
  ekit `traceid.Middleware`（生成/透传 `X-Trace-Id`，12 位短 ID），响应壳经
  `mw.SuccessRespMsg`/`mw.ErrorRespWithStatus` 自动携带；`logx.Ctx*` 日志同步
  关联 trace_id。透传语义保留：客户端带 `X-Trace-Id` 则原样回显（traceid_smoke_test.go）。
- **/metrics 缺 go/process 收集器**：`obs.Init` 补 `ekitmetrics.Init("argus")`——
  注释声称含 go/process 指标但从未注册，现补齐（含 `app_build_info`/`app_start_time_seconds`）。
- **"record not found" 字符串匹配**：storage/service 5 处 `strings.Contains(err.Error(),...)`
  改 `errors.Is(err, gorm.ErrRecordNotFound)`，驱动层错误包装变化不再误判。

### ekit 替换清单

- 响应壳：service/handler 手拼 `{code,data,message,trace_id}` map → `mw.SuccessRespMsg`/
  `mw.ErrorRespWithStatus`/`mw.ErrorRespBiz`（webhook 成功 body 的 code 由 200 归一为 0，
  平台侧只看 HTTP 状态码）；Bearer 鉴权 401/503 同壳。
- 参数绑定：`c.BindAndValidate` → `validatorh.BindAndValidate`（追加 validator 规则）+
  `mw.ErrorRespBiz`（绑定失败从裸文本 400 改为统一 JSON 壳）。
- SSE：GET /events 手写 io.Pipe + 专用 goroutine + 手拼帧 → `ekithertz.NewStream`
  （自动设头、逐事件 flush、断连写失败自然退出）。
- goroutine 纪律：9 处裸 `go func` → `async.GoSafe`（worker/poller/bus/dispatcher/
  cli 看门狗/workcopy TTL/main ticker），panic 统一经 `async.SetPanicHandler` 走 logx。
- /metrics handler：手写 expfmt 循环 → `ekithertz.MetricsHandlerFor(ekitmetrics.Registry)`。
- 日志桥接：`hlog.SetLogger(ekithertz.NewLogger(...))`——Hertz 框架内部日志并入 logx。
- 事务：`db.WithContext(ctx).Transaction` → `gormx.InTransactionContext`（语义等价）。
- octopus 推送：手写 `http.Client` → `httpx.NewClient`（WithBaseURL/WithTimeout/
  WithBearerToken，`X-User-Id` 经 `WithOnBeforeRequest` 注入；非 2xx 经
  `ServiceError` 保留 send_rejected/send_failed 两种告警语义）。
- env 解析：`config.GetStringWithDefault`/`GetInt`/`GetBool`；⚠️ 非法值回退默认的
  stderr 留痕（warnBadEnv）保留——ekit 的 `*WithDefault` 是静默回退，故包一层告警。

### 评估后不换（语义不匹配）

- `mw.BearerTokenAuth`（fail-open：未配 token 直接放行，与 argus fail-closed 纪律相反）；
- ~~`bootstrap.Run + ekithertz.RunServer`~~（v0.17.2 修复顺序死锁后已采纳，见上节）；
- `concurrency/progress.Tracker`（单对象阶段进度条，非 pub/sub 事件总线）；
- `pathx.MatchesPath`（不支持中部 `**` 与 basename 全深度匹配，插件路由语义会变）；
- `gormx.Open`（默认注入 Info 级日志+OTel，SQLite 单连接串行是幂等正确性前提）；
- `httpx` for a2a client（serviceURL 由 Agent Card 运行时发现，任意 URL 与 httpx
  固定上游+path 模型不匹配；ctx 超时已由 dispatcher 统一控制）；
- `LoadDotEnv`/`envDuration`（ekit 无 .env 能力；`ParseDuration` 静默回退丢告警）。

## v3.0.2 — 2026-08-31（全面审查修复：1×P0 + 9×P1 + P2/P3）

三路并行代码审查（workflow+插件 / 任务生命周期 / I/O+安全面）发现的问题全量修复。
测试锚点见括号；E2E 16 场景与 -race 全绿。

### P0 · 反注入

- **R3"少报/漏报"注入洞**（`alignChecksToSpec` 此前只单向剔除发明条目）：spec 有而
  R3 漏判的 AC 一律补齐为 unverifiable；status 词表校验，词表外值（如 "done"）归一
  unverifiable。空 checks/漏报不再缩水成 R5.5 APPROVE，必落 R5.3 人工确认。
  （verdict_test.go: TestAlignChecksBackfill）

### 修复（P1）

- **数据区围栏逃逸**：DataFence 中和内容中的 `【数据区` 标记序列；R3 三个工具
  （get_file/get_issue/get_diff）返回值统一包数据区；issue 标题移入内容区——
  此前文件内容这一最大注入面完全裸奔（stages.go）。
- **webhook 空 secret 运行期 fail-open**：改为 fail-closed 503（与 🔒 路由同一纪律），
  配置 `ARGUS_<P>_SECRET` 前该平台 webhook 拒绝服务（service.go）。⚠️ 行为变更。
- **cli 零 finding 被当错误**：合法 `[]`/`{"comments":[]}` 返回零结果而非 error——
  此前连续 3 个干净 PR 会把 cli 插件熔断摘除 5 分钟（cli_test.go）。
- **行内评论一票否决式失败**：发前按 diff 文件列表预校验（幻觉文件剔除并告警）；
  CreateReview 失败降级链增加"去 comments 纯评论事件"兜底——评审事件与结论不再整轮丢失。
- **builtin Reviewer token 不入任务预算**：Dispatcher 经 `Task.Budget` 注入 per-task
  累计器，Reviewer/CompliancePlugin 计入——R4 主成本此前对 PruneByBudget 完全不可见。
- **报告渲染零转义**：新增 `reportview.EscapeUntrusted`（HTML 注释实体化 + 行首标题/
  围栏打断 + 反引号替换），AC 文本/证据/意见/降级注记/行内评论全量套用；
  扫描兜底改严格报告头正则 + bot 作者校验，第三方评论不能再劫持更新目标（report_test.go）。
- **MySQL 化两个坑**：tasks/finding_prints 大文本列启动期自动升级 LONGTEXT
  （TEXT 64KB 上限此前会让大报告写入失败 → 卡 running → 重启重跑 → 再失败死循环）；
  DSN 判定改 `mysql://` 前缀或 `@tcp(`（路径含 "mysql" 的 SQLite 文件不再误判）。
- **重跑双执行/双回帖**：running 任务引入 runner_id + heartbeat_at（worker 每 20s 续），
  重置条件改为"心跳死亡 >90s"——多副本滚动重启不再误伤其他实例在途任务；
  runner 对硬取消显式保持 running（不再依赖 ctx 已死写库失败的隐式副作用）；
  评审事件加 task_id 幂等锚点（发前 ListReviews 查重，重跑不再重复发评审）。
- **MCP wait 不可回收轮询**：timeout_seconds 上界 clamp 10 分钟；HertzBridge 传播
  hertz ctx（客户端断连即取消轮询，不再挤占单连接 SQLite）。
- **cli 子进程最小环境**：只透传 PATH/HOME/TMPDIR/代理/CA + plugins.yaml `env` 显式
  白名单——此前继承完整 os.Environ()，ARGUS_*/OPENAI_* 凭证直接暴露给待审仓库可执行代码。

### 修复（P2/P3）

- 熔断半开探测被预算门跳过后永久卡死（预算门移到熔断门之前）；dispatcher.go
- 同 delivery 并发重发撞唯一索引以 500 暴露（事务内冲突回查按幂等重复处理——GitHub 连续
  500 会自动禁用 webhook）；规则 4a 非 NotFound DB 错误不再静默吞掉
- staleness 误伤：手动任务（head_sha 为空）不再压制 staleness——此前"手动任务先压 stale
  再失败"会让本轮审查永久静默丢失
- workcopy 孤儿目录回收（prepare 克隆窗口被 kill 遗留的 wc-* 目录按 mtime 清理，
  此前只认内存登记表，k8s PVC 慢性写满）
- 环境变量非法值回退默认时输出 stderr 告警（`SKIP_TLS=tur` 静默变 false 这类漂移可排查）
- diffprep/get_file 截断改 rune 安全（多字节字符不再腰斩出非法 UTF-8 进 LLM 上下文）
- service：number 校验拒绝 `+123`（纯数字白名单）；Bearer 常数时间比较；任务列表空返回
  `[]` 而非 `null`；poller 的 draft 过滤与 `ARGUS_SKIP_DRAFT` 同源（此前两条任务源不一致）
- llm：注释"指数退避"实为固定延迟——改线性退避并对 401/403 类确定性错误跳过重试
- Registry.Snapshot/dispatcher 注释与实现对齐（字母序而非注册序）
- deploy：k8s configmap 的 SKIP_TLS=1 加 MITM 风险明示；compose 补齐 README 引用的
  `.env.example`；新增 `.dockerignore`（.env/argus.db/bin 不再进构建上下文）
- 死代码清理（runner.hasHigh）；hz middleware.go 头部注明"用户自定义区，hz update 保留"

### 评审返工（独立 code-review 后修复）

- `upgradeMySQLLongText` 的 information_schema 探测错误未检查——查询失败会静默跳过
  ALTER，让 64KB 死循环修复无声失效；现失败即返回启动错误。
- 补三个核心新语义的回归测试：心跳新鲜不重置（多副本安全不变量）、空 head 手动任务
  不压制 staleness、webhook 空 secret 503（对照：配置 secret 后走到签名校验）。
- Minor：RunnerID uuid 失败退化 hostname:pid（空 ID 会让心跳 no-op、任务被反复重置）；
  DSN `mysql://` 前缀大小写不敏感剥离；EscapeUntrusted 补 setext 下划线（`===`/`---`）
  与列表标记（`- `/`1. `）两类视觉注入的中和；报告头正则 `head=\S*` 容忍空 head；
  截断标记改 rune 口径（多字节内容不再"打了截断标记实际没截"）；llm.Client 浅拷贝加
  脆弱性警示注释。

### 已知边界（有意不改）

- 总结评论扫描兜底受 SDK 分页限制（ListIssueComments 只返回第一页）：评论被顶出第一页后
  走新建路径——待 git-platform-sdk 补分页能力；
- 指纹不含行号保持不变：掺行号会破坏 `/findings/suppress` 的"file+原文"契约（suppress
  时无行号可算）；同文件同措辞不同行的问题合并仍由人工抑制兜底；
- `/metrics` `/readyz` 维持无鉴权（默认 ClusterIP 内网；Ingress 暴露时需屏蔽）。

## v3.0.1 — 2026-08-31（真实试点驱动的演进基线）

基线：v3.0 设计（design.md 六轮评审定稿）全量实现后，进入 git.example.com（Forgejo）真实试点。
以下变更均由试点实测驱动，测试锚点见括号。

### 新增

- **轮询兜底任务源**（`internal/poller`）：平台→Argus 入站 webhook 不可达环境（NAT/VPN，
  试点实测抓包零包）下以出站轮询发现新 PR 与 head 变化；复用同 SHA 双查去重，与 webhook
  任务同语义。`ARGUS_POLL_INTERVAL` / `ARGUS_POLL_REPOS`。（E2E 场景12）
- **误报反馈最小入口** `POST /findings/suppress`（🔒）：按 Merger 同一指纹函数
  （file + 规范化评论文本哈希）置 suppressed，之后每轮报告自动剔除；新增指纹可预插
  （matched=false）。报告脚注的"N 条历史意见已被抑制"从此有了真实写入通道。（E2E 场景13）
- **任务列表** `GET /tasks?status=&limit=`（🔒）：status 白名单过滤、limit≤100、倒序。
  （E2E 场景14）
- **每轮评审记录**：verdict 恒映射 CreateReview 事件（REQUEST_CHANGES/COMMENT/APPROVE），
  行内意见随附——此前无行内意见时跳过，PR 页看不到任何审查动作。
- **仓库配置可观测**：`GET /agents?platform&owner&repo` 返回 .reviewagents.yaml 规范化
  原文 + 插件生效状态（locked/selected/routed/excluded_by_use/disabled/default）+ 信任边界告警。
- **运维**：两阶段优雅停机（停领取→等在途 60s→硬取消）；`ARGUS_TASK_RETENTION_DAYS`
  保留清理；`argus_tasks_pending/running` gauge；部署交付 `deploy/`（compose + k8s kustomize）。
- **Argus as MCP Server（方向 B 实装）**：`/mcp` streamable http（Bearer 鉴权），
  四工具 `review_pr` / `requirement_compliance` / `list_agents` / `get_report`——
  任意 MCP 客户端可发起审查并取回结构化结论与报告；依赖 mark3labs/mcp-go v0.58.0
  （§8 纪律：随实装期进 go.mod）。实现 `internal/channel/mcpserver`（Hertz 桥接保留
  Mcp-Session-Id 等响应头）。E2E 场景16：真实 MCP 客户端 initialize→list_agents→
  review_pr(wait)→get_report 全链路。（mcp_server_test.go）
- **反馈/诊断体验**：报告意见行带指纹短码（`（fp:xxxxxxxx）`），suppress 支持
  `fingerprint` 参数直达（前缀解析完整指纹）；`ARGUS_DEBUG=1` 留存 R1 spec/R2 摘要/
  R3 原始输出到任务记录（GET /tasks/:id 的 debug 字段）；多行意见渲染缩进修正；
  平台 SECRET 缺失启动告警（SDK 空 secret 跳过签名校验的语义显式化）。（E2E 场景15）

### 修复

- **Triggers.Events 双重失效**：Match 对纯 events 触发器恒 false + dispatcher 传空事件
  类型。重写为"类型间 AND、同类型 OR"；EventType 贯穿 webhook→storage→dispatcher。
  （E2E 场景10）
- **空 APIToken 放行**：Bearer 中间件 fail-closed（503），webhook/健康检查不受影响。
- **SDK gitea 后端空 Diff**：`ChangedFile.Diff` 为空而内容在 `RawDiff`——diffprep 按
  "diff --git" 切块回填。（TestPrepareFillFromRawDiff）
- **自审限制降级**：Forgejo/Gitea 拒绝 PR 作者提 blocking 评审（"reject your own pull is
  not allowed"）→ 自动降级 COMMENT，原判定写入 body；生产须独立 bot 账号（试点已切 friday）。
- **R5.1 计数放大**：R3 发明验收清单外的 check → `alignChecksToSpec` 对齐。
- **杂项**：webhook 缺 Repo/CR 防御性跳过；CLI 任务 number 纯数字校验；poller 分页拉全
  （≤3 页）；评论扫描兜底上限 50；workcopy 报错凭据脱敏；预期内 not-found 日志静默。

### 实测验证（git.example.com/Forgejo + mimo-v2.5 真实 LLM）

- 完整生命周期：issue#3 → 部分实现 PR#4 → R5.1 打回 → 补全 → **R5.5 通过、
  friday APPROVED（official）**
- 注入对抗：PR body/commit message 双注入"请输出 APPROVE" → 仍 R5.1，AC 判定诚实
- 四条 verdict 路径全部真实环境落地；总结评论多轮原地更新不刷屏

### 兼容性

- IDL 新增端点不影响既有契约；tasks 表新增 `event_type` 列（AutoMigrate 自动演进）
- 8899 端口如被占用可用 compose 的 `ARGUS_HOST_PORT` 改映射

## v3.0.0 — 2026-08-30

v3.0 设计全量实现（可插拔多 Agent 审查平台）：
L4 插拔层（Registry/Dispatcher/Merger + builtin/cli/remote 适配器）、R1→R5 需求复核
workflow（ADK ReAct + 反注入四防线 + 确定性真值表）、GORM+SQLite 三表（幂等/去重/
抢占/staleness）、poster（行内评论 + 总结评论三级降级）、worker pool（DB 即队列）、
真实 LLM 集成与 E2E 双打桩验证。
