# 真实模型质量评测体系（P2-02，issue #25）

> 状态：骨架落地（评测框架 + 指标计算 + 数据集装载 + 报告；真实模型调用留接口）。
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

标签语义：`known_miss` = 金标准有缺陷且历史模型漏报（漏检正例，必须有
must_find）；`known_fp` = 历史模型在干净 diff 上报出的意见（误报正例，
必须有零 must_find）。`Validate()` 硬校验 id 唯一 / label·verdict 词表 /
标签结构约束 / diff 非空。

## 跑批骨架（`Evaluate`）

```go
rep, err := evalq.Evaluate(ctx, ds.Cases, reviewer, evalq.Options{
    Model: "mimo-v2", Params: map[string]string{"temperature": "0.2"},
    StabilityRuns: 3, // 裁决稳定性 k 轮（第 1 轮复用，省一半成本）
})
rep.WriteJSON(w)  // eval/results/baseline-latest.json
rep.WriteMarkdown(w) // eval/results/baseline-latest.md（+分语言表 +逐案钻取）
```

**真实模型调用留接口**：`Reviewer` 接口（`Review(ctx, Case) (Outcome, error)`）
是唯一接入点——骨架不内置任何 LLM 客户端（依赖政策红线；评测旁路形态与
obs evalmetrics 一致，评测变异不影响在线链路）。真实模型实现建议复用
eval-real 的装配（`ARGUS_EVAL=1` + `OPENAI_*`），产出 Outcome 时把模型
findings 归一化到 `evalq.Finding{File, Kind, Severity}`。

打桩回归（CI 常跑）：

```bash
make eval-baseline   # go test ./test/evalq/ -run TestEvalPipelineStub -v
```

## 报告形态

- JSON：全量指标 + 分语言 + 逐案（TP/FN/FP 下标与漏检/误报类别词），artifact
  归档口径同 golden eval（全量数据，不只 pass/fail 信号）。
- Markdown：总指标表 + 分语言表 + 逐案明细（漏检类别/误报类别直读，不必回查
  金标准）。
- 下游消费：P3-02 质量控制台读本报告文件做质量趋势曲线；obs evalmetrics 的
  gauge 面可在后续批次接 `argus_eval_*` 同款暴露。

## 完成度边界（骨架口径）

- 已落地：指标计算单源（含裁决稳定性）、基准集装载与完整性校验、打桩跑批
  回归、报告双形态、`make eval-baseline` 入口。
- 未含（后续批次）：真实模型 Reviewer 实现（接口已留，接 eval-real 装配）、
  指标基线报告的**真实数值**（需真实模型跑批产出，随首次真实评测归档）、
  分严重度报表聚合（逐案数据已含 severity，聚合面随真实数据形态再定）。
