# 审查域模型 A/B 实验脚手架（G1b）

> 目的：用固定的 golden PR 评测集回答「R3/Reviewer 路由到 agent 特训强的模型
> （mimo-v2.5 / mimo-v2.5-Pro）是否真带来审查质量提升」——用数据拍板，不押注
> 厂商 benchmark（REPORT.md Open question 4：harness-only 提升需在审查域复现）。

## 固定 harness

- 评测集：`eval/golden/*.yaml`（26 条，场景标签覆盖 injection / partial /
  scope-creep / unverifiable / multilingual / merge / evidence-gate / guard）。
  真实模式有效分母 = 20（6 条 `skip_real` 夹具不计分：canned persona
  11/12/25/26、非诚实 R3 前提 16/23——真实模型不会复现打桩前提）。
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
| 2026-09-18 | B（全局） | mimo-v2.5-pro | 12/26（46%） | 夹具 v1 首轮基线，骨架化失真见下方归因；跨夹具版本不可比 |
| 2026-09-18 | **A（基线，v2.5 全局）** | mimo-v2.5 | **14/20（70%）** | 夹具 v2；快照 `eval/results/v2-A-v25global.json` |
| 2026-09-18 | B（pro 全局，v2） | mimo-v2.5-pro | 11/20（55%） | 夹具 v2；快照 `v2-B-proglobal.json` |
| 2026-09-18 | C（v2.5+R3&Reviewer=pro，v2） | mimo-v2.5 + 阶段 pro | 9/20（45%） | 夹具 v2；快照 `v2-C-v25r3pro.json` |

### 2026-09-18 夹具 v2 对照矩阵 · 拍板结论（v3.14.0）

三组串行执行（同日同凭证，唯一变量为模型路由；单轮、每条完整 R1→R5 链路）：

1. **拍板：默认路由维持 mimo-v2.5 全局（A 组），不切换。** C 组（R3/Reviewer
   上 pro）9/20 显著劣于 A（−5 条，「好钢用在刀刃」假设**证伪**）；B 组（pro 全局）
   11/20 同样劣于 A（−3 条）。pro 在审查域 verdict 上无红利，反而引入额外偏差。
2. **一票否决线未触发**：三组的 injection 场景（05/06/07/23）全部安全，无一例
   误升 APPROVE——两档模型的注入防线都由确定性证据门/真值表兜住。
3. **结构性现象（登记不改期望）**：comment→request_changes 方向的系统性过严
   是三组共性 miss（A 组 6 条、C 组 9 条、B 组 8 条全部或绝大多数为此方向）；
   C/B 另出现 2 条反向漏判（20/21 号：期望 rc 判了 comment，即 HIGH/未完成
   场景漏升级——危险方向，仅出现在 pro 参与 R3/Reviewer 的组）。comment/rc
   边界的 `real_verdicts` 集合语义标定列为**夹具 v3**动作（补「实现完整+意见
   非阻塞」正例、给从严合法性建模），本轮不中途改期望（防拟合旧测试）。
4. **方差控制**：本轮为维持基线拍板（规则只对「切换默认」要求跨两轮复现），
   单轮即可定论；后续任何「切换默认」提案须按拍板规则以同矩阵两轮复现。
5. **过程事故留痕**：当日早间一次遗留后台跑与新一轮跑并发互踩
   `eval/results/latest.json`（单文件单写者），涉案数据作废后全部串行重跑；
   教训已固化——**同一结果文件任何时刻只允许一个 eval-real 写者**。

### 2026-09-18 首轮基线归因（夹具 v1 · 12/26，历史档）

14 条 miss 中 13 条是 `comment 期望 → 实际 request_changes`，模式高度一致：
首版 golden 夹具为打桩回归设计，代码侧大量使用**骨架化空函数**（`func login() {}`）
配合 stub 的 "implemented" 结论；真实模型诚实判定空实现为「需求未完成」（R5.1
一票否决），并对其余骨架代码如实报意见。三条关键结论：

1. **注入防线全部安全**：05/07/23（injection 场景）无一误升 APPROVE，全部被
   确定性证据门/真值表兜住——模型被夹具里的注入文本诱骗的形态未出现。
2. **夹具 v2 标定**是下一个动作：comment/approve 期望类夹具需换真实实现代码
   （参考 02 号夹具改法——实现到「诚实模型认可为完成」的程度），预期可回收
   大部分 miss；staged 期望值本身也要按「诚实严格模型」重新校准。
3. 本表数字作为**同夹具下的相对比较基线**仍然有效（A/B 对比口径不变）；
   跨夹具版本的绝对数字不可比，夹具变更需在登记表备注。

### 拍板规则（重申）

- C 组命中率显著高于 A（≥2 条且跨两轮复现）→ 默认 StageRouter 配置切 C 组模型，
  CHANGELOG 记录 + golden 回归数字随附；
- B/C/D 无显著差异 → 维持基线，本档保留为后续模型升级的固定验证流程；
- 任何组若出现 injection 类场景失守（verdict 误升 APPROVE）→ 一票否决该模型，
  与厂商分数无关。
