# 审查质量控制台（P3-02，issue #27）

> 状态：骨架落地（数据聚合 + 静态页/CLI 输出形态）。
> 关联：误报闭环（/findings/suppress · restore）、双账本 /usage/monthly（v6.9.0）、
> 质量趋势数据源 = P2-02 评测基线（#25）、指标命名单源 internal/obs。

## 形态决策

不建重型前端（v3.84 轮裁定延续：argus 自身不做常驻 UI）——只读控制台以
**生成式静态页 + CLI 文本**交付：

- `cmd/qconsole`：admin 档 token 拉既有聚合面 → 生成自包含 HTML（inline CSS、
  无外部资源、离线可开）或 CLI 文本；
- 数据纪律（验收硬口径）：**全部来自既有 API 聚合，无新表**——
  `/stats/overview`（任务/verdict 分布/抑制计数/token）、`/usage/monthly`
  （双账本）、`/metrics`（obs 计数器）、评测基线报告文件（P2-02 artifact）。

## 用法

```bash
# 静态页（默认 qconsole.html）
go run ./cmd/qconsole -addr http://argus.internal:8899 -token $ARGUS_ADMIN_TOKEN -days 30

# CLI 文本（终端/工单粘贴）
go run ./cmd/qconsole -addr ... -token ... -text

# 挂评测基线趋势（make eval-baseline / eval-real 产出后自动上屏）
go run ./cmd/qconsole ... -eval-report eval/results/baseline-latest.json
```

token 档位：`/stats` `/usage` 需 read 档（带行约束时只见约束内仓库）；
`/metrics` 需 admin 档——metrics 缺位（403/404）不构成控制台不可用，
对应区块显示「无数据」。

## 面板区块（issue #27 验收四件套）

| 区块 | 数据源 | 口径 |
|---|---|---|
| 误报反馈 | /stats/overview `findings_suppressed` + 各仓抑制列 | 指纹抑制累计（suppress/restore 闭环面） |
| 插件表现 | /metrics `argus_plugin_failures_total` / `argus_plugin_skipped_total`（按 plugin 标签展开） | 失败+跳过合计一眼面；明细钻取走 /metrics 原面 |
| 失败原因归类 | /metrics：llm_failovers / plugin_failures / qa_failures / budget_exceeded / events_dropped / notify_failures / poller_failures / migration_lease_lost | 非零计数上屏（LLM 降级/插件失败/预算超限……） |
| Token 消费 | /usage/monthly 双账本 + 各仓明细 | quota（done 口径）与 real（全终态）水位条 |
| 质量趋势 | 评测基线报告 JSON（internal/evalq.Report 投影） | Precision/Recall/误报率/漏检率 + 分语言 + 裁决稳定性 |
| RC 率代理 | verdict 分布 | request_changes 占比——误报率真值来自评测基线，RC 率是同面第一近似（页内注明） |

## 完成度边界（骨架口径）

- 已落地：聚合器（HTTPSource + 只读纪律 GET-only）、双形态渲染、eval 基线
  趋势接入、`cmd/qconsole` CLI、桩测回归（聚合口径/缺位容忍/只读纪律）；
  服务端路由直出 `GET /console`（#27 同批，服务侧自包含 HTML 页——数据面
  同一聚合面，行过滤在 stats/usage 内，见 internal/service/service_console.go）。
- 未含（后续批次）：按月历史曲线（需评测基线按月归档积累，聚合器已按点
  接入）、web/ 宣传站联动。
