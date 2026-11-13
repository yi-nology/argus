# 架构卫生与架构规范全仓审计报告

> **执行状态（2026-11-14 批 A3 勘验）**：P0-1 与 P1 已由 v6.3.0 卫生批清偿
> （acpx DataFence/白名单幽灵+盲区四包/doc.go 十包/ARCHITECTURE.md 重写/
> 死导出 4 删/fixverifier 留痕）；P2 十条中 7 条被顺带修掉（P2-1 措辞、
> #12/#14 包文档、#15 docAllowlist 死条目、#17 路径残留、#18 层号、
> P2-6 CONTRIBUTING 注记、P2-7/8 main.go 注释），**本批补齐余 4 条**：
> #11 mcpserver / #13 adapter_remote 包文档（隔开竞争性 package 注释，
> go doc 归位 server.go / remote.go 总述）、P2-9 dup 回查失败 Warn 留痕、
> P2-10 三处 WalkDir 外层错误弃口径注释。本文件为存档，勿再当新发现执行。

- 仓：/Users/zhangyi/my_project/lacus/argus（github.com/yi-nology/argus）
- 日期：2026-11-11　审计口径：只读，零 git 操作
- 方法：`make arch`（test/arch 三守卫）；`go list -f` 全量 import 图与包文档实测；`deadcode -test ./...` 与 `deadcode ./...`（无 -test）双口径；tech-debt.md「复测方法」awk 口径体量榜；全仓 grep（白名单边/ToolErrNote/DataFence/fence/logredact/裸 go/`_ =`）；与 docs/tech-debt.md、ARCHITECTURE.md、CHANGELOG.md 对照。
- 新旧账分界：v5.4.6（2026-11-04 上游追平批）之后引入＝「新债」；tech-debt.md 已有裁定者＝「旧账，勿重开」。

---

## 总览

| 优先级 | 数量 | 说明 |
|---|---|---|
| P0（安全/正确性） | 1 | acpx 适配器 DataFence 防注入旁路 |
| P1（卫生债） | 8 | 白名单漂移、守卫盲区、包文档 10 处、ARCHITECTURE.md 失步、死导出 4 个、fixverifier 吞错、新增超长函数 12 个、新增大文件 13 个 |
| P2（打磨） | 10 | 文档残留、注释措辞、双命名惯例、低危吞错等 |

---

## 1. 分层纪律

### 现状实锤

- `make arch` 三守卫全过：TestLayering / TestBootstrapNotImported / TestPackageDocs PASS。
- 42 个 internal 包 import 图（go list 实测）对照 test/arch/arch_test.go `layering` 白名单：**受约束包零白名单外新边**。
- v6.2.0 `internal/context → internal/argusctx` 改名批：**代码零残留**（生产/测试/Makefile 均无 `internal/context` 引用）；docs 残留 1 处（见 P2-8）；CHANGELOG 历史条目按「历史条目不改写」约定不计。
- 依赖方向无反向：bootstrap 仅被 main.go import；受约束包无「上层被下层 import」；adapter_* → plugin 单向成立。

### 问题清单

**P1-1〔新债〕test/arch 白名单漂移：幽灵条目 + 死条目**
- `test/arch/arch_test.go:82`（dispatch 条目）允许 `internal/agent/agentrun`、`internal/agent/blackboard`、`internal/llm` 三个**已不存在的包**（全仓 go list 无此三包，其职责早已由 dispatch 自身 + 外部 oagentkit/agent 承接）——典型「收编/改名批未同步白名单」漂移，与 context→argusctx 批同类。
- 死条目（允许但实际未 import）：`arch_test.go:102` argusctx 允许 obs（实际未用）、`:82` dispatch 允许 storage（实际未用）、`:85` plugin 允许 learning（实际未用）、`:106` poller 允许 plugin（实际未用）。
- 修正建议：删 3 个幽灵条目；死条目二选一（删，或注明「预留边+理由」）；CHANGELOG 记档。幽灵条目让「封闭允许集」语义失真——门禁只查实到边，漂移不会红。

**P1-2〔新债〕layering 守卫盲区：4 个常驻能力包零约束**
- `internal/commentcmd`、`internal/fixverifier`、`internal/memorykernel`、`internal/writing` 不在 layering 表内（arch_test.go:54 注释自认「不在表内不受 import 方向约束」），但四包均有稳定消费方（service→commentcmd、runner→fixverifier/writing、dream/service→memorykernel），当前实际依赖恰好干净（commentcmd/memorykernel/writing 零 internal，fixverifier 仅 plugin），**纯靠自觉**。
- 修正建议：补 4 行条目（各自允许集=现状快照），与 v3.62.0 建表同法。

**P2-1〔旧账措辞〕** `test/arch/arch_test.go:69` 注释「门禁红至今」——补录语残留，当前门禁绿，改「红至白名单补录」。

### 与 ARCHITECTURE.md 的镜像失步（P1-3，详见 §2 清单 16）

---

## 2. 包文档完整性（42 个 internal 包逐一看 go doc 实测）

TestPackageDocs 只查「doc 非空」，实测文案质量：**13 包的包文档实为单个文件的文件头注释**（go doc 首屏误导，文件一拆一改即失效），1 包真包文档被遮蔽，1 包内容过时，1 处 doc 白名单死条目。

### 「过时/失真包文档」清单（文件:行 + 一句修正建议）

| # | 位置 | 问题 | 修正建议 | 优先级 |
|---|---|---|---|---|
| 1 | internal/argusctx/doc.go:1-2,5 | 写「workcopy（工作副本沙箱）」「实现见 diffprep.go / sdkctx.go / workcopy.go」——workcopy 已收编 oagentkit/tools/workcopy（gitdeepen 包文档同证），本包无 workcopy.go | 删 workcopy 表述，文件清单改 diffprep.go / sdkctx*.go / githubapp.go | P1 |
| 2 | internal/impactview/build.go:1-3 | 文件头注释挂在 `package impactview` 上方，**遮蔽** doc.go:1-17 的真·包文档（go doc 实测显示 build.go 文本） | build.go 头注释与 package 子句间加空行，让 doc.go 生效 | P1 |
| 3 | internal/plugin/completeness.go:1 | 契约单源包（Task/Outcome/Registry/Hook*/fence/词表，13 文件）的包文档只有「完备性裁判契约」一段 | 写「Package plugin …插件契约单源…」总述 | P1 |
| 4 | internal/dispatch/bestofn.go:1 | 引擎面（Dispatcher/Registry/Merger/编队共识）包文档只有 best-of-N 一段 | 「Package dispatch 审查引擎面：分发/熔断/聚合/编队…」 | P1 |
| 5 | internal/service/audit_forward.go:1 | service 门面包文档=SIEM 外送单文件说明 | 「Package service …服务门面（webhook/评论/任务 API/审计）…」 | P1 |
| 6 | internal/runner/inject.go:1 | 编排包文档=注入器单文件说明 | 「Package runner 任务执行编排…」 | P1 |
| 7 | internal/storage/comment_delivery.go:1 | 存储包文档=v3.8.0 评论幂等单文件说明 | 「Package storage gorm 持久化…」 | P1 |
| 8 | internal/semsearch/chunks.go:1 | 已含图面（graph.go/graph_query.go/embed_batch.go，v5.9.0+），文档只有 chunk 抽取 | 补「符号图+语义检索」职责 | P1 |
| 9 | internal/workflow/requirement/anomaly.go:1 | R1-R5 编排包文档=循环异常守卫单文件说明 | 「Package requirement 审查编排 R1-R5…」 | P1 |
| 10 | internal/qa/budget.go:1 | 问答引擎包文档=token 压实单文件说明 | 「Package qa 审查问答引擎…」 | P1 |
| 11 | internal/channel/mcpserver/bridge.go:1 | 包文档=桥接单文件说明（另有 server_handlers/server_tools 等 10 文件） | 总述 MCP 入口适配面 | P2 |
| 12 | internal/commentcmd/catalog.go:1 | 包文档=指令目录单文件说明（实含词法/解析/事件五文件） | 总述词法层 | P2 |
| 13 | internal/plugin/adapter_remote/a2a.go:1 | 包文档=a2a 单文件说明，实际主体在 mcp.go | 总述 remote 适配器 | P2 |
| 14 | internal/bootstrap/assemble.go:1 | 文件框式（内容尚贴切） | 建议改「Package bootstrap」总述形 | P2 |
| 15 | test/arch/arch_test.go:198-200 | docAllowlist 的 domain 条目已死——domain 现有包文档（「Package domain 是 Argus 的领域模型叶子包」），且注释仍写「v3.62.0 时在途，勿代写」 | 删条目 | P2 |
| 16 | ARCHITECTURE.md:28,35,39,47-58（图中 poller :20 与 :30 双列） | 依赖表与白名单失步（repo 自定「互为镜像、不一致即修」）：service 行缺 commentcmd/domain/dispatch/impactview/reposettings/semsearch/writing 且「memory」为幽灵名（应 memorykernel）；workflow/requirement 行缺 codesearch/domain/dispatch/semsearch；reportview 行缺 dispatch；L0 行缺 codesearch/semsearch/goedges；图中 L0「config/obs 零业务依赖」（:39）与 config→learning 矛盾；poller 在图 L2（:20）/L3（:30）双列；「argusctx(sdkctx/ctxfit/diffprep)」（:28）中 ctxfit 实现 v3.149.0 已归 oagentkit/llm/wrap（config.go:126 同证）；「memory(记忆)」（:35）幽灵名 | 按 test/arch layering 现快照重写表与图 | P1 |
| 17 | docs/architecture-deep-dive.md:530 | 残留改名前路径 `internal/context/diffprep.go`（v6.2.0 后应 argusctx/diffprep.go） | 改路径或标注「改名前路径」 | P2 |
| 18 | internal/argusctx/doc.go:1、internal/toolpool/toolpool.go 头 | 「L5」层级编号与 ARCHITECTURE.md 的 L0-L4 体系不一致（一个编号两套语义） | 统一取一套层号 | P2 |

---

## 3. 死代码

### 现状实锤

- `deadcode -test ./...`（tech-debt 既有口径）：**归零**（输出为空）。
- `deadcode ./...`（无 -test，抓「仅测试可达」）：5 个函数。
- 生成代码 biz/model 豁免（口径外）。

### 问题清单

**P1-4〔新债〕导出面整洁：4 个导出渲染函数生产零消费**（v3.9.2 Stop / v3.113「只测试在调的生产平行壳」教训同款）
- `internal/impactview/html.go:30` `RenderHTML`、`html.go:36` `RenderHTMLWithFindings`、`html.go:52` `RenderHTMLWithCompare`、`internal/impactview/mermaid.go:16` `RenderMermaid`——生产唯一渲染入口是 `RenderHTMLFull`（消费点 internal/service/service_impact.go:118）与 `RenderMermaidWithStats`，上述 4 个仅被本包 *_test.go 调用。v5.8.x 图面批（RenderHTMLFull v5.8.2）接管生产面后遗留，属新债。
- 修正建议：转未导出测试助手，或删除并让测试直接走 Full 入口；按「新增导出方法须同步查调用面」教训补一条编目。

**旧账（勿重开）**：`internal/argusctx/sdkctx.go:159` `NewSDKCtxStubbed`——tech-debt「test-only 保留裁定维持」在案。

**方法论边界**：deadcode 不覆盖导出 type/const/var 死字段；「新增导出面增量核对」需 git diff 配合（本次禁 git），本轮以全量 deadcode 双口径替代。

---

## 4. 体量榜（tech-debt.md 复测方法）

### 现状实锤

- 非测试非生成 Go 共 **37,653 行**（v5.1.0 基线 32,457，+5,196）。
- tech-debt.md「剩余 >100 行函数（5 个，均有留痕裁定）」表**已严重过时**：同 awk 口径现测 **>100 行函数 17 个**（另有 type/const/var 边界口径细节与基线一致）。
- 基线 5 个裁定函数现状：`bootstrap.Assemble` 204→**266**（+62）；`config.Load` 137→**161**（+24）；`Dispatcher.Run`→现 `RunWithHook` **133**（dispatcher_run.go:81，Run:75 已退化薄壳）；`JudgeCompleteness` 108→**125**（+17）；`qa.Engine.tools` 108→108（持平）。**旧账但行数漂移**：Assemble +62 后原「拆分负收益」裁定理由建议复核。

### 新增超长函数（>100 行，v5.4.6 之后长出，无裁定）——新债

| 函数 | 行数 | 位置 | 归因 |
|---|---|---|---|
| BuildRepoMap | 207 | internal/impactview/repomap.go:29 | v5.6.x 仓库级代码地图 |
| commands | 204 | cmd/argus-copilot/main.go:69 | v5.3.3 骨架，v5.6-5.7 子命令（share/图谱互跳）堆积越线 |
| scanScripts | 181 | internal/impactview/scriptgraph.go | v5.6.x 非 Go 词法图层 |
| runApp | 147 | main.go:100（基线 90~100 带） | v5.6-6.1 子系统接线后越线 |
| RunWithHook | 133 | internal/dispatch/dispatcher_run.go:81 | 旧账 Run 126 的后身，+7 |
| Build | 131 | internal/impactview/build.go | v5.8-v6.0 图面/历史/对比扩展 |
| buildAffectedPackages | 125 | internal/impactview/build.go | v6.0.0 L2 图优先 |
| JudgeCompleteness | 125 | internal/workflow/requirement/completeness.go | 旧账 108 +17 |
| buildScriptLayer | 120 | internal/impactview/scriptgraph.go | v5.6.x |
| diffFunctions | 119 | internal/impactview/funcdiff.go | v5.4.7/v5.6.4 funcdiff 扩展 |
| CheckCompliance | 114 | internal/workflow/requirement/compliance.go（基线 90~100 带） | 越线 |
| postSummary | 110 | internal/poster/poster.go | 越线 |
| r3Tools | 103 | internal/workflow/requirement/r3tools.go | v5.9.0/5.11.0/6.1.0 工具表堆积 |
| sendReview | 103 | internal/poster/poster.go（基线 90~100 带） | 越线 |
| semsearch.Refresh | 105 | internal/semsearch/semsearch.go | v5.9.0 图重建 |
| config.Load | 161 | internal/config/config.go | 旧账 137 +24 |
| Assemble | 266 | internal/bootstrap/assemble.go | 旧账 204 +62 |

### 新增大文件（基线「剩余较大文件 420~560」表对照）

- **新进大文件（v5.4.6 后长出）**：impactview/html.go 772（v5.7.x 图谱五连/v5.8.x）、impactview/scriptgraph.go 689（v5.6.x）、workflow/requirement/r3tools.go 656（v5.9-6.1）、storage/printalias.go 478（v5.8.2 prints 跨 MR）、qa/qa_tools.go 456（v5.9-6.1 工具 21→23+图工具）。
- **越带（原有文件涨过基线带上限 560 或新进 430+）**：config/config.go 598、storage/store_task.go 578、impactview/build.go 564、workflow/requirement/workflow.go 521、bootstrap/models.go 474、runner/inject.go 456、bootstrap/assemble.go 431、semsearch/semsearch.go 430。现 ≥430 行 15 个，基线仅列 6 个。
- **正面变化（不必重开）**：runner/runner.go 554→376（已拆 runner_*.go）；service/fix.go 已拆至 174（fix_render/fix_sandbox）；mcpserver.New 已拆 server_handlers/server_tools；service_webhook.go 312 稳定（「拆包评估结论」在案勿重探）。

**P1-5〔新债〕**：上表 12 个新增超长函数 + 13 个新增/越带大文件，tech-debt.md 均无留痕。修正建议：按 tech-debt 裁定格式逐个补「留痕裁定或拆分」；重点集中面是 impactview（html/scriptgraph/build 三件 2,025 行）与 requirement/r3tools.go。**P1-6〔旧账漂移〕**：Assemble/config.Load/JudgeCompleteness 三个已有裁定函数增长 17~62 行，复核裁定是否仍适用。

---

## 5. 命名与组织规范

① **文件名-职责一致性**：service 包双惯例并存——`service_*` 13 个（service.go/auth/webhook/taskapi/…）与裸域名 13 个（audit_forward/changelog/cmd_registry/comment_router/describe/fix*/health/recheck/sandbox_verify/testgen/writing_cmd）；runner（runner_* 6 个 + inject.go/writing.go）、storage（store_* + 裸名）、qa（qa_* + budget/ptc）同形。v5.4.6 后新增文件均沿用所在包既有主导风格，**惯例未被新文件破坏**，但历史双轨未收口。〔P2-6〕建议：补一条 CONTRIBUTING 文件名注记（service 门面 service_*、领域子面裸名）即可，不必机械改名。

② **测试文件对应关系**：全部 *_test.go 的 package 声明与所在目录一致（脚本全量核对，0 错挂；0 外部测试包错名）。

③ **导出面整洁**：见 §3 P1-4（4 个生产零消费导出渲染函数）；其余导出 API 零「无任何消费」项（deadcode -test 归零实证）。

④ **注释语言一致性**：≥3 行连续纯英文注释块全仓 **0 个**（脚本扫描非测试生产代码）；中文为主纪律在线。〔无问题〕

---

## 6. 架构规范面

① **组合根纪律**：bootstrap 仅 main.go import（go list+grep 实证），TestBootstrapNotImported 绿；main 只剩进程生命周期。**保持**。

② **internal/plugin 工具信封单源**：
- ToolErrNote（plugin/reminder.go:22）：qa_tools.go:83,111,190、r3tools.go:137,157,531 消费一致，未见手写错误信封旁路。**保持**。
- **DataFence 旁路实锤〔P0-1 新债〕**：`internal/plugin/adapter_acpx/acpx.go:164-177` `buildPrompt`——
  - diff 用手工围栏拼接（`:164` 手写 `"请审查以下 diff：\n```diff\n"`，`:166` `b.WriteString(task.Diff.Text)`，`:170` 手写 `"\n```"`），**未过 plugin.DataFence**；
  - 编队黑板 `task.Artifacts[dispatch.ArtifactSquadBoard]`（半可信 LLM 生成内容）`:173-176` 仅以指令行划界直拼入 prompt。
  - 后果：diff 内容（PR 作者可控）含 ` ``` ` 行即可伪造「数据区结束」，向 CLI agent（codex/claude/kimi）注入任意指令——这正是 DataFence 设计要中和的攻击面（plugin/fence.go:12-17「防止 PR 内容伪造数据区结束」+ argus_fence_neutralized_total 观测）。
  - 三处契约/承诺被违反：ARCHITECTURE.md 架构不变量 6「数据防线上移……单一出处，禁止本地转义克隆」；dispatch/squad_consensus.go:333「adapter 用 DataFence 包裹后注入」；argusctx/diffprep.go:150「文件名理论上可伪造围栏标记，**中和交给 DataFence**」（diffprep 自身不做中和，此承诺在 acpx 路径落空）。
  - 对照组：`internal/plugin/adapter_builtin/builtin.go:351-368` 同场景正确使用 `plugin.DataFence("git diff（已清洗）",…)` + `plugin.DataFence("编队共享黑板…（数据）",…)`，指令放围栏外。
  - 修正建议：acpx buildPrompt 两处改 `plugin.DataFence`（指令留围栏外，与 builtin 措辞对齐）；顺带补中和观测（当前 acpx 路径中和计数恒 0）。

③ **logredact / fence.EscapeUntrusted**：stderr/err.Error() 外送面 logredact.Redact 覆盖一致（ocrshim/run.go:35、dispatch/dispatcher.go:69-71、squad_consensus.go:234-238、adapter_remote/mcp.go、runner/writing.go:328,355、impactview/coupling.go:161 等）；输出面 fence.EscapeUntrusted 覆盖一致（reportview/report.go、health/health.go:161-168、service/service.go:272）。**唯一实质旁路即 ② 的 acpx**。〔P0-1〕

④ **GoSafe goroutine 纪律**：internal 全部 `async.GoSafe`（12 处，poller/bootstrap/dispatch/sink/service）；裸 `go` 仅 1 处 `cmd/argus-copilot/main.go:56`（信号转发→os.Exit，无 panic 面）。〔P2-7〕顺带：main.go:63 注释「裸 goroutine 已全部收编为 async.GoSafe」表述过满（copilot CLI 未收编），建议改「服务进程内已全部收编」。〔P2-8〕

⑤ **错误处理（吞错 `_ =`）新增点位**：
- `internal/fixverifier/fixverifier.go:120`——defer 恢复原文件 `_ = os.WriteFile(...)`：恢复失败静默，工作副本残留 suggestion 修改会污染同任务后续 search_code/图索引语义（正确性）。按 runner_lifecycle「best-effort 写失败留痕」先例（tech-debt v5.4.x 吞错分诊收尾）补 Warn 留痕。〔P1-7 新债〕
- `internal/storage/store_task.go:143`——事务内 `_ = tx.Select("id")…First(&existing).Error`：dup 回查失败静默，dupReason=DupInFlight 但 existingID 空（元数据降级）。建议留痕。〔P2-9〕
- `internal/impactview/repomap.go:148`、`graph.go:42`、`scriptgraph.go:277`——`_ = filepath.WalkDir(...)` 外层错误弃（依赖 walkFn 内部处置，失败即降级面）。建议补注释声明口径。〔P2-10〕
- 惯用形/低风险不计：textutil.TruncRunes 双返回值弃错误（多处）、Close/Release 排除、`json.Unmarshal` 旧数据行回退（store.go:228、reviewtarget.go:50、store_task.go:417,450、fix_render.go:75）、缓存 Set（sdkctx_repoconfig.go:33、runner_prctx.go:154、cmd_registry.go:133）、DROP INDEX IF EXISTS（samesha_guard.go:32,41）、prints.go:60 `withWrite` 闭包恒 nil（逐条 Err 已落 out[i].Err）。tech-debt 吞错分诊既有裁定维持。

---

## 新债 / 旧账对照

| 项 | 新债（v5.4.6 后） | 旧账（已有裁定勿重开） |
|---|---|---|
| 分层 | 白名单 3 幽灵条目+4 死条目；layering 盲区 4 包 | 受约束包零越界边（守卫有效） |
| 包文档 | argusctx 过时文案、impactview 遮蔽、8 包文件框式文档、ARCHITECTURE.md 失步 | docAllowlist domain 条目（v3.62.0 遗物，已该销） |
| 死代码 | RenderHTML×3 + RenderMermaid 生产零消费 | NewSDKCtxStubbed test-only 保留 |
| 体量 | 12 个新增 >100 行函数、13 个新增/越带大文件 | Assemble/Load/JudgeCompleteness/RunWithHook/tools 5 裁定（前 3 有漂移待复核）；service_webhook 拆包不做的裁定；fake provider/withWriteLock 等勿重探项 |
| 规范 | acpx DataFence 旁路；fixverifier 恢复吞错 | 吞错分诊其余项；注释语言/测试对应/组合根均健康 |

## 修正优先级建议（执行序）

1. **P0-1**：acpx buildPrompt 两处改 plugin.DataFence（含黑板），补中和观测。
2. **P1**：白名单清幽灵+补盲区四包；包文档 10 处整改（先 argusctx/impactview/plugin/dispatch/service 五处误导性最强）；ARCHITECTURE.md 依赖表重写；RenderHTML* 收编；fixverifier 恢复留痕；tech-debt.md 补 v6.2.0 复测批（新超长函数/大文件逐个裁定）。
3. **P2**：余下文档残留/措辞/双命名惯例注记/低危吞错留痕。

（完）
