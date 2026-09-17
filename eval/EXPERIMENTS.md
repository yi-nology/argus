# 审查域模型 A/B 实验脚手架（G1b）

> 目的：用固定的 golden PR 评测集回答「R3/Reviewer 路由到 agent 特训强的模型
> （mimo-v2.5 / mimo-v2.5-Pro）是否真带来审查质量提升」——用数据拍板，不押注
> 厂商 benchmark（REPORT.md Open question 4：harness-only 提升需在审查域复现）。

## 固定 harness

- 评测集：`eval/golden/*.yaml`（24 条，场景标签覆盖 injection / partial /
  scope-creep / unverifiable / multilingual / merge / evidence-gate / guard）。
- 执行：`make eval-real`（= `ARGUS_EVAL=1 go test ./test/e2e/ -run TestGoldenReviewEval`），
  走 `lacus/.env` 的 OPENAI_* 凭证，每条夹具跑完整 runner→R1→R5 链路。
- 口径：**verdict 命中率**（主）+ 报告 must_find 子串（真实模式不判分，仅供人看）。
  模型方差控制：同配置至少跑 2 遍取并集通过数，波动 >2 条时加跑第 3 遍。

## 对照矩阵

| 组 | 环境变量 | 说明 |
|---|---|---|
| A 基线 | `OPENAI_MODEL=<现用基线>` | 其余不设 |
| B 全局换 mimo | `OPENAI_MODEL=mimo-v2.5` | 全阶段同模型 |
| C R3/Reviewer 切 mimo | `OPENAI_MODEL=<基线>` + `ARGUS_MODEL_R3=mimo-v2.5` + `ARGUS_REVIEWER_MODEL=mimo-v2.5` | 只切审查两阶段（StageRouter，零代码改动） |
| D C 组升级 Pro | 同 C，模型名换 `mimo-v2.5-pro` | 验证 Pro 溢价是否值得 |

约定：除上表变量外一切配置冻结（golden 集不打桩、不传 stub 段）；每轮结果
`eval/results/latest.json` 会记录 model 与 stage_models 便于归档比对。

## 执行

```bash
# 1) 基线
make eval-real && cp eval/results/latest.json eval/results/baseline-$(date +%m%d).json
# 2) C 组示例
ARGUS_MODEL_R3=mimo-v2.5 ARGUS_REVIEWER_MODEL=mimo-v2.5 make eval-real \
  && cp eval/results/latest.json eval/results/mimo-r3-reviewer-$(date +%m%d).json
```

注意：mimo 侧有并发限流（试点环境实测），golden 驱动是串行逐条执行，无需调参；
若单条 429 退避，重跑该子测试即可（`-run 'TestGoldenReviewEval/<名称>'`）。

## 结果登记（每轮一表，只增不删）

| 日期 | 组 | 模型 | 命中/总数 | 备注 |
|---|---|---|---|---|
| —— | A | （待首轮实测） | — | — |
| —— | B | — | — | — |
| —— | C | — | — | — |
| —— | D | — | — | — |

## 拍板规则

- C 组命中率显著高于 A（≥2 条且跨两轮复现）→ 默认 StageRouter 配置切 C 组模型，
  CHANGELOG 记录 + golden 回归数字随附；
- B/C/D 无显著差异 → 维持基线，本档保留为后续模型升级的固定验证流程；
- 任何组若出现 injection 类场景失守（verdict 误升 APPROVE）→ 一票否决该模型，
  与厂商分数无关。
