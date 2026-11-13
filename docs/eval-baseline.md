# 真实模型质量评测体系（P2-02，issue #25）

> 状态：骨架 + 本批补齐（分严重度报表聚合、起步集 `eval/baseline/go.yaml`、
> 真实模型单发审查 Reviewer 与 `make eval-baseline-real` 跑批入口；首跑基线
> 数值见文末「完成度边界」）+ **#28 四语言起步集扩容**
> （`{go,java,python,jsts}.yaml` 各 11 条共 44 条，门禁 `TestBaselineDataset`）。
> 关联：golden eval（`make eval` / `make eval-real`，v3.10.0 起）是编排语义与
> verdict 命中率面——本体系补 finding 级混淆矩阵与裁决稳定性；现网已裁决 MR
> 样例回填仍待数据（#28 余量）；质量趋势可视化消费面见 P3-02（#27）。

## 指标口径（单源 `internal/evalq`）

| 指标 | 定义 | 口径说明 |
|---|---|---|
| Precision | TP/(TP+FP) | finding 级 |
| Recall | TP/(TP+FN) | finding 级 |
| 误报率 FPRate | FP/(FP+TN) | clean 基准（must_find 空）上报意见全部计 FP——golden 北极星口径延续 |
| 漏检率 MissRate | FN/(TP+FN) | 金标准有缺陷而模型未报（含 known_miss 正例的漏报） |
| F1 | 2PR/(P+R) | — |
| VerdictAccuracy | 命中数/样例数 | golden eval 原口径，保持历史可比 |
| 裁决稳定性 | k 轮逐案一致率均值 | `Stability(runs)`：逐案取众数占比，对全部案取均值；1.0 = k 轮全同 |

匹配规则：`must_find` 项按 kind（大小写不敏感）为主键、file（可选）收窄命中；
`must_not_find` 命中的实际上报意见计 FP；评测出错的样例保守计分（verdict
不命中 + 记漏检），`errored` 单列——评测失败也是数据。

## 基准集装载（`eval/baseline/`）

一语言一文件（`<language>.yaml`），文件名 = `language` 字段（双处漂移硬门禁）：

```yaml
language: go
description: Go 语言审查基准（现网在役仓已裁决 MR 样例）
cases:
  - id: go-001
    repo: acme/pay-gateway        # 样例来源仓（现网在役）
    title: 批量写无事务包裹
    label: standard               # standard | known_miss | known_fp
    source: MR !112               # 已裁决 MR 出处（审计可回溯）
    diff: |
      --- a/ledger.go
      +++ b/ledger.go
      ...
    expect:
      verdict: request_changes
      must_find:
        - file: ledger.go
          kind: missing-tx
          severity: high
      must_not_find: []
```

（上例是**字段形态说明**——`repo`/`source` 指向现网已裁决 MR 的目标口径，该
形态的样例归 P3-03 #28；当前实际起步集的出处口径见下。）

标签语义：`known_miss` = 金标准有缺陷且历史模型漏报（漏检正例，必须有
must_find）；`known_fp` = 历史模型在干净 diff 上报出的意见（误报正例，
必须有零 must_find）。`Validate()` 硬校验 id 唯一 / label·verdict 词表 /
标签结构约束 / diff 非空。

**起步集（#25 交付 + #28 四语言扩容，两批合流）**：`eval/baseline/{go,java,python,jsts}.yaml`
四语言各 11 条共 44 条样例——各语言独立混合 standard/known_miss/known_fp、
known_miss/known_fp 正例各 ≥1、high 严重度锚齐，分仓 ≥3。
**出处口径**：全部为合成夹具（go 为 golden 08/09/10/29/30 改写 + 自编写；其余
三语言为自编写现实形态 diff），`source` 字段逐条标注，不冒充现网已裁决 MR；
known_miss/known_fp 在起步集是**预设形态**（按标签结构约束标注），其「历史模型
漏报/误报」实证待现网 MR 样例回填。装载门禁双层：
`test/evalq.TestEvalBaselineStarterSet`（#25 规模/标签混合/严重度覆盖/分仓/出处）
+ `test/evalq.TestBaselineDataset`（#28 四语言齐/每语言 ≥10 条/known_miss·
known_fp 正例/high 锚）。

## 跑批骨架（`Evaluate`）

```go
rep, err := evalq.Evaluate(ctx, ds.Cases, reviewer, evalq.Options{
    Model: "mimo-v2", Params: map[string]string{"temperature": "0.2"},
    StabilityRuns: 3, // 裁决稳定性 k 轮（第 1 轮复用，省一半成本）
})
rep.WriteJSON(w)  // eval/results/baseline-latest.json
rep.WriteMarkdown(w) // eval/results/baseline-latest.md（+分语言表 +分严重度表 +逐案钻取）
```

**真实模型 Reviewer（本批落地，test 侧）**：`Reviewer` 接口
（`Review(ctx, Case) (Outcome, error)`）是唯一接入点——`internal/evalq` 保持
L0 零 internal 依赖、不内置任何 LLM 客户端（依赖政策红线；评测旁路形态与
obs evalmetrics 一致，评测变异不影响在线链路）。真实实现住在
`test/evalq/real_model_test.go`：

- **装配**：`openai.NewChatModel` 轻装配（与生产 `bootstrap.BuildModels` 同一
  构造器，不引 bootstrap 全家桶）；
- **单发审查协议**：输入样例 title+diff（`plugin.DataFence` 包数据区），输出
  结构化 findings JSON——`{"verdict","findings":[{file,line,kind,severity,comment}],"notes"}`，
  kind = kebab-case 类别词（词表随 prompt 给出，与金标准同风格；起步集按闭集
  计量，开集召回随 #28 扩容），verdict 口径对齐 golden R5 真值表
  （high→request_changes、仅中低→comment、无意见→approve）；
- **归一化**：`plugin.DecodeFindingsJSON` 宽容解析（jsonrepair 单源）+ 评测侧
  并行视图取 kind/verdict（二者不在 `FindingsWire` 生产契约里：生产 verdict 由
  R5 裁决、意见身份按 file+comment+line 指纹），归一到
  `evalq.Finding{File, Kind, Severity}`（去空白转小写）；**kind 缺失的条目置空**
  ——计分侧自然不命中，诚实计量不猜类别。解析规则单测不联网
  （`TestDecodeModelOutcome`）。

跑批入口与门禁（形态照 golden realLLM 分支）：

```bash
make eval-baseline        # 打桩回归（CI 常跑，不联网）
make eval-baseline-real   # 真实模型：ARGUS_EVAL=1 + lacus/.env 的 OPENAI_*
# 裁决稳定性轮数：ARGUS_EVAL_STABILITY=N（缺省 3）
```

## 报告形态

- JSON：全量指标 + 分语言 + **分严重度**（`by_severity`：期望条数/实际上报/
  TP·FP·FN/档位比率）+ 逐案（TP/FN/FP 下标与漏检/误报类别词），artifact
  归档口径同 golden eval（全量数据，不只 pass/fail 信号）。
- Markdown：总指标表 + 分语言表 + **分严重度表** + 逐案明细（漏检类别/误报
  类别直读，不必回查金标准）。
- 分严重度口径：期望按金标准 `must_find.severity` 归桶、实际上报与 FP 按实际
  上报 severity 归桶；词表行（high/medium/low）恒出，未标注归 `unspecified`
  桶垫底；档位比率零分母置 0（与总指标同口径）。
- 下游消费：P3-02 质量控制台读本报告文件做质量趋势曲线；obs evalmetrics 的
  gauge 面可在后续批次接 `argus_eval_*` 同款暴露。

## 完成度边界（#25 本批口径 + #28 四语言扩容）

- 已落地：指标计算单源（含裁决稳定性）、**分严重度报表聚合**（JSON
  `by_severity` + Markdown 分严重度表）、基准集装载与完整性校验、**四语言起步集
  `eval/baseline/{go,java,python,jsts}.yaml`**（各 11 条共 44 条合成夹具，
  各语言自带标签混合面）、打桩跑批回归、报告双形态、`make eval-baseline`（打桩）与
  `make eval-baseline-real`（真实模型，`TestEvalBaselineRealModel`，
  `ARGUS_EVAL_STABILITY` 控制 k 轮）双入口、**真实模型单发审查 Reviewer**
  （test 侧轻装配 + 归一化规则单测）。
- 起步集出处口径：全部样例为合成夹具（go=golden 改写 + 自编写；java/python/
  jsts=自编写），`source` 逐条标注；known_miss/known_fp 是预设形态，实证回填
  与现网已裁决 MR 样例仍待回填——起步集不冒充现网数据。
- **首跑基线（2026-11-13，`make eval-baseline-real`，mimo-v2.6-pro，k=3）**：
  Precision 0.833 / Recall 1.000 / 误报率 1.000 / 漏检率 0.000 / F1 0.909 /
  Verdict 命中率 42.9% / 裁决稳定性 0.952（混淆 TP=5 FP=1 FN=0 TN=0；7 案
  出错 1——单发协议下该样例模型输出不可解析，保守计分）。⚠️ **该数值是
  go.yaml 单语言 7 案时代的首跑**——四语言扩容（44 案）后尚未重跑，新基线
  以扩容后首次 `make eval-baseline-real` 归档为准，勿跨口径比较。全量 artifact
  见 `eval/results/baseline-latest.{json,md}`（gitignored，每次跑批覆盖）。
  同配置另一次跑批因网关读超时出错 1 案（Precision 0.444 / Recall 0.800 /
  稳定性 0.905）——模型与网络方差大，基线按批次归档、不跨批硬比。
- **官方终基线（2026-11-14，全部修复后终轮，mimo-v2.6-pro，k=3）**：
  **P=1.000 / R=1.000 / 误报率 0.000 / 漏检率 0.000 / F1=1.000 /
  verdict 79.5% / 稳定性 0.977**（TP=33 FP=0 FN=0 TN=11，errored=0）——
  **四语言全满分**（go 9 / java 8 / jsts 8 / python 8 案 TP 全中）。
  artifact `eval/results/baseline-latest.*` + 本地 archive/
  baseline-official-final-20261114.*。中间轮次（第三轮 P=1.000 R=0.909
  残余 3 FN=1 出错+2 近义词摇摆）证明修复方向正确、终轮收敛到满分。
  治理前后对照与根因链见下节。
- 未含（后续批次）：开集 kind 召回（词表随 prompt 扩容）；现网已裁决 MR 样例
  回填（需真实裁决数据）；历史批次数值的留存归档（latest 每次覆盖）。
  （质量趋势消费面已随批 A1 落地：`/console` 读本报告渲染 `renderConsoleTrend`。）

## known_fp 误报诊断（批 B3，2026-11-14）

**现象**：首跑误报率 1.000——known_fp 正例全部被报（FP kind 集中在
`style-nit`）。

**根因链（三段证据闭合，是 prompt 与金标准的策略矛盾，不是评分 bug）**：

1. **评分口径正确**：clean 基准（零 `must_find`）上「全部实际上报计 FP」是
   golden 北极星口径（`evalq.go` FP 口径注释在案）——known_fp 案例
   `expect.verdict=approve` 且零 must_find，模型报任何意见都计 FP。
2. **Prompt 主动邀请**：`reviewSystemPrompt` 的 kind 示例词表含
   `naming-convention`、`style-nit`（real_model_test.go:43），且协议只要求
   「没有问题时输出 []」——未定义「风格偏好不算问题」。
3. **金标准从不要求风格类**：四语言 44 案 `must_find` 词表里 `style-nit` /
   `naming-convention` 出现 **0 次**（`missing-doc-comment` 仅 go-003 新文件
   缺注释一例）；known_fp 四案恰是风格偏好诱饵（类型注解/f-string 改写/
   getter 重命名/防御性判空）。

**结论**：模型服从 prompt 词表邀请报风格意见 → 撞上「clean 即 FP」口径 →
误报率 1.000。**金标准与评分都不改**（北极星口径与诱饵设计是有意的）；
治理点在 prompt：补「纯风格偏好/等价重构不算问题」边界，同时保留
`missing-doc-comment`（新代码缺注释仍要报，go-003 金标准依赖它）。

**与生产的关系（诚实边界）**：本指标是**单发 Reviewer 裸协议**误报率；
生产链路另有误报反馈回流（指纹抑制）、prints 惯犯热力降权等抑制层——
生产实际误报率低于本指标。治理 prompt 降的是「裸协议误报倾向」，两层各算各的。

### 第二根因：kind 同义词错配（44 案首跑揪出，7 案时代不可见）

治理前 44 案跑批（2026-11-14，k=3，21min，errored=1）拆解出**更大的问题**：
P=0.632 / R=0.364 / 漏检率 0.636——21 个 FN 中 **20 个是同义词错配、
真沉默 0**：模型把问题全找到了，但标签自造（`sequential-await` vs 金标准
`serial-await`、`nil-deref` vs `npe`、`unsafe-innerhtml` vs `xss`…）。
根因=协议词表缺口：金标准 27 词闭集，prompt 示例只列 11 词且其中 5 词
（`nil-deref`/`race-condition`/`unchecked-err`/`naming-convention`/`style-nit`）
不在金标准——模型在「示例里没有的词」上必然自造。

**治理动作（评分与金标准设计不动，两处 prompt 修 + 一处数据缺陷修）**：

1. kind 词表 → 金标准闭集全词表（协议统一标签，模型仍须自己发现问题）；
2. 补「纯风格偏好不算问题」边界（保留 `missing-doc-comment`，go-003 依赖）；
3. **金标准数据缺陷修复**：`hardcode-secret`/`hardcoded-secret` 同概念两
   拼写并存（go=hardcoded、python/jsts=hardcode）→ 统一 `hardcoded-secret`
   （模型两轮都稳定选 hardcoded，且语义更标准；修数据非刷分）。

### A/B 对照（同模型 mimo-v2.6-pro，k=3，44 案）

| 指标 | 治理前 | 治理后（第二轮） |
|---|---|---|
| Precision | 0.632 | **1.000** |
| Recall | 0.364 | **0.939** |
| 误报率 | 0.538 | **0.000** |
| 漏检率 | 0.636 | **0.061** |
| F1 | 0.462 | **0.969** |
| Verdict 命中 | 63.6% | **81.8%** |
| 裁决稳定性 | 0.955 | **0.970** |
| errored | 1 | **0** |

第二轮残余 FN 仅 2 个（ts-006/py-006），即拼写缺陷修复所指——数据修复后
理论 recall=1.000。分语言：go/java 双 1.000，jsts/python 0.88（各 1 个拼写 FN）。
**终轮（拼写修复后）验证该推断：FN=0、四语言全 1.000、F1=1.000**。

### 附带揪出的工程 bug：artifact 同名互冲

治理中实锤：`TestBaselineStubRun`（make test 全量必跑）与真实跑批共写
`baseline-latest.*`——**每次门禁都把真实基线冲成 stub**（02:09 真实绿 →
02:13 门禁冲掉 → 02:15 消费测试连锁红）。修复=打桩改写 `baseline-stub.*`
（分名纪律补全）+ 两个真实文件消费测试加 `model==stub` skip 守卫。
回归验证：全量 `make test` 跑完 `latest` 仍为真实模型。

归档（本地，`eval/results/` gitignored）：
`archive/baseline-before-treatment-20261114.*`（治理前）、
`archive/baseline-after-treatment-20261114.*`（治理后第二轮）、
`baseline-latest.*`（官方终基线=数据修复后第三轮）。
