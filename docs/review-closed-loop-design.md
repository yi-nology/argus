# Argus Review 闭环设计 —— 后悔学习环 + 负控自验证门

日期：2026-09-13
状态：**已实现（v3.5.0 闭环 + v3.5.1 活检/dream）**；行为以代码与 CHANGELOG 为准
范围：优先级 1–4 全量（后悔学习环 / 负控自验证门 / 长期记忆 dream / 机制活检）
前置：v3.4.1 已有指纹级 regret 标注与人工 suppress/restore；v3.4.0 已有 R3.5 反思闸门

---

## 0. 为什么是这两条

Copilot 类 reviewer 的天花板在于：**每一轮都从零开始，误报模式不会沉淀，质量门只有
「模型自己觉得没问题」**。Argus 已有半环：

| 已有 | 本设计补全 |
|---|---|
| R3.5 剔除 → `finding_prints.regret_count`，Merger 标注「可信度低」 | 把信号从**单指纹**抬到**仓库级模式**，并驱动下次审查行为变化 |
| 人工 `suppress`/`restore` + 误报回流 Milvus | 结构化 `pattern_signals` 计数 + 置信度阈值（语义与符号双轨） |
| R3.5 Rubric 判「是不是误报特征」 | 要求保留条目附**证据引用 + 反例假设**，缺证据则确定性降级/丢弃 |

目标：从「一次性 reviewer」变成「越用越准的 reviewer」——不是自动 suppress（防误杀红线不变），
而是让高误报模式**发表门槛变高、可信度标注变明显、低价值条目被闸门吃掉**。

---

## 1. 总闭环

```
                    ┌──────────────────────────────────────┐
                    │  下一轮 R4 提示词注入（仓库级）        │
                    │  「历史误报模式 / 高 regret 模式」     │
                    └──────────────▲───────────────────────┘
                                   │
  R4 插件产出 ──► Merger 指纹去重/抑制 ──► R3.5 证据门 ──► R5 裁决 ──► Poster
       │                │                     │                          │
       │                │                     │ drop（证据不足/命中 FP）  │
       │                │                     ▼                          ▼
       │                │              RecordPrintRegret            开发者反馈
       │                │              PatternRegret++              suppress / restore
       │                │                                                │
       │                └──────────── PatternPosted++ ◄──────────────────┘
       │                                     │
       └─────────────────────────────────────┘
                     pattern_signals 表
                     （repo × plugin × pattern_key）
```

两条铁律不变：

1. **持久抑制仍只来自人工** `suppress`/`restore`；学习环只调置信度与发表门槛。
2. **R5 裁决仍不走 LLM**；证据门的丢弃发生在 R5 之前，真值表只读最终 findings 计数。

---

## 2. 条 2：负控自验证门（R3.5 证据协议增强）

### 2.1 定位

**增强现有 R3.5，不另挂独立 verifier agent。** 理由：

- 同一次「终审编辑」调用已覆盖全部 findings，边际成本最低；
- 独立 verifier 每条一次调用，误报率收益有限，成本线性上涨；
- 与「编排确定性」一致：协议与后验校验用 Go 代码，模型只产出结构化字段。

### 2.2 输出协议（模型侧）

在 `reflectFindingWire` 上追加两个字段：

```go
type reflectFindingWire struct {
    ID           int    `json:"id"`
    File         string `json:"file"`
    Line         int    `json:"line"`
    Severity     string `json:"severity"`
    Comment      string `json:"comment"`
    SuggestedFix string `json:"suggested_fix,omitempty"`
    // 新增：保留该条时必须给出的负控材料
    Evidence     string `json:"evidence"`      // 必填（strict/soft）：定位到具体行/符号
    Counter      string `json:"counterexample"` // 必填（strict/soft）：若为误报，最可能原因
}
```

提示词追加纪律（`reflectTask()`）：

```
保留任何条目时必须同时给出：
- evidence：引用变更文件中可核对的具体位置（file 内符号/关键字/行号附近片段，
  须能从【findings】或【变更文件清单】中对应上）；
- counterexample：若本条为误报，最可能的误判原因是什么——给出反例假设后仍确认为真问题。
禁止空 evidence 仍标记为保留。剔除无需写这两个字段。
```

Rubric 增补两条（确定性可判定优先）：

```
8. evidence 为空或与 file/line 对不上（写的是无关文件、或片段在变更清单外）；
9. counterexample 为空——即没有过「反例假设」这一关。
```

### 2.3 确定性后验门（Go 代码，不走 LLM）

解析末稿后、回写 findings 前，对每条**保留**条目执行：

| 检查 | 失败动作（按模式） |
|---|---|
| `Evidence` 空白 | strict：剔除并记 regret；soft：强制降为 `low` + comment 追加「未提供证据引用」 |
| `Counter` 空白 | 同上 |
| `Evidence` 中出现的文件路径不在变更清单且 ≠ 本条 file | strict：剔除；soft：降级 |
| 原有字段校验（file 越界/severity 非法/line 负数/评语清空） | 维持现状：回退原值 |

「空 evidence 一律不删」的旧保底语义在 **strict 模式下收紧**：  
无证据 = 未通过自验证 = 不发表。模型故障/解析失败仍走原有保底（保留原 findings，不因反思崩溃而审查失败）。

### 2.4 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `ARGUS_REFLECTION_ROUNDS` | `0` | 维持现状：>0 才跑 R3.5 |
| `ARGUS_REFLECTION_EVIDENCE` | `off` | `off`：行为同 v3.4；`soft`：缺证据强制降级+标注；`strict`：缺证据剔除并记 regret |

推荐试点路径：`Rounds=1` + `Evidence=soft` → 观察一周误杀/漏杀 → 仓库白名单升 `strict`。

### 2.5 与 regret 的接线

strict 模式下因证据门剔除的指纹：

- 照旧进 `ReflectSummary.RemovedPrints` → `RecordPrintRegret`；
- 同时 `PatternRegret++`（§3）——「说了但举不出证据」是强误报信号，权重大于普通 R3.5 Rubric 剔除。

soft 模式只降级不剔除：**不**记 regret（意见仍在报告里，只是标了「未验证」）。

### 2.6 可观测

| 指标/日志 | 含义 |
|---|---|
| `argus_reflect_dropped_total{reason=evidence\|counter\|rubric}` | 证据门丢弃原因分布 |
| `argus_reflect_degraded_total{reason=evidence\|counter}` | soft 模式降级条数 |
| `ReflectSummary.DroppedNoEvidence int` | 写入任务 result/debug，GET /tasks 可查 |
| 报告脚注 | 「R3.5：…；证据门丢弃 N 条 / 降级 M 条」 |

---

## 3. 条 1：后悔学习环（模式级）

### 3.1 与现有指纹级 regret 的关系

| 层 | 键 | 用途 | 状态 |
|---|---|---|---|
| 指纹级 | `platform/owner/repo/number/print` | 单条意见跨轮身份；suppress 实体 | 已有 |
| **模式级** | `platform/owner/repo/plugin/pattern_key` | **同类意见跨 PR 沉淀；驱动下次行为** | **本设计新增** |

指纹绑定单 PR；学习必须跨 PR。故新增 `pattern_signals`，粒度 = **仓库 × 插件 × 模式**。

### 3.2 pattern_key 派生（确定性）

```
normalize(comment):
  - Unicode NFKC
  - 去掉行号、十六进制短码、路径段、纯数字、反引号内容中的标识符
  - 折叠空白；截断至 200 rune
  - 小写

pattern_key = sha256_hex16( plugin + "\x00" + normalize(comment) )
pattern_text = normalize 后的前 160 rune（人读样本，入库便于审计）
```

约束：

- **不含 file/line**——同一类问题在不同文件应聚成同一模式；
- **含 plugin**——builtin/security 与外部 OCR 对同类话术的信任历史不应混用；
- 不对 comment 做 LLM 摘要——模式键必须可离线复算、可单测、可对账。

### 3.3 表结构

```go
// internal/storage/store.go
type PatternSignal struct {
    ID uint `gorm:"column:id;primaryKey"`

    Platform string `gorm:"column:platform;size:16;not null;uniqueIndex:idx_pattern,priority:1"`
    Owner    string `gorm:"column:owner;size:128;not null;uniqueIndex:idx_pattern,priority:2"`
    Repo     string `gorm:"column:repo;size:128;not null;uniqueIndex:idx_pattern,priority:3"`
    Plugin   string `gorm:"column:plugin;size:64;not null;uniqueIndex:idx_pattern,priority:4"`
    Pattern  string `gorm:"column:pattern_key;size:32;not null;uniqueIndex:idx_pattern,priority:5"`

    PatternText string `gorm:"column:pattern_text;size:255"` // 审计可读样本

    Posted    int `gorm:"column:posted"`     // 成功进入报告的次数（入库后、回帖成功）
    Regret    int `gorm:"column:regret"`     // R3.5 剔除（含证据门 strict 丢弃）
    Suppress  int `gorm:"column:suppress"`   // 人工 suppress
    Restore   int `gorm:"column:restore"`    // 人工 restore（撤销误抑制）

    FirstSeenAt time.Time `gorm:"column:first_seen_at"`
    LastSeenAt  time.Time `gorm:"column:last_seen_at"`
    CreatedAt   time.Time `gorm:"column:created_at"`
    UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (PatternSignal) TableName() string { return "pattern_signals" }
```

MySQL schema.sql 同步补 `pattern_signals`，并**补上 GORM 已有但 schema 落后的 `finding_prints.regret_count`**（现 schema 缺列，新环境灌库会丢列）。

### 3.4 置信度（确定性，可单测）

Beta 先验 `(α=2, β=2)` → 初始 0.5。信号更新：

```
// 每次 Posted：α += 1          （观点发出去了，弱正样本）
// 每次 Regret：β += 2          （终审判误报嫌疑，强负样本）
// 每次 Suppress：β += 3        （人工判死，更强负样本）
// 每次 Restore：α += 2; β = max(β-2, 1)  （撤销误杀）
confidence = α / (α + β)   ∈ (0,1)
samples    = Posted + Regret + Suppress
```

实现：表里存计数，**confidence 读时算**（不落列，避免漂移与并发更新冲突）。
衰减（可选 v1.1）：超过 `ARGUS_REGRET_HALF_LIFE_DAYS` 未更新的信号按半衰期把额外计数折回先验——v1 不做，先用硬计数。

### 3.5 消费点（让学习「改变行为」）

学习环没有消费点就只是记账。三个确定性消费点：

#### A. Merger：模式级可信度标注（替代/并入现有指纹标注）

```
prior 指纹标注逻辑保留（跨轮复现 / 指纹 regret）；
额外：若 pattern.signals ≥ MinSamples 且 confidence < SoftFloor：
  comment += "（本仓库同类意见历史置信度低）"
```

#### B. Merger：低价值条目闸门（默认只动 low）

```
if confidence < HardFloor && severity == low && samples ≥ MinSamples:
  drop；计入 Degraded 注明「学习环抑制：模式置信度 X」
```

**不 drop medium/high**——高价值误报要靠证据门和人工，不能被历史统计一票否决。
与人工 suppress 的关系：suppressed 指纹在 UpsertPrint 处剔除，优先于模式闸门。

#### C. R4 前注入（提示词层）

Runner/Workflow 在 R4 前拉取本仓库 `confidence < InjectFloor` 且 `samples ≥ MinSamples`
的 Top-N（默认 10）模式，写入：

```go
task.Artifacts[plugin.ArtifactReviewHints] = ...
// 内容示例：
// 【本仓库历史高误报模式，请提高举证标准】
// - 「…未处理错误返回…」（插件 security，置信度 0.18，suppress 4 次）
// - 「…建议提取常量…」（插件 style，置信度 0.25，regret 6 次）
// 命中类似表述时：给出具体行与影响，否则不要输出。
```

builtin Reviewer / acpx 适配器拼提示词时消费该 Artifact（与既有 `ArtifactReviewGuidelines` 同路径）。
**不在 Dispatcher 里按模式禁用整个插件**——粒度太粗，且违背「仓库配置只能选不能增」的信任边界。

### 3.6 信号写入点

| 事件 | 动作 | 代码落点 |
|---|---|---|
| R3.5/证据门剔除指纹 `fp` | `PatternRegret(plugin, normalize(comment))` | `runner` 记 `RecordPrintRegret` 处并行 |
| Merger 确认进入报告（未 suppressed） | `PatternPosted++` | `UpsertPrint` 返回后；或 Poster 成功后（v1 取入库点，简单且覆盖 stale） |
| `suppress` 成功 | `PatternSuppress++` + 既有 Milvus 回流 | `service.SuppressFinding` / 评论指令路径 |
| `restore` 成功 | `PatternRestore++` | `service` restore 路径 |

写失败只告警，不阻塞主流程（与现有 regret 入库一致）。

### 3.7 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `ARGUS_REGRET_LEARNING` | `0` | 总开关；0 时全部消费点旁路（表可仍写入以便预热） |
| `ARGUS_REGRET_MIN_SAMPLES` | `3` | samples 低于此不消费（防小样本抖动） |
| `ARGUS_REGRET_SOFT_FLOOR` | `0.35` | 低于此打「置信度低」标注 |
| `ARGUS_REGRET_HARD_FLOOR` | `0.20` | 低于此 + low severity → 丢弃 |
| `ARGUS_REGRET_INJECT_FLOOR` | `0.35` | 低于此进入 R4 提示词 Top-N |
| `ARGUS_REGRET_INJECT_TOP_N` | `10` | 注入条数上限 |

### 3.8 信任与安全

- pattern 信号只来自 **Argus 自身产出 + 平台人工反馈指令**，不读工作副本文件当配置；
- 注入提示词经 `safejson.EscapeUntrusted`（comment 半可信）；
- 仓库级隔离：查询/写入均带 `platform/owner/repo`，不跨仓库平均；
- 不把 learning 结果写回 `.reviewagents.yaml`（仓库配置信任边界不变）。

---

## 4. 数据流串联（单任务时序）

```
1. Runner 组装 Task，拉取本仓库 pattern 提示词 → Artifacts[review_hints]
2. R4 fan-out → findings（各插件）
3. Merger：
     - 指纹去重 / suppressed 剔除（现状）
     - 模式闸门（HardFloor × low）丢弃
     - 模式低置信标注
     - UpsertPrint → PatternPosted（若未 suppress）
4. R3.5（Rounds>0）：
     - 模型输出含 evidence/counterexample
     - 确定性证据门：strict 剔除 / soft 降级
     - RemovedPrints → RecordPrintRegret + PatternRegret
5. R5 真值表（只读最终 findings）→ Poster
6. 事后：suppress/restore 指令 → PatternSuppress/Restore + 既有知识回流
```

---

## 5. 默认行为与回退

| 场景 | 行为 |
|---|---|
| 两开关全关（默认） | 与 v3.4.2 完全一致：无证据字段要求，无模式消费 |
| `REFLECTION_EVIDENCE=soft` + `REGRET_LEARNING=1` | 证据不足降级标注；模式只标注+提示词注入，不 drop |
| 全开 + `strict` | 证据门可剔除；low×低置信可丢；medium/high 仍不被学习环丢弃 |
| DB 迁移 | AutoMigrate 新表；SQLite 开发库无痛；MySQL 可选灌 `deploy/migrations/schema.sql` 初始化（启动时 AutoMigrate 会自动补齐缺失部分） |
| 回滚 | 关 env 即可；表保留不丢数据 |

---

## 6. 测试计划（负控优先）

### 6.1 pattern_key

- 同文案不同行号/文件/十六进制 → 同 key；不同 plugin 同文案 → 不同 key；
- 中英文空白折叠、反引号标识符剥离的表驱动用例。

### 6.2 置信度

- 仅 Posted → 缓升但不过 SoftFloor（小样本不消费）；
- Regret×2 + Suppress×1 → confidence 下穿 HardFloor；
- Restore 后回抬；`MinSamples` 以下始终不消费（负控）。

### 6.3 证据门

- 保留条无 evidence → strict 剔除且 regret；soft 降级且不记 regret；
- evidence 指向清单外文件 → strict 剔除；
- 模型故障/解析失败 → 原 findings 保留（保底不回归）；
- 剔除后 R5 基于修订后计数求值（既有 reflection 测试扩展）。

### 6.4 端到端（e2e mock）

- 两轮 mock 任务：第一轮产出并 regret → 第二轮同模式被标注/（low）丢弃；
- suppress 注入后下一轮该指纹与模式均不再发表。

### 6.5 回归

- 开关默认关闭时，全量既有测试零修改通过。

---

## 7. 实现切片（建议 PR 顺序）

| PR | 内容 | 可独立合入 |
|---|---|---|
| PR-1 | `pattern_key` 工具 + 单测 | 是 |
| PR-2 | `pattern_signals` 表 + 信号写入 API + schema.sql（含 regret_count 补列） | 是（只记账） |
| PR-3 | R3.5 证据协议 + 后验门 + 指标 + 测试（默认 `off`） | 是 |
| PR-4 | Merger 模式标注/low 闸门 + R4 hints 注入 + env | 是 |
| PR-5 | e2e 闭环 + 文档/CHANGELOG/架构深读 §15 | 收尾 |

---

## 8. 与优先级 3/4 的挂点（不在本设计实现）

| 后续 | 挂点 | 状态 |
|---|---|---|
| 长期记忆 dream | `pattern_signals` 即结构化长期记忆；dream = 周期把低置信模式写入 knowledge / Milvus，并对 `pattern_text` 做去重合并 | v3.5.1 实现 |
| 活检探针 | `argus_layer_exec_total{layer=...}`；告警：`increase==0` 且有 done 任务 → 层静默失效；证据门长期零动作告警 | v3.5.1 实现 |

---

## 9. 决策记录

| # | 决策 | 备选 | 理由 |
|---|---|---|---|
| D1 | 增强 R3.5，不引入独立 verifier agent | 每 finding 一次 verifier 调用 | 成本线性、架构更简单；证据协议已能压误报 |
| D2 | 学习环只调门槛/标注/注入，不自动 suppress | 高 regret 直接 suppress | 沿用「持久抑制必须人工」红线，防自动化误杀 |
| D3 | 模式键确定性 normalize，不用 LLM 摘要 | embedding 聚类 | 可审计、可对账、零额外成本；语义泛化交给已有 Milvus 路径 |
| D4 | HardFloor 只丢 low | 丢 medium | 误杀 medium/high 的代价不对称 |
| D5 | confidence 读时计算，表内只存计数 | 持久化 confidence 列 | 避免并发更新漂移与公式变更迁移 |
| D6 | 默认全关，opt-in | 默认开 | 与 R3.5/编队/router 一致：强动作显式打开 |

---

## 10. 差异化一句话

Copilot 没有跨轮可学习的审查指纹，也没有「无证据不得发表」的负控终审；  
Argus 补齐 1+2 之后，误报会变成可计数、可衰减、可注入的仓库级记忆，而高价值意见必须先通过证据门。
