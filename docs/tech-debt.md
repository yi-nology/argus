# tech-debt —— 代码体量与复用剩余清单

> 全面精简轮（v3.95.0~v3.101.0，2026-10-02~03）收官后的持久清单；v3.106.0~v3.112.0
> 设计模式/收口轮（2026-10-04）续账。下一轮优化从这里起步；
> 每完成一项请同步更新本文件。量法：非测试、非生成（`biz/model/api/` thriftgo 除外）Go 代码。

## 基线（2026-10-04 复测）

- 手工代码 **24,145 行**（v3.101 基点 24,059；增量来自词表注册表/工厂/模板的文档注释与新单测伴生代码——重复面在净降）。
- **单元覆盖率 34.3%**（全仓 statements 口径；不含 test/e2e 打桩链路——golden 69 条走真实编排但按包不计数，编排面实际覆盖远大于此数，基线仅作纵向对比）。薄弱面清理：**poller 15.5%→93.0%**（v3.112.0 补 pollOnce 行为套件——poller 是试点常开的生产任务源且 golden 只覆盖 webhook 源）；runner 13.9% 为 golden 编排按包不计数的口径假象，不补包内单测。
- 最大手工文件 **554 行**（runner.go），全部文件 <560 行。
- 100+ 行函数从 **10 个降到 5 个**（mcpserver.New 被 v3.108.0 底座抽取降到 97，见下表）。
- 批记录：v3.95~v3.101 见 git log；v3.106.0 词表注册表 / v3.107.0 空对象+解码单源 / v3.108.0 mcpserver 底座 / v3.109.0 建任务工厂+词表常量 / v3.110.0 写作模板方法 / v3.111.0 工具信封单源 / v3.112.0 poller 覆盖+死代码清偿。

## 剩余 >100 行函数（5 个，均有留痕裁定）

| 函数 | 行数 | 裁定 |
|---|---|---|
| `bootstrap.Assemble` | 204 | 组合根装配铺开型，拆分负收益（v3.61.0 在案） |
| `config.Load` | 137 | 声明式 env→字段映射，拆段落 helper 只加间接层 |
| `Dispatcher.Run` | 126 | 熔断配对契约（Allow↔Success/Failure/Abandon 恰一），拆散风险 > 收益 |
| `JudgeCompleteness` | 108 | 编号即结构的确定性清单体（fail/pass 闭包对称） |
| `qa.Engine.tools` | 108 | 工具注册清单，同上 |

90~100 行区间（`runApp`/`mcpserver.New` 97/`CheckCompliance`/`sendReview`/`migratePrintFingerprintsV2`/`SuppressFinding` 等）为健康上限内，不再压。

## 剩余较大文件（420~560 行，内聚面已单一）

- `runner/runner.go` 554：核心编排（Execute/run 管线），再拆只剩把 run 四步提指针，负收益。
- `service/fix.go` 493、`context/sdkctx.go` 493、`storage/store_task.go` 485、`plugin/merger.go` 470、`service/service_comment.go` 447、`poster/poster.go` 425：单一内聚面（fix 命令全链 / SDK 门面 / 任务表操作 / 合并器 / 评论处理 / 回帖）。

## 已裁决不做项（勿重探）

- 生成代码 `biz/model/api/argus.go`（8,537 行，thriftgo DO NOT EDIT）。
- `noopLogger` service/qa 双包同构 17 行：ekit logx 无 Nop，不值得新开共享位。
- search/get_diff 工具的**执行体**两包各留一份：包装语义本就不同（R3 两级暂存
  get_detail / QA 定长截断），只单源 wire 契约不强行合执行。
- `BearerMiddleware`/obs Prometheus 中间件/`toolpool.Pool` 接口等历史裁定见各提交信息。
- 手拼截断尾注（`\n…（截断）`）不换 `textutil.TruncNote`：后者无 `\n` 分隔会改
  PR 可见文本，且 writing/ocrdeleg 薄包装裁定在案（v3.107.0 复判）。
- mcpserver 大一统 toolSpec 注册表：9 工具 handler 异构（可选能力检查/分支语义
  各异），抽公共底座（requireArgs/taskView）即停（v3.108.0 复判）。
- storage `withWriteLock` 包装器：27 处机械改动低于 Go 惯例容忍度（v3.106 轮复判）。

## 已收口记录（后续勿当新发现）

- v3.102.0 工具 wire 契约单源：`codesearch.ToolIn/ToolOut`（search_code schema
  与 ToolDescription 同居）+ `plugin.ToolFileIn/ToolFileOut/ToolGetFileDesc`
  （get_file 描述此前两面措辞已漂移，取合并措辞为正）。deadcode -test 复扫归零。
- v3.103.0 安全扫描收口：gosec G115（`int32(f.Line)` 饱和钳制 `clampInt32`）+
  G301 两处（sandbox_verify/ocrshim 目录 0755→0750）。**误报裁定**：fixverifier
  G703×3（resolveSafePath 前缀+EvalSymlinks 双侧校验可靠）、fix.go G304（上游
  safeRelPath 拒绝 `..` 穿越，fileExcerpt 全部在其后调用）、G118 fix 独立回帖
  ctx（v3.91.1 刻意）、G401 dream sha1（非安全 slug，换哈希会孤儿化既有记忆
  文件，G505 为其 import 面同源）、其余 G304（config/env 派生根目录，by-design）。
- **gosec 基线全集补档（v3.112.0 复扫定性）**：G301×4 + G306×8（ocrdeleg/memory/
  dream/assemble wcRoot/sandbox_verify/ocrshim/fixverifier 的产物目录 0755 +
  文件 0644）——argus 自有工件目录，单租户容器部署无跨 UID 消费面；收紧会破坏
  ocrshim sidecar 共享卷跨进程读取风险，by-design 留 0755/0644。G115×3/G104×78
  全部在生成代码（biz/model 豁免面）。非生成 26 条至此全部有裁定归属。
- govulncheck GO-2026-6505（otel otlptrace）**已清偿：v3.105.0 依赖升级
  v1.25.0→v1.45.0**；当前唯一漏洞源为零。
- v3.106.0~v3.111.0 设计模式轮：任务 kind 词表下沉 domain 注册表（修 storage
  裸字符串漂移）/ audit 空对象兑现（6 守卫死代码）/ taskFindings 解码单源 /
  mcpserver requireArgs+taskView 底座 / storage.NewTask 建任务工厂 + Dup* 词表 +
  progress.EventTask* 事件常量 + dupResp 模板 / runWritingCommand 写作模板方法 /
  plugin.ToolErrNote 信封单源 + newSearchCodeTool 合一。裁定不做：mcpserver
  大一统 toolSpec 表、截断尾注换 TruncNote、withWriteLock 包装器、tools() 完整
  spec 表（In/Out 泛型互异）。
- v3.112.0 收口批：poller pollOnce 行为套件（15.5%→93.0%，含事件订阅时机坑——
  Bus 只投递发布时已在册的消费者）+ `plugin.IsReviewKind` 死转发删除（v3.106.0
  自引入、复扫 deadcode 抓获——storage 直连 domain 后转发无人消费）。

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
