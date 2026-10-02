# tech-debt —— 代码体量与复用剩余清单

> 全面精简轮（v3.95.0~v3.101.0，2026-10-02~03）收官后的持久清单。下一轮优化从这里起步；
> 每完成一项请同步更新本文件。量法：非测试、非生成（`biz/model/api/` thriftgo 除外）Go 代码。

## 基线

- 手工代码 **24,059 行**（本轮起点 24,258；净降主要被新增文档注释/函数签名部分回填——拆分是可读性投资，不是纯粹减行）。
- 最大手工文件 **554 行**（runner.go），全部文件 <560 行。
- 100+ 行函数从 **10 个降到 6 个**（见下表）。
- 五批记录：v3.95.0 重复面单源 / v3.96.0 七大文件拆分 / v3.98.0 四长函数收口 / v3.99.0 Execute 收口+契约单源 / v3.101.0 config+stages+reflection 收口（v3.100.0 被并行会话扫描收口占用）。

## 剩余 >100 行函数（6 个，均有留痕裁定）

| 函数 | 行数 | 裁定 |
|---|---|---|
| `bootstrap.Assemble` | 204 | 组合根装配铺开型，拆分负收益（v3.61.0 在案） |
| `config.Load` | 137 | 声明式 env→字段映射，拆段落 helper 只加间接层 |
| `Dispatcher.Run` | 126 | 熔断配对契约（Allow↔Success/Failure/Abandon 恰一），拆散风险 > 收益 |
| `mcpserver.New` | 120 | 声明式工具注册表，属配置不属逻辑 |
| `JudgeCompleteness` | 108 | 编号即结构的确定性清单体（fail/pass 闭包对称） |
| `qa.Engine.tools` | 108 | 工具注册清单，同上 |

90~100 行区间（`runApp`/`CheckCompliance`/`sendReview`/`migratePrintFingerprintsV2`/`SuppressFinding` 等）为健康上限内，不再压。

## 剩余较大文件（420~560 行，内聚面已单一）

- `runner/runner.go` 554：核心编排（Execute/run 管线），再拆只剩把 run 四步提指针，负收益。
- `service/fix.go` 493、`context/sdkctx.go` 493、`storage/store_task.go` 485、`plugin/merger.go` 470、`service/service_comment.go` 447、`poster/poster.go` 425：单一内聚面（fix 命令全链 / SDK 门面 / 任务表操作 / 合并器 / 评论处理 / 回帖）。

## 已裁决不做项（勿重探）

- 生成代码 `biz/model/api/argus.go`（8,537 行，thriftgo DO NOT EDIT）。
- `noopLogger` service/qa 双包同构 17 行：ekit logx 无 Nop，不值得新开共享位。
- `BearerMiddleware`/obs Prometheus 中间件/`toolpool.Pool` 接口等历史裁定见各提交信息。

## 复测方法

```bash
# 文件榜
find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' ! -path './biz/*' -print0 | xargs -0 wc -l | sort -rn | head
# 函数榜（type/const/var 也算边界）
for f in $(find . -name '*.go' ! -name '*_test.go' ! -path './vendor/*' ! -path './biz/*'); do
  awk -v file="$f" '/^func |^type |^const |^var |^\)/{if(name){print NR-start, name, file; name=""}} /^func /{name=$0; start=NR}' "$f"
done | sort -rn | head
```

门禁：`make lint && make test && make arch && make eval`（golden 69）。
