# 真实模型质量评测体系（P2-02，issue #25）

> 状态：骨架 + 本批补齐（分严重度报表聚合、起步集 `eval/baseline/go.yaml`、
> 真实模型单发审查 Reviewer 与 `make eval-baseline-real` 跑批入口；首跑基线
> 数值见文末「完成度边界」）。
> 关联：golden eval（`make eval` / `make eval-real`，v3.10.0 起）是编排语义与
> verdict 命中率面——本体系补 finding 级混淆矩阵与裁决稳定性；多语言基准集
> 见 P3-03（#28）；质量趋势可视化消费面见 P3-02（#27）。

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

**起步集（本批交付）**：`eval/baseline/go.yaml` 7 条样例——混合
standard/known_miss/known_fp、覆盖 high/medium/low 三档 must_find、7 个分仓。
**出处口径**：全部为合成夹具（golden 08/09/10/29/30 改写 + 自编写现实形态 Go
diff），`source` 字段逐条标注改写来源，不冒充现网已裁决 MR；known_miss/
known_fp 在起步集是**预设形态**（按标签结构约束标注），其「历史模型漏报/误报」
实证待现网 MR 样例回填（P3-03 #28）。装载门禁
`test/evalq.TestEvalBaselineStarterSet` 守规模/标签混合/严重度覆盖/分仓/出处。

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

## 完成度边界（#25 本批口径）

- 已落地：指标计算单源（含裁决稳定性）、**分严重度报表聚合**（JSON
  `by_severity` + Markdown 分严重度表）、基准集装载与完整性校验、**起步集
  `eval/baseline/go.yaml`**（7 条合成夹具，标签/严重度/分仓混合）、打桩跑批
  回归、报告双形态、`make eval-baseline`（打桩）与 `make eval-baseline-real`
  （真实模型，`TestEvalBaselineRealModel`，`ARGUS_EVAL_STABILITY` 控制 k 轮）
  双入口、**真实模型单发审查 Reviewer**（test 侧轻装配 + 归一化规则单测）。
- 起步集出处口径：全部样例为合成夹具（golden 08/09/10/29/30 改写 + 自编写），
  `source` 逐条标注；known_miss/known_fp 是预设形态，实证回填与现网已裁决 MR
  样例归 P3-03（#28）——起步集不冒充现网数据。
- **首跑基线（2026-11-13，`make eval-baseline-real`，mimo-v2.6-pro，k=3）**：
  Precision 0.833 / Recall 1.000 / 误报率 1.000 / 漏检率 0.000 / F1 0.909 /
  Verdict 命中率 42.9% / 裁决稳定性 0.952（混淆 TP=5 FP=1 FN=0 TN=0；7 案
  出错 1——单发协议下该样例模型输出不可解析，保守计分）。全量 artifact 见
  `eval/results/baseline-latest.{json,md}`（gitignored，每次跑批覆盖）。
  同配置另一次跑批因网关读超时出错 1 案（Precision 0.444 / Recall 0.800 /
  稳定性 0.905）——模型与网络方差大，基线按批次归档、不跨批硬比。
- 未含（后续批次）：多语言基准集与开集 kind 召回（#28）；质量趋势可视化
  消费面（P3-02 #27）；历史批次数值的留存归档（latest 每次覆盖）。
