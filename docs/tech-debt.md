# tech-debt —— 代码体量与复用剩余清单

> 全面精简轮（v3.95.0~v3.101.0，2026-10-02~03）收官后的持久清单；v3.106.0~v3.112.0
> 设计模式/收口轮（2026-10-04）续账。下一轮优化从这里起步；
> 每完成一项请同步更新本文件。量法：非测试、非生成（`biz/model/api/` thriftgo 除外）Go 代码。

## 基线（2026-10-04 复测；2026-10-28 v5.1.0 复测）

- 手工代码 **24,145 行**（v3.101 基点 24,059）；**v5.1.0 复测：非生成 32,457 行**——v3.113→v5.0 两个月新增 impactview/qa/sastscan/reposettings/memorykernel 等约 8,300 行，机械扫描（deadcode -test 归零 / >100 行函数 4 个全有归属 / 跨包零重复函数体）确认新包纪律在线，无精简欠账。
- **单元覆盖率 34.3%**（全仓 statements 口径；不含 test/e2e 打桩链路——golden 69 条走真实编排但按包不计数，编排面实际覆盖远大于此数，基线仅作纵向对比）。薄弱面清理：**poller 15.5%→93.0%**（v3.112.0 补 pollOnce 行为套件——poller 是试点常开的生产任务源且 golden 只覆盖 webhook 源）；runner 13.9% 为 golden 编排按包不计数的口径假象，不补包内单测。
- 最大手工文件 **554 行**（runner.go），全部文件 <560 行。
- 100+ 行函数从 **10 个降到 5 个**（mcpserver.New 被 v3.108.0 底座抽取降到 97，见下表）。**v5.1.0 复测：4 个**——bootstrap.Assemble 204→244、StartSubsystems 206（新增长点，组合根铺开型同裁定）、config.Load 137、JudgeCompleteness 108。

## v5.1.0 深度重构批（2026-10-28）

- **TaskKind 间接层清偿**：v3.106.0 词表下沉 domain 时留的 plugin re-export 别名（19 消费点）全仓直连 domain，plugin/taskkind.go 删除——间接层完成使命归零（IsKnownKind 仅 1 消费点）。arch 白名单同步（poller/service/runner/workflow → domain 直连合法化）。
- **deadcode 清偿**：plugin/testlog_test.go（testNopLogger 21 方法，测试已迁 logx 后全死）删除，`deadcode -test ./...` 归零。
- **render.go 超规范拆分**：impactview 575 行 → render.go 306（文本面）+ mermaid.go 333（图渲染面）——图面被 e2e 快照与模型提示词双重消费，文本面只服务评论/报告，生命周期不同步。
- **pruneSpillRoot 周期化**：每任务 wireWorkflow 全目录扫描（任务风暴下 O(N²)）挪 bootstrap `spill-prune` ticker（与 workcopy-scavenger 同周期）。
- **评估后不做**（复判）：loadRepoConfig 双份（runner 侧缓存进 PRContext、workflow 侧带 override，语义不同构）；recvTypeName 双份 8 行（tail 先例）；Service 字段分组（有意的门面形态）；>100 行 4 函数（同 v3.112 裁定，bootstrap 长出属新增子系统装配）。
- 批记录：v3.95~v3.101 见 git log；v3.106.0 词表注册表 / v3.107.0 空对象+解码单源 / v3.108.0 mcpserver 底座 / v3.109.0 建任务工厂+词表常量 / v3.110.0 写作模板方法 / v3.111.0 工具信封单源 / v3.112.0 poller 覆盖+死代码清偿。

## v5.3.2 上游能力回收批（2026-11-02，随依赖升级 v5.3.1 之后）

- **GitHub App 刷新器 → 上游 TokenSource**（净删 ~120 行）：`githubAppRefresher`
  （50min ticker + provider 重建原子替换）+ `appTokenManager` + `SDKCtx.Stop` +
  subsys.go 停机收编，全量替换为 `installationTokenSource`（go-git-platform
  v0.100 `Config.TokenSource`，transport 每请求取值 + 临期 5min 懒续期）。顺修
  正确性窗口：旧方案 token 过期到 provider 重建完成之间，在飞请求持过期 token
  401；现在每请求现取，窗口归零。启动期 eager 首取保留（fail-fast：凭据坏 ⇒
  平台启动即判死，不拖到首请求）。新增 3 用例（快路径/临期退旧值/过期无凭据报错）。
- **{dir} 注入 shell 模板转义**：fixverifier + sandbox_verify 两处
  `strings.ReplaceAll(cfg.Cmd, "{dir}", dir)` 直拼 `sh -c`，路径含空格/引号/$
  元字符时验证命令静默跑错。收口 `plugin.ResolveShellTemplate`（shellx.Quote，
  v0.31.0 上游原语）+ 7 组表驱动用例。
- **readyz LLM 探针 ctx 贯穿**：checker 拿到 ctx 一路丢成 Background，停机取消
  传不到、trace 断链；改 `http.NewRequestWithContext` 贯穿。
- **qa_session CAS 重读吞错补 Warn**：unmarshal 失败静默把会话历史截断为本轮，
  补 `argus.qa.session_reload_malformed`。
- **ConditionalRequests 平台开关**（v0.100 `Config.ConditionalRequests`）：
  `ARGUS_<PLATFORM>_CONDITIONAL_REQUESTS`（默认关，env 已接四平台）；GitHub 的
  304 不计限流预算，poller 每 tick 全量翻页场景可持续轮询速率实质抬升。
- **测试日志净化**：97 处 `logx.NewSlogLogger("…")` → `logx.Nop()`（v0.8.0 新
  原语；断言失败信息不再被 JSON 日志淹没；e2e 1 处保留真 logger 供排障）；
  service/qa 双包 noopLogger 同构随之删除（销账上方不做项）。
- 手写数字解析 → `strconv.Atoi`（argus-copilot parseFindings，正则已限字符集）。

### v5.3.2 遗留 issue（下一轮起步候选，按收益排序）

1. **argus-copilot 分发/usage → pkg cli.App**（v0.31.0）：main.go 手写 switch
   分发 + 33 行 usage，退出码契约靠注释人肉同步（review.go:27「改此须同步」）；
   cli.App 内建 --help/version/默认子命令/退出码 0-1-2 单源，估删 ~70 行。风险：
   usage 文案格式会变。
2. **GetRepo→LoadRepoConfig 两步模式 4 处重复**（runner_prctx / requirement/verdict
   / describe / service_agents）：收口 `SDKCtx.LoadRepoConfigAuto` + describe/agents
   路径加 5min 默认分支缓存（与 collabCch 同款），估省每请求 1 次平台 API + ~30 行。
3. **WebhookConfigManager 新功能机会**（非优化）：v0.100 提供单 webhook 查询/更新
   幂等管理（7 平台），argus 可做「启动时按配置 secret 幂等确保 webhook 存在」，
   省掉手工到平台配置——属功能决策，待试点环境拍板。

## 剩余 >100 行函数（5 个，均有留痕裁定）

| 函数 | 行数 | 裁定 |
|---|---|---|
| `bootstrap.Assemble` | 204 | 组合根装配铺开型，拆分负收益（v3.61.0 在案） |
| `config.Load` | 137 | 声明式 env→字段映射，拆段落 helper 只加间接层 |
| `Dispatcher.Run` | 126 | 熔断配对契约（Allow↔Success/Failure/Abandon 恰一），拆散风险 > 收益 |
| `JudgeCompleteness` | 108 | 编号即结构的确定性清单体（fail/pass 闭包对称） |
| `qa.Engine.tools` | 108 | 工具注册清单，同上 |

90~100 行区间（`runApp`/`mcpserver.New` 97/`CheckCompliance`/`sendReview`/`migratePrintFingerprintsV2`/`SuppressFinding` 等）为健康上限内，不再压。

## 剩余较大文件（420~560 行，内聚面已单一）

- `runner/runner.go` 554：核心编排（Execute/run 管线），再拆只剩把 run 四步提指针，负收益。
- `service/fix.go` 493、`context/sdkctx.go` 493、`storage/store_task.go` 485、`plugin/merger.go` 470、`service/service_comment.go` 447、`poster/poster.go` 425：单一内聚面（fix 命令全链 / SDK 门面 / 任务表操作 / 合并器 / 评论处理 / 回帖）。

## 设计模式再勘察（v3.121.0，2026-10-04，12 点全评估）

应用户"大量应用设计模式"指令的全仓再勘察结论：**模式容量已饱和**——12 个候选点全部为"已模式化/DB 级更强守卫/形式化负收益"，唯一下水的是 webhook 回执词表工厂。评估证据（后续勿重探）：

| 候选点 | 结论 |
|---|---|
| notify renderMarkdown status switch | 2 分支+default，策略表负收益 |
| kindToIDL/protocolToIDL switch | 2-3 稳定枚举 case，表化同规模 |
| poster sendReview/postSummary 降级链 | 每级语义异构（不同 API/幂等锚点），CoR 形式化遮蔽业务注释 |
| runner 注入管线（5 injector） | 调用点已是一行式，注入器即策略对象；表格化零样板消除 |
| HandleComment 命令分发 | 已是具名方法链（准入→触发词→抑制→节流→写作→Q&A），表格化破坏节流语义排序 |
| webhookAdmission 准入序列 | 混有非守卫步骤（delivery 提取/请求桥接），直线+早返回即最可审计形态 |
| bootstrap BuildModels | 已是 Builder 组合形态（buildGenerateChain/buildStageLLMs/wrapFailover/wrapFit） |
| poster prepareInline | 已有全管线测试（TestPrepareInline_FilterNormalizeSortClamp），抽段无测试增益 |
| R 系列 Run 阶段管线 | v3.101 已收口为具名阶段方法，失败策略/checkpoint 逐级异构，框架化是仪式感 |
| 任务状态生命周期 | 已是 DB 级 fencing 守卫（FinishTaskFenced WHERE status='running'+归属校验，比进程内状态表更强） |
| runner writing.go | 已是模板(runWriting)+策略(writeDescribe/Changelog) 形态，LLM 实体在共享 writing 包 |
| webhook 回执词表 | **本批下水**：14 处 Status 字面量 → wh* 常量 + whResp 工厂（v3.121.0） |

## 已裁决不做项（勿重探）

- 生成代码 `biz/model/api/argus.go`（8,537 行，thriftgo DO NOT EDIT）。
- `noopLogger` service/qa 双包同构 17 行：ekit logx 无 Nop，不值得新开共享位。**〔v5.3.2 已销账〕**logx v0.8.0 上游提供 `Nop()`，双包同构已删（见下节）。
- search/get_diff 工具的**执行体**两包各留一份：包装语义本就不同（R3 两级暂存
  get_detail / QA 定长截断），只单源 wire 契约不强行合执行。
- `BearerMiddleware`/obs Prometheus 中间件/`toolpool.Pool` 接口等历史裁定见各提交信息。
- 手拼截断尾注（`\n…（截断）`）不换 `textutil.TruncNote`：后者无 `\n` 分隔会改
  PR 可见文本，且 writing/ocrdeleg 薄包装裁定在案（v3.107.0 复判）。
- mcpserver 大一统 toolSpec 注册表：9 工具 handler 异构（可选能力检查/分支语义
  各异），抽公共底座（requireArgs/taskView）即停（v3.108.0 复判）。
- storage `withWriteLock` 包装器：27 处机械改动低于 Go 惯例容忍度（v3.106 轮复判）。

## 已收口记录（后续勿当新发现）

- v3.102.0 工具 wire 契约单源：`codesearch.ToolIn/ToolOut`（search_code schema
  与 ToolDescription 同居）+ `plugin.ToolFileIn/ToolFileOut/ToolGetFileDesc`
  （get_file 描述此前两面措辞已漂移，取合并措辞为正）。deadcode -test 复扫归零。
- v3.103.0 安全扫描收口：gosec G115（`int32(f.Line)` 饱和钳制 `clampInt32`）+
  G301 两处（sandbox_verify/ocrshim 目录 0755→0750）。**误报裁定**：fixverifier
  G703×3（resolveSafePath 前缀+EvalSymlinks 双侧校验可靠）、fix.go G304（上游
  safeRelPath 拒绝 `..` 穿越，fileExcerpt 全部在其后调用）、G118 fix 独立回帖
  ctx（v3.91.1 刻意）、G401 dream sha1（非安全 slug，换哈希会孤儿化既有记忆
  文件，G505 为其 import 面同源）、其余 G304（config/env 派生根目录，by-design）。
- **gosec 基线全集补档（v3.112.0 复扫定性）**：G301×4 + G306×8（ocrdeleg/memory/
  dream/assemble wcRoot/sandbox_verify/ocrshim/fixverifier 的产物目录 0755 +
  文件 0644）——argus 自有工件目录，单租户容器部署无跨 UID 消费面；收紧会破坏
  ocrshim sidecar 共享卷跨进程读取风险，by-design 留 0755/0644。G115×3/G104×78
  全部在生成代码（biz/model 豁免面）。非生成 26 条至此全部有裁定归属。
- govulncheck GO-2026-6505（otel otlptrace）**已清偿：v3.105.0 依赖升级
  v1.25.0→v1.45.0**；当前唯一漏洞源为零。
- v3.106.0~v3.111.0 设计模式轮：任务 kind 词表下沉 domain 注册表（修 storage
  裸字符串漂移）/ audit 空对象兑现（6 守卫死代码）/ taskFindings 解码单源 /
  mcpserver requireArgs+taskView 底座 / storage.NewTask 建任务工厂 + Dup* 词表 +
  progress.EventTask* 事件常量 + dupResp 模板 / runWritingCommand 写作模板方法 /
  plugin.ToolErrNote 信封单源 + newSearchCodeTool 合一。裁定不做：mcpserver
  大一统 toolSpec 表、截断尾注换 TruncNote、withWriteLock 包装器、tools() 完整
  spec 表（In/Out 泛型互异）。
- v3.112.0 收口批：poller pollOnce 行为套件（15.5%→93.0%，含事件订阅时机坑——
  Bus 只投递发布时已在册的消费者）+ `plugin.IsReviewKind` 死转发删除（v3.106.0
  自引入、复扫 deadcode 抓获——storage 直连 domain 后转发无人消费）。
- v3.113.0 冗余清偿批（无 -test deadcode 口径 + 全仓结构体字段/测试助手扫描）：
  删 `qa.searchCodeToolFor`（只有测试在调的生产平行壳，测试改走 newSearchCodeTool
  真构造器并升级为真实执行断言）/ `plugin.PRContext.IsDraft`（只写不读死字段，
  draft 跳过语义在任务源层直读 cr.Draft）/ writingFakePR.CreateIssueComment 死
  测试覆盖 + 同包 errorString 与 errReply 重复合并。**扫描结论**：config 115 字段
  全有活消费（accessor 可达）、导出常量零死项、跨包重名函数仅 parseFindings（语义
  不同非冗余）与 tail（7 行×2，按 noopLogger 先例不值得新开共享位）、测试助手全活、
  无注释代码块。test-only 保留裁定维持：NewSDKCtxStubbed/testkit.Shq/LoadIndex。

## 复测方法

```bash
# 文件榜
find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' ! -path './biz/*' -print0 | xargs -0 wc -l | sort -rn | head
# 函数榜（type/const/var 也算边界）
for f in $(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' ! -path './biz/*'); do
  awk -v file="$f" '/^func |^type |^const |^var |^\)/{if(name){print NR-start, name, file; name=""}} /^func /{name=$0; start=NR}' "$f"
done | sort -rn | head
```

门禁：`make lint && make test && make arch && make eval`（golden 69）。
