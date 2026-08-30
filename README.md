# Argus —— 可插拔多 Agent 审查平台（v3.0）

> 设计文档：`design.md`（v3.0 定稿，经六轮评审）。本 README 讲清楚"它是什么、怎么工作、怎么跑"。

## 一、Argus 是什么

Argus 是一个 **PR 评论 Bot + 异步任务平台**：给它一个 PR（webhook 自动触发，或 TaskAPI 手动提交），
它会：

1. **解析需求**——从 PR 描述 / 关联 issue / 分支名 `#N` 中改写出验收清单（AC-1..N）；
2. **核对完成度**——用带工具的 ReAct agent（`get_file`/`get_issue`/`get_diff`）逐条判定
   `implemented | partial | missing | unverifiable`，并检测需求外改动（scope creep）；
3. **委派专家插件**——并发调用可插拔的审查 agent（内置 LLM 审查、OCR CLI、远端 MCP 服务……）；
4. **汇总裁决**——确定性真值表（不走 LLM）给出 `approve | comment | request_changes`；
5. **回帖**——在 PR 上发/更新一份结构化报告（验收清单完成度 + scope creep + 行内评论 + 结论）。

### 分层架构（一句话版）

```
L1 形态   PR Bot（v3.0）｜CLI/TaskAPI（v3.0）｜MCP/A2A Server（v3.1 占位）
L2 门面   TaskAPI（异步任务）——webhook 和 CLI 最终都变成 tasks 表里的一行
L3 编排   requirement-compliance workflow（R1→R5，确定性 Go 代码）
L4 插拔   Registry → Dispatcher → Merger   ←—— 本项目的核心
L5 上下文 git-platform-sdk（diff/issue/文件）｜工作副本沙箱｜SQLite 存储｜skill/RAG/工具池扩展点
```

两条铁律贯穿全设计：

- **形态是 L1 适配器，agent 是 L4 插件**——加一种接入方式不动核心，加一个审查 agent 不动形态；
- **路由/裁决/合并不走 LLM**——确定性代码保证可解释、可复现；自主性只给 R3 阶段内的 ReAct 工具调用。

### R5 裁决真值表（按序短路）

| 序 | 条件 | 结论 |
|---|---|---|
| R5.1 | missing + partial > 0 | REQUEST_CHANGES（需求未完成，一票否决）|
| R5.2 | 存在 high finding | REQUEST_CHANGES |
| R5.3 | unverifiable>0 或 需求模糊/空清单 或 scope creep | COMMENT（人工确认）|
| R5.4 | 存在 medium/low finding | COMMENT |
| R5.5 | 其余 | APPROVE |

R5.3 的扩展是防注入的关键：攻击者即使通过 PR 描述注入指令踢掉难满足的 AC，极限效果也只是
"人工确认"，永远拿不到白给的 APPROVE。

## 二、目录导览（对照 design §5.2）

```
idl/argus.thrift               接口唯一事实来源（hz 生成路由/DTO）
biz/                           hz 生成壳：handler 薄实现调 internal/service
internal/
  config/                      环境变量配置（§6/§12 全表）
  plugin/                      L4：契约(AgentPlugin/Task/Result) + severity 词表 + 指纹
    ├ registry.go              plugins.yaml → 注册表；仓库配置信任边界（§3.4）
    ├ dispatcher.go            确定性路由 + fan-out + 超时 + 熔断(3败/5min) + 预算裁剪
    ├ merger.go                指纹去重（规范化文本哈希+file，行号±2仅辅助）+ 冲突取高 + 误报抑制
    ├ adapter_builtin/         requirement-compliance(workflow 外壳) + 通用审查 agent(skillRef 注入)
    ├ adapter_cli/             OCR CLI 适配器（进程组 exec：Setpgid→TERM→KILL；容错 JSON 解析）
    └ adapter_remote/          a2a/mcp 接口位（v3.0 占位）
  workflow/requirement/        R1 需求解析 / R2 变更理解 / R3 完成度(ADK agent+工具) / R4 委派 / R5 真值表
  context/                     diffprep(diff 清洗) + sdkctx(SDK 薄封装) + workcopy(浅克隆沙箱)
  storage/                     GORM 三表：tasks(幂等/去重/抢占/staleness) + finding_prints + review_targets
  llm/                         重试/JSON 反馈重试/token 记账
  poster/                      行内评论(CreateReview) + 总结评论三级降级(更新→扫描→新建) + staleness 门控
  runner/ worker/              任务执行编排 + DB 即队列的 worker pool
  knowledge/ toolpool/         扩展点：Skill(file 实现)+RAG(Noop)+MCP 工具池(mock)
  channel/mcpserver/           Argus as MCP Server 路由占位（v3.1）
test/e2e/                      双打桩端到端：mock OpenAI + fake Gitea + mock OCR CLI，9 个场景
```

## 三、关键设计如何落地（实现对照）

| 设计承诺 | 实现位置 | 要点 |
|---|---|---|
| webhook 幂等 | `storage.CreateTaskForWebhook` + `service.Webhook` | 幂等键=平台 delivery ID（`X-Gitea-Delivery` 等，ParseWebhookEvent **之前**读取）；header 缺失 event_id 置 NULL |
| 同 SHA 去重（含 in-flight 竞态） | `storage.CreateTaskForWebhook` | `last_head_sha` 双查 + pending/running 同 SHA 检查，**查询与落库同一事务**；SQLite 连接数=1 串行保正确性 |
| staleness（过期任务不覆盖新结果） | `storage.HasNewerNonFailedTask` + `poster.Post` | 单条件：存在同 PR 更晚创建的非 failed 任务 ⇒ stale；findings 照常入库，不回帖不覆盖 review_targets |
| 反注入四防线 | `prompts.go`(数据区包裹) + `antiinjection.go`(可疑 AC 进 Ambiguous / 无证据降级) + `Verdict`(只读结构化值) | LLM 输出不可信：implemented/partial 无 `file:line` 证据一律降级 unverifiable（代码级强制）|
| 仓库配置信任边界 | `plugin.ResolveRepoConfig` | 只从默认分支读；未注册名忽略+告警；lockRepoConfig 插件仓库配置关不掉；routing 不能新增 endpoint |
| 总结评论更新不刷屏 | `poster.postSummary` | review_targets 定位 → 失败扫 HTML 注释头 `<!-- argus:report ... -->` → 再失败新建；行内评论每轮独立 |
| 成本预算 | `plugin.TokenBudget` + `Dispatcher.PruneByBudget` | 任务内累计器判定（指标只观测）；裁剪按 cost 降序（heavy 先裁），lockRepoConfig 豁免+告警 |
| 部分失败容忍 | `Dispatcher.Run` + `merger.Degraded` | 单插件失败/超时/熔断 → 报告"降级注明"，其余照常 |
| 供应链信任 | `bootstrap`（LookPath 检查）| cli 二进制/remote endpoint 只能来自 plugins.yaml；可执行文件缺失→插件置 disabled，绝不换路径解析 |
| 仓库配置可观测 | `GET /agents?platform&owner&repo` + `plugin.ObserveRepoConfig` | 返回 .reviewagents.yaml 规范化原文 + 各插件生效状态（locked/selected/routed/excluded_by_use/disabled/default）+ 信任边界告警 |

## 四、快速开始

### 1. 准备

```bash
cd argus
cp plugins.example.yaml plugins.yaml   # 按需裁剪
# LLM：复用 lacus/.env（OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL）
```

### 2. 环境变量（全表见 design §12）

```bash
export ARGUS_ADDR=:8899
export ARGUS_DB_DSN=argus.db              # 集群部署换 MySQL DSN 即切
export ARGUS_APITOKEN=change-me           # /tasks /agents 的 Bearer token（生产必须）
export ARGUS_BOT_USERNAME=argus-bot       # webhook bot 自审保护
export ARGUS_GITEA_URL=https://gitea.example.com
export ARGUS_GITEA_TOKEN=xxx
export ARGUS_GITEA_SECRET=webhook-sign-secret
# GitHub/GitLab 同构：ARGUS_GITHUB_TOKEN|SECRET / ARGUS_GITLAB_URL|TOKEN|SECRET
# 可选：ARGUS_SKIP_DRAFT(默认true) ARGUS_MAX_CONCURRENT_TASKS(默认2)
#       ARGUS_TASK_TOKEN_BUDGET(默认200k) ARGUS_MAX_DIFF_CHARS ARGUS_PLUGINS(默认plugins.yaml)
```

### 3. 运行

```bash
go build -o argus . && ./argus
# 健康检查
curl localhost:8899/healthz
curl localhost:8899/readyz
```

### 4. 触发一次审查

```bash
# 方式 A：TaskAPI（与 webhook 任务同语义，同样回帖到 PR）
curl -X POST localhost:8899/tasks \
  -H "Authorization: Bearer $ARGUS_APITOKEN" -H "Content-Type: application/json" \
  -d '{"kind":0,"platform":"gitea","owner":"acme","repo":"app","number":"7"}'

curl localhost:8899/tasks/<taskId> -H "Authorization: Bearer $ARGUS_APITOKEN"   # 查询/取报告
curl localhost:8899/tasks?status=done -H "Authorization: Bearer $ARGUS_APITOKEN"  # 任务列表

# 误报反馈：把某条意见标记为抑制（之后每轮报告自动剔除）
curl -X POST localhost:8899/findings/suppress \
  -H "Authorization: Bearer $ARGUS_APITOKEN" -H "Content-Type: application/json" \
  -d '{"platform":"gitea","owner":"acme","repo":"app","number":"7","file":"a.go","comment":"意见原文"}'

# 插件可观测（含仓库配置视图：.reviewagents.yaml 生效状态 + 信任边界告警）
curl "localhost:8899/agents?platform=gitea&owner=acme&repo=app" -H "Authorization: Bearer $ARGUS_APITOKEN"

# 方式 B：webhook（平台签名自动校验，重发幂等）
curl -X POST localhost:8899/webhook/gitea -H "X-Gitea-Delivery: <uuid>" ...
```

### 5. 测试与验证

```bash
make test    # 单元测试（真值表/反注入/指纹/渲染/diffprep/存储，不联网）
make e2e     # E2E 九场景双打桩（mock LLM + fake Gitea + mock OCR，不联网）
make it      # 真实 LLM 集成测试（ARGUS_IT=1，走 lacus/.env 的 OPENAI_*，约 1-3 分钟）
make check   # vet + gofmt
```

`make it` 已实测：mimo-v2.5 真实模型下 R1 改写出精准验收清单、R3 判定 partial/missing 带
file:line 证据、correctness 插件抓出真实 medium 问题、R5.1 正确一票否决并回帖。

### 6. Webhook 接入步骤（以 Gitea 为例）

1. Gitea 仓库 → 设置 → Webhook → 添加 Gitea 类型 webhook：
   - 目标 URL：`http://<argus地址>:8899/webhook/gitea`
   - 请求方式：`POST`，内容类型：`application/json`（不要选 form）
   - 密钥文本：填 `ARGUS_GITEA_SECRET` 的同值（HMAC-SHA256 签名校验，错签 401 拒绝）
   - 触发条件：勾选"Pull Request"事件（或自定义只勾 opened/synchronize/reopened）
2. 保存后点"测试投递"：Argus 日志出现 `argus.webhook.accepted`（或 `skipped`+原因）即通。
3. 同步推送（synchronize）会更新既有报告评论而不是刷新评论；同 head SHA 重发/重复投递幂等。

### 7. Docker 部署

```bash
make docker                     # go mod vendor + 多阶段构建（静态二进制 + git）
docker run -d --name argus -p 8899:8899 -v argus-data:/data \
  -e ARGUS_APITOKEN=change-me \
  -e ARGUS_GITEA_URL=https://gitea.example.com \
  -e ARGUS_GITEA_TOKEN=xxx \
  -e ARGUS_GITEA_SECRET=webhook-secret \
  argus:v3.0.0
```

镜像内含 git（workcopy 浅克隆用）；SQLite 与工作副本沙箱落 `/data` 卷，容器重建任务不丢。
平台不可达时启动只告警降级（`/readyz` 可见），不会 crash loop。

## 五、仓库侧配置（.reviewagents.yaml）

放在**业务仓库默认分支**，只能"选"不能"增"（引用未注册插件会被忽略并告警）：

```yaml
use: [requirement-compliance, ocr]      # 白名单（锁定插件不受影响，永远参与）
disable: [correctness]
routing:
  - {paths: ["web/**", "*.vue"], use: [frontend-visual]}
  - {labels: ["ui"], use: [frontend-visual]}
```

## 六、真实 Gitea 试点

接入内网 Forgejo（git.example.com）的完整步骤、验收清单与已知边界见 **[PILOT.md](PILOT.md)**。
要点：需要你生成一个 Forgejo Access Token（唯一人工步骤）；证书过期用 `ARGUS_GITEA_SKIP_TLS=1`；
webhook 一键注册：`./scripts/setup-webhook.sh <owner/repo>`。

## 七、运维与健壮性（v3.0.1 加固）

- **优雅停机**：SIGTERM 后 HTTP 先停，worker 停止领取新任务并给在途审查 60s 自然完成
  （`wp.Stop(60s)`）；超时才硬取消，任务保持 running、下次启动自动重置回 pending——
  部署重启不会把跑到一半的审查杀掉重跑、双倍消耗 token。
- **任务保留策略**：`ARGUS_TASK_RETENTION_DAYS=30`（0=永久）——超期的已终结任务
  （done/failed/stale）启动时+每日自动清理；pending/running 永不清理。
- **队列积压可观测**：`argus_tasks_pending` / `argus_tasks_running` gauge（30s 采样），
  积压一眼可见。
- **日志卫生**：预期内"查无记录"（worker 空轮询、webhook 重放查询等）已静默，
  不会每 500ms 刷红色错误淹没真实告警。
- **凭据安全**：workcopy 克隆失败的报错会脱敏嵌入 URL 的 token；`SKIP_TLS=1` 时
  启动日志有明示告警。
- **单实例约束**：SQLite 模式下 Argus 必须单实例运行（连接数=1 + 写锁保证幂等/
  去重事务性）；集群多副本部署必须切 MySQL。

## 八、部署定位

**学习期**：本机/内网运行，SQLite 单文件（`ARGUS_DB_DSN=argus.db`），LLM 复用 lacus `.env`；
镜像构建自包含（vendor，无需私有 Gitea 凭证）：`make docker` → `argus:v3.0.0`。
**后续**：推 Harbor 走 buildkit+ArgoCD 上 studio 集群（`cloud=studio-amd64`），DSN 切 MySQL，
Prometheus 指标（`/metrics`，argus_* 前缀）对接现有 kube-prometheus-stack，日志已结构化（logx）。

**生产清单（上线前自查）**：`ARGUS_APITOKEN` 必须设置——未配置时 🔒 路由（/tasks /agents）
fail-closed 拒绝服务（503），webhook 与健康检查不受影响；
`ARGUS_*_SECRET` 三个平台各自配置（空 secret 时 SDK 会跳过签名校验）；OCR 二进制用
`scripts/install-ocr.sh` 安装并留存 sha256（§8 供应链信任）；worker 数按 LLM 配额调
`ARGUS_MAX_CONCURRENT_TASKS`；磁盘监控 `/data`（SQLite + 工作副本沙箱，后者有 1h TTL 兜底回收）。

## 八、当前边界（v3.0 未做，接口已留）

- remote 插件（a2a/mcp 协议子集）：`adapter_remote` 占位，v3.1 接 review-service MCP server；
- cli `delegate` 模式：接口留好（生产参考 review-service `internal/ocr/delegate.go`）；
- 误报反馈通道：`finding_prints.status=suppressed` 表结构已备，入口 v3.1 定；
- MCP Server 门面（`/mcp` 501 占位）、A2A Server、SSE 会话、RAG 实装、IM 通知。
