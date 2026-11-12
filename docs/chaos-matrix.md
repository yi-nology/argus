# 混沌测试矩阵（P2-01，issue #24）

> 状态：骨架落地（注入点抽象 + 五族故障可执行矩阵 + 报告输出）。
> 关联：拆自 [Meta] #21 的 P2-01；fencing 基建见 #6（迁移/任务租约）、
> fail-closed 迁移见 v6.7.0。

## 定位

把「任务中途死亡要能恢复、依赖不可达要有界失败、限流要退避、租约失守要
fencing」从口头约定变成可执行契约。三层形态（与 issue #24 建议分层对齐）：

| 层 | 载体 | CI |
|---|---|---|
| 注入点抽象 + 规范矩阵 | `internal/chaos`（L0 零 internal 依赖） | 在线链路不 import（旁路形态，同 obs evalmetrics） |
| 单测级实弹回归 | `test/chaos`（sqlite 真存储 + 打桩 LLM/Git 面） | `make chaos`（`go test ./...` 全量自动含） |
| 容器级 / k3s Pod Kill | testcontainers pg/MySQL、43 环境 k3s 手动 job | 部署侧手动触发（本包 Injector/Report 即其驱动契约） |

## 故障族词表与规范矩阵

五族（`chaos.Family`）× 注入点（`chaos.Matrix()` 为机器可读单源）：

| 故障族 | 故障（Kind） | 注入点 | 期望恢复语义 |
|---|---|---|---|
| process_kill | task_worker_sigkill | worker.process | kill -9 后心跳冻结，staleAfter 窗口内 `ResetRunningToPending` 复位回 pending；死亡实例僵尸写入被 `FinishTaskFenced` 拒绝 |
| network_partition | git_api_unreachable | git.api | 瞬态：有界重试；恢复后同调用成功（不 hammer） |
| network_partition | llm_gateway_unreachable | llm.review | 瞬态：有界重试后任务按失败分类落 fail_reason，`ResetFailedForRetry` 退避复活 |
| lease_expiry | task_lease_expired | db.finish | 失租后旧执行体 `FinishTaskFenced` 被 fencing 拒绝，新执行体正常落终态 |
| rate_limit_429 | git_api_429 | git.api | 瞬态：退避重试至成功；非瞬态（kill）fail-fast 单次 |
| llm_fault | llm_timeout | llm.review | 瞬态：有界重试 |
| llm_fault | llm_429 | llm.review | 瞬态：退避重试至成功 |
| llm_fault | llm_empty_output | llm.review | 契约故障（非瞬态）：不盲目重试，失败分类 + 退避复活 |
| llm_fault | llm_malformed_output | llm.review | 同上 |
| db_timeout | db_write_timeout | db.write | 写路径超时/回滚后重试不产生重复任务（webhook event_id 幂等键兜底） |

## 注入点抽象（契约面）

```go
inj := chaos.NewScriptedInjector()
inj.Script("llm.review", chaos.Script{Fault: chaos.ErrUnreachable, FailFirst: 2}) // 前 2 次注入，之后放行（恢复窗）
err := chaos.Retry(ctx, inj, chaos.Op{Kind: chaos.NetLLMUnreachable, Target: "llm.review"}, 3, 0, fn)
```

- `Injector`：宿主（打桩 LLM/Git 客户端、存储包装层）每次外部调用前询问；
  部署侧容器级 job 用同一接口包真实客户端。
- 哨兵故障：`ErrKilled / ErrUnreachable / ErrLeaseLost / ErrRateLimited /
  ErrDeadline / ErrEmptyOutput / ErrMalformedOutput`（`errors.Is` 判别）。
- `IsTransient(err)`：瞬态（断网/429/超时）→ 有界重试是正确恢复动作；
  非瞬态（死亡/失租/空输出/非结构化）→ fail-fast 走失败分类。注入面与恢复面
  共用的语义契约，`test/chaos` 逐格断言。
- `Retry`：有界重试骨架（每次尝试恰好询问注入器一次，`Calls` 计数 = 尝试
  次数——断言口径单源）。

## 可执行回归与报告

```bash
make chaos   # go test ./test/chaos/ -run TestChaosMatrix -v
```

- 覆盖门禁：`chaos.MissingCells` / `ExtraCells`——矩阵缺格（「每格至少一条
  脚本化回归」的验收口径）与越格场景都在测试里硬断言。
- 报告双形态落 `test/chaos/results/chaos-latest.{json,md}`（gitignore；CI
  artifact 面，全量数据归档而非只留 pass/fail 信号，口径同 golden eval）。
- 单格失败不短路、probe panic 单格捕获——混沌报告要全量数据。

## 完成度边界（骨架口径）

- 已落地：注入点抽象、五族故障全格实弹回归（sqlite + 打桩面）、瞬态分类
  契约、矩阵覆盖门禁、报告输出、`make chaos` 入口。
- 未含（后续批次）：testcontainers 级真实 pg/MySQL 注入 job、43 k3s Pod Kill
  手动工作流（CI yml 不在本批扩面，驱动契约已就位）、生产 LLM/Git 客户端的
  Injector 接线（runner 侧装配点留接口，未改在线链路）。
