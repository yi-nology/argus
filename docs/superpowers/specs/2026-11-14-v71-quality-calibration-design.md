# Argus v7.1 质量校准演进路线 — 设计 spec

- 日期：2026-11-14
- 状态：已获用户批准（设计六节 yes），待实施计划
- 依据：第五轮技术评审（基准 `eef394d8`，对比 `54b3335`，48 提交）
- 上游清单：`docs/execution-13items.md`（批 A ✅、批 B ✅ 2026-11-14 收官——终轮基线 F1=1.000 归档，A/B 对照已用旧口径跑完）——本 spec 与之**合并为新总纲**，见 §1

---

## 0. 背景：六项评审主张核实结论

本 spec 的事实基础来自对评审六项主张的逐条源码核实（2026-11-14，三路并行探查）：

| # | 主张 | 判定 | 关键证据 |
|---|---|---|---|
| 1 | 预留金额与执行上限脱节 | 属实 | `internal/storage/budget_reserve.go:173-187` 截断后的 `hold` 只落库不回传；`ReserveResult`（:109-114）无授予字段；`internal/runner/runner.go:232-237` 用原始 `knobs.taskTokenBudget` 建 Budget 且**建在闸之前** |
| 2 | 账本幂等键不稳定 | 属实 | `internal/runner/runner_usage.go:40-42` 记录时新铸 uuid；上游 `UsageRecord` 无 ID 字段、eino `RunInfo.Name` not unique、`UsageCallCounter` argus 侧零使用——无稳定 ID 可贯穿；文件头注释（:26-29）自相矛盾 |
| 3 | Precision 系统性高估 | 属实 | `internal/evalq/evalq.go:198` `if clean || forbidden`；`evalq.go:226` FPRate 分子=意见数、分母 TN=样例数单位混用；`evalq_test.go:43-45` 钉死"非 clean 多报不计 FP"为预期 |
| 4 | Evidence 验证深度不足 | 属实 | `internal/plugin/evidence.go:88-125` 仅三项集合级校验、刻意无文件 I/O（包头 :13 纪律）；纯文字→`Verified=true`（:95-98）；校验结果不回写 schema，仅追加 ⚠️ 文本 |
| 5 | Helm 分离未生效 | 部分属实 | Job 侧 `ARGUS_MIGRATE_MODE=job` 已落地（`migration-job.yaml:39-43`）；**Deployment/values 均未设 verify，业务 Pod 缺省 `""` 仍执行启动期迁移**（`migrate_mode.go:21-29`、`store.go:162-176`）；`values.yaml:70-73` 注释与实现不一致；双探针指 `/readyz`，`/healthz` 现成（`main.go:251`）未用 |
| 6 | 修复治理待产品化 | 属实 | `f.Verified` 门在（`fixverifier.go:157-160`）；上游 `CreateCROptions`（ggp v0.134 `options.go:51-60`）**无 Draft 字段**；凭证同源未分离（`assemble.go:96-104`）；分支按 taskID（`writing.go:322-325`）每轮审查各开一个修复 PR、无收敛；无状态机 |

**评审两处不精确，本 spec 按核实结果纠偏：**

1. **fail-open 只针对"判定不可得"**（存储/租约故障，`runner_lifecycle.go:60-63`）；额度不足本来就是 fail-closed（:64-81 落 failed 终态）。`ARGUS_BUDGET_ENFORCEMENT` 只需覆盖前者，不改既有语义。
2. **探查副发现**：`deploy/migrations/schema.sql` 与 `autoMigrateModels()` 漂移——缺 `usage_events`/`budget_reservations`/`repo_settings`/`outbox_events` 四张表 DDL。纳入 Phase 1。

---

## 1. 总纲：路线图与批次归属

### 1.1 与 13 项清单的关系（判断 1，可推翻）

**合并为新总纲**：本演进路线成为主清单，`execution-13items.md` 批 A~G 折入其批次。理由：① 批 B 与评审第一期都动 `evalq`，必须串行（保 A/B 对照变量单一）；② 同仓多线交错提交事故在案；③ 一份清单防漂移。

### 1.2 阶段表

| 阶段 | 内容 | 关键依赖 | 估时 |
|---|---|---|---|
| **批 B 收尾** ✅ | 治理后 A/B 对照跑完 + 趋势面实测（B2）+ 文档回写（B3/B4）——2026-11-14 完成（终轮 F1=1.000，3 commit） | 无 | 已完成 |
| **Phase 1** 预算与语义修复 | ①GrantedTokens 接线+enforcement ②评测口径三分 ③稳定 invocation_id ④Helm verify+探针分离 ⑤schema.sql 漂移门禁 | 批 B 收尾（都动 evalq） | 1–2 周 |
| **批 C** 容器级 chaos | testcontainers pg/MySQL 故障注入 + 门禁（原 13 项批 C） | Phase 1（storage 文件面交集） | bounded 大 |
| **批 D** 长函数全拆 | 重扫真清单→全拆 + 覆盖率 40%（原 13 项批 D，A 方案已拍板） | Phase 1（避免拆了又改） | bounded 大 |
| **Phase 2** Evidence v2 | 结构深度校验（本地可做）+ 真实 PR 基准（需 43 现场窗） | Phase 1；基准需现场 | 2–3 周 |
| **Phase 3** Verified Fix v2 | 按 PR 收敛 + 状态机 + 写凭证分离（Draft 等上游） | Phase 1 | 2–3 周 |
| **Phase 4** 质量运营 | 反馈回流 + 维度趋势；批 E/F/G 出独立 spec | Phase 2/3 | 持续 |

排序说明：批 C 与 Phase 2 结构部分文件面不相交（`storage/test` vs `plugin/domain`），实际可并行——表中给串行基线，实施时按窗口灵活调。**Phase 1 是全线硬前置**：口径不修，Phase 2 真实 PR 基线报出的 Precision 不可信。

### 1.3 Phase 1 内部顺序

批 B 收尾 → Phase 1（五个子项可按 ④Helm → ⑤schema → ①预算 → ③幂等 → ②评测 的风险递增序，② 最后动以缩短 evalq 冻结窗）→ 三门禁 → 重跑新口径基线。

---

## 2. Phase 1 详细设计

### 2.1 预算强制（GrantedTokens + enforcement）

**原则**：修三处接线，不动预留机制本身——活跃预留（`budget_reservations` 占位 + used 聚合）已保证并发不超卖；**不做**"每次模型调用前查账本"（YAGNI，预留已覆盖）。

1. **`ReserveResult` 增 `GrantedTokens int64`**（`budget_reserve.go:109-114`）：回传已落库的 `hold`（:173-187 截断值）。语义：`<=0` = 无界/不约束（沿用现"任务预算非正：不占预留直通"语义）。
2. **`budgetGate` 回传授予值 + 闸提前**：
   - `runner_lifecycle.go:48` 签名定死 `bool` → `(allowed bool, granted int64)`（不返回整个 `ReserveResult`，保持闸的职责单一）；
   - `runner.go:232-257` 调整顺序：**先闸后建**——`NewTokenBudget(granted)` 替代 `NewTokenBudget(knobs.taskTokenBudget)`；
   - 闭环语义：DB 承诺 5,000 → 任务 `Budget.Remaining()` 上限即 5,000，评审指出的"账面 5,000 实跑 20,000"洞闭合。
   - `knobs.taskTokenBudget` 其余消费点（预算闸输入 `runner_lifecycle.go:56`）不变。
3. **`ARGUS_BUDGET_ENFORCEMENT=soft|hard`**（`config.go` 新字段，解析只认两值，非法=soft+warn）：
   - **soft（缺省）** = 现状：判定不可得 → fail-open 放行；额度不足 → fail-closed（不动）；
   - **hard** = 判定不可得也 fail-closed：任务落 `failed` 终态，reason `budget_ledger_unavailable`。

**测试（TDD）**：红→绿①授予值传导（预留截断后 Budget 上限=granted）②hard 拒绝路径；保留 `runner_budget_test.go:132-142` `TestBudgetGateFailOpenOnStorageError` 作为 soft 回归钉。

### 2.2 评测口径三分

**原则**：不把 unlabeled 塞进 FP（未判定 ≠ 误报），但绝不静默丢弃；指标改名去"标准 Precision"的冒充。

1. **`CaseResult` 增 `Unlabeled []int`**（`evalq.go`）：非 clean 样例上未匹配、非 `must_not_find` 的 Finding 显式入列。
2. **指标三分**：
   - `Precision` 全面改称**受限标注口径 Precision**（report 表格 `report.go:142-146`、JSON 注释 `evalq.go:129-136`、`docs/eval-baseline.md`、包文档 `evalq.go:12-16` 同步）；
   - 新增 `unlabeled_count`（report + JSON）；
   - 新增 `clean_case_fp_rate` = **有 FP 的干净样例数 / 干净样例数**（样例单位，真正的"干净 PR 误报发生率"）；旧 `fp_rate` 保留现公式标 `deprecated`（`/console` 趋势面在读，不破坏历史曲线），下个基线周期淘汰。
3. **消费方同步清单**（探查实锤）：`evalq.go` / `report.go:142-146,198` / `service_console.go:197-226` / `qconsole/render.go:63-67` / `qconsole/qconsole.go:63-70,356-363` / `docs/eval-baseline.md`。
4. **口径切换点协议**：Phase 1 完成后用新口径**重跑一次基线**作为验收，归档标注"此处口径切换，前后数字不可直接比"——否则趋势断层无法解读。
5. **与批 B 衔接** ✅：A/B 对照已用旧口径跑完（对照变量单一，2026-11-14
   收官归档）——Phase 1 现在可以动 `ScoreResult`。

**测试**：unlabeled 计数用例、`clean_case_fp_rate` 单位用例（分子分母同为样例数）、既有 `TestScoreResultFindingMatching` 断言改写（多报进 Unlabeled 而非丢弃）。

### 2.3 稳定 invocation_id

**风险先行**：上游实锤无稳定 call ID（`UsageRecord` 无 ID、`RunInfo.Name` not unique、`UsageCallCounter` argus 侧零使用、`Iteration` 恒 0）。

1. **主路（argus 本地）**：接上游已有 `WithCallCounter` 使 `UsageRecord.Iteration` 有值，稳定键 = `u-<taskID>-<attempt>-<iteration>`，**调用发起前定型**、ctx 贯穿到 `recordUsageEvent`（`runner_usage.go:40-42` 不再现场铸 uuid）。
   - **实施前置探针**：先写测试验证 counter 粒度语义（并发 client、`generateRetry` 重试环下是否稳定、是否 per-物理调用递增）——本项唯一技术风险点。
2. **兜底**：counter 语义不满足 → 保持现铸 uuid，**修掉 `runner_usage.go:26-29` 自相矛盾注释**，端到端幂等明确降级为"依赖上游 CallID"，同步提 oagentkit issue。
3. **副产物（schema 漂移门禁）**：补 `deploy/migrations/schema.sql` 四张表 DDL（`usage_events`/`budget_reservations`/`repo_settings`/`outbox_events`）+ 门禁测试钉"schema.sql 表清单 == `autoMigrateModels()` 清单"，防再漂移。

**测试**：同 invocation_id 重放收敛（既有 `runner_usage_test.go:141-166` 保留）+ 新增"同物理调用重复投递撞唯一索引"用例（主路落地后）+ schema 清单一致性门禁。

### 2.4 Helm verify 默认 + 探针分离

1. **Deployment 注入 verify**：`migration-job.enabled=true` 时模板自动注入 `ARGUS_MIGRATE_MODE=verify`——**仅当 `.Values.config` 未显式设置该键时**（避免覆盖用户手配；env 列表后者覆盖前者的 K8s 语义要利用好）。同步修正 `values.yaml:70-73` 注释与实现不一致。
2. **探针分离**：liveness → `/healthz`（进程级零依赖，`main.go:251` 现成），readiness 保持 `/readyz`——消除"DB 抖动重启正常进程"。
3. **同步手工清单**：`deploy/k8s43/50-deployment.yaml` 同改（只改文件不部署，现场搁置照旧）。
4. **护栏**：批 A 已建 `ci.yml` helm job 补两条断言——verify env 注入（enabled 双态）、探针路径。

### 2.5 验收（Phase 1）

- 预算不能被在途任务突破（测试证明：granted 传导 + hard 拒绝）；
- 评测指标不再系统性高估（unlabeled 显式暴露、Precision 标注受限口径、FPRate 拆分）；
- 业务 Pod 缺省零 DDL（helm template 断言 verify 注入）；
- 稳定键或兜底注释修正落地（探针结论决定走哪条）；
- `make lint && make test && make arch && make eval` 全绿 + 新口径基线归档。

---

## 3. Phase 2 骨架（Evidence v2）

- **纯函数纪律演进**：核心保持纯、I/O 显式注入——新增 `SourceReader` 接口（按 path+SHA 读行），装配侧从 workcopy 提供实现。纪律从"零 I/O"推进到"IO 边界显式化"，不破坏。
- **四态枚举回写 schema**（`domain.Finding` 增 `EvidenceStatus`）：
  - `invalid`：解析失败 / 行号越界（如 `:999999`）/ 文件不存在；
  - `unverified`：行号越界或引用片段与实际代码行不匹配 / 纯文字无引用（**纯文字不再叫 verified**，报告措辞"未提供结构化证据"）；
  - `structurally_valid`：文件 + 行号 + 片段全对（语义未验）；
  - `verified`：structurally_valid + commit SHA 对齐（引用行在 task 对应版本真实存在且内容匹配）。
- **降级规则沿用但按新状态**：`invalid|unverified` 的 critical/high→medium（退出 R5 否决面，`workflow.go:321-324` 接线点不变）；`structurally_valid` 放行。
- **契约纪律（v6.6.0 教训）**：Finding 加字段须同步四处契约（README§3 / SKILL.md / tools.md / ide-mcp）+ IDL `findings.thrift` + DTO 出口（`service_agents.go:194-201` findingDTO）+ 计数 grep 清扫（版本·计数·改名·平台·语言五词）。
- **真实 PR 基准**：依赖 43 现场数据，独立小批（`execution-13items.md` 已注明回公司后）；人工裁决样例回填后才报"完整口径 Precision"。
- **验收**：能明确区分引用合法 / 证据有效 / 结论成立；路径存在性不再冒充事实证明；testdata 夹具覆盖行号越界、片段不匹配、SHA 对齐三类用例。

## 4. Phase 3 骨架（Verified Fix v2）

- **按 PR 收敛**（最痛一件）：分支名 `argus/fix/<prNumber>` 替代 taskID；`openFixPR`（`writing.go:420-458`）前查同目标 PR 的既有 fix PR——存在则更新分支+回帖，不新开。回帖标记 `argus:fix_cr`（:474）已有 task_id，补 pr 索引可读。
- **状态机最小版**：fix 实体状态 `pending→pushing→pr_created→closed/failed`；推送前检查取消标记（可取消）；failed 走既有重试机制（`models.go:56-60` Attempt/NextRetryAt）；回滚=关 PR（现状，文档化）。
- **写凭证分离（argus 本地可做，不依赖上游）**：`Platforms[].WriteToken`（缺省回落 `Token`），`assemble.go:96-104` 的 `CredentialOf` 与 push 链路优先取写 token。附带核查探查隐患：push 走裸 `git push`（`writing.go:384`）未见凭证注入，依赖环境 git 配置。
- **Draft 依赖上游**：提 ggp issue（`CreateCROptions.Draft`），等版期间修复 PR 维持非 Draft + 防循环护栏（`isFixBranch` 已有）。**反直觉约束**：argus 自身 `SkipDraft=true`（`config.go:482`），修复 PR 若改 Draft 反而会被自己跳过——上游支持后需连带处理 SkipDraft 对 fix PR 的豁免。
- **验收**：自动修复可审计、可取消、可重试、可回滚，不绕过分支保护；同一 PR 多次审查不产生多个待处理修复 PR。

## 5. Phase 4 骨架（质量运营与自学习）

- 人工采纳 / 驳回 / 修复 / 回归结果回流评测与审查策略（批 E 组织级学习环折入）；
- 按仓库 / 语言 / 模型 / 插件维度质量趋势（消费 Phase 1 的口径三分 + Phase 2 的 Evidence 状态）；
- 批 F 需求追溯、批 G 多模态届时各出独立 spec（沿用 13 项清单既有约定：E/F/G 三批各出独立 spec 后入总清单状态列）；
- **验收从"报告数量"转向可证伪指标**：误报率降低、有效发现率提高——能证明而非只是报告。

---

## 6. 测试与验收总口径

每阶段完成 = 阶段内明细全勾 + 门禁绿：

```
make lint && make test && make arch   # 三门禁
make eval                              # 涉评测阶段加
```

- 涉 helm 改动：`ci.yml` helm job（lint + template 双路径）必须含该阶段新增断言；
- 全部 TDD：先红后绿；Phase 1 四子系统各自独立 commit（按单拆，精确点名 `git add`——在途文件教训在案）；
- push 收尾：双 remote 回读 + tag 对账（v6.9.0 丢 origin 教训）。

## 7. 风险登记

| 风险 | 缓解 |
|---|---|
| 上游 ggp Draft / oagentkit CallID 节奏不受控 | 本地 fallback 已设计（凭证分离本地做、幂等兜底路径已定），不阻塞主线 |
| 评测口径切换致 `/console` 趋势断层 | 归档标注切换点 + 新口径重跑基线；旧 `fp_rate` 保留 deprecated 一周期 |
| `WithCallCounter` 粒度不满足幂等 | 探针测试先行决定主路/兜底；兜底不改行为只修注释+提上游票 |
| 批 B 与 Phase 1 顺序被抢跑 | §1.2 依赖表钉死：批 B 未收尾不动 `ScoreResult` |
| 同仓多线交错提交 | 本 spec 即新总纲，单线推进；commit 精确点名 |
| 43 现场依赖（真实 PR 基准、部署） | Phase 2 拆"本地结构部分/现场基准部分"两半，现场部分独立小批 |

## 8. 明确不做（YAGNI）

- 每次模型调用前实时查月度账本（活跃预留已覆盖超卖面）；
- Evidence 符号/调用链核验（留 Phase 4，依赖图谱数据新鲜度）；
- 修复 PR 产品级审批流（平台侧分支保护为最终控制点，Argus 不越位）；
- v8 / 新 Agent / 可视化特效（评审结论：先校准再扩展）。
