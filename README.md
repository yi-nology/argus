# Argus —— PR 自动审查 Bot（可插拔多 Agent 审查平台）

> 给它一个 PR，它读完代码、对照需求、派出多个审查专家，然后把一份带证据的报告回帖到 PR 上，
> 并给出 `APPROVE / COMMENT / REQUEST_CHANGES` 的正式评审结论。
> 设计文档：`design.md`（六轮评审定稿）｜试点手册：`PILOT.md`｜变更记录：`CHANGELOG.md`

当前版本 **v3.0.2**（v3.0.1 全面审查修复版），已在真实 Forgejo（git.example.com）完成完整生命周期试点验证。

---

## 一、Argus 能做什么

### 1. 需求复核：不只看代码，还看"做没做完"

- 自动从 **PR 描述 / 关联 issue（分支名 `#N` 也能关联）** 改写出验收清单（AC-1..N）；
- 用带工具的 ReAct agent（`get_file` / `get_issue` / `get_diff`）**逐条判定**
  `implemented | partial | missing | unverifiable`，每条判定必须带 `file:line` 证据——
  无证据的判定会被代码级强制降级为 unverifiable；R3 少报/漏报的条目会被**双向对齐**
  补齐为需人工确认（LLM 少写几条就想把 partial PR 洗成 APPROVE？不存在）；
- 检测**需求外改动（scope creep）**：改了清单之外的东西会单独列出，请人确认。

### 2. 多专家并发审查：一个 PR，多双眼睛

- 插件式架构（`plugins.yaml` 声明），单插件失败/超时/熔断只做"降级注明"，不拖垮整轮审查；
- 内置 **requirement-compliance**（上面的需求复核全流程）、**correctness**（通用代码审查）、
  **security**（安全审查，仓库配置禁不掉）三类 LLM agent；
- 可插 **CLI 外部工具**（内置 OCR：alibaba/open-code-review，进程组托管、容错 JSON 解析）
  和 **remote 远端 agent**（a2a/mcp 接口位已留）；
- 支持**方法论注入**（skillRefs）：把 SKILL.md 里的评分标准注入对应阶段的 prompt；
- **确定性路由与合并**：按 paths/labels/事件路由插件，指纹去重（不同专家发现同一问题只报一次）、
  冲突取高severity、token 预算吃紧时 heavy 插件先裁（锁定插件豁免）。路由/裁决/合并不走 LLM，可解释、可复现。

### 3. 确定性裁决 + 双层回帖

按序短路的真值表（`R5.1`–`R5.5`，见下文）给出结论，然后：

- **行内评论**：审查意见钉到具体代码行，随 CreateReview 事件附上（每轮独立）；
- **总结报告**：PR 下一条结构化评论，多轮审查**原地更新**不刷屏（三级降级：定位更新 → 扫描
  HTML 注释头 → 新建）。实际长这样：

  ```markdown
  <!-- argus:report task_id=t-abc head=sha123 ver=v3.0.1 -->
  ## 📋 Argus 需求复核报告（需求源: issue #12）

  ### 验收清单完成度: 1/2 ⛔
  | AC | 判定 | 证据 |
  |---|---|---|
  | AC-1 支持分页 | ✅ implemented | api/list.go:42 |
  | AC-2 导出 CSV | ❌ partial | 只实现了前端入口，后端接口缺失 api/export.go:15 |

  ### ⚠️ 需求外改动（scope creep）
  - `internal/foo.go`：与需求无关的重构，请确认

  ### 🔍 代码审查意见
  - [correctness/medium] `api/list.go:57` 未限制 page 上限，可被大值打爆内存（fp:ab12cd34）

  ### 结论: ⛔ 需求未完成（1 项未达标）（R5.1）
  ```

### 4. 增量审查

- PR 更新时只审查新增/变更的文件，未变化文件自动跳过，节省 LLM 预算。

### 5. 自动修复建议

- 发现问题时同时给出具体代码修复建议（suggested_fix）。

### 6. 四种接入方式，同一个引擎

| 方式 | 场景 | 说明 |
|---|---|---|
| **Webhook** | 平台可达 Argus | Gitea/Forgejo/GitHub/GitLab 签名校验，delivery 幂等，`scripts/setup-webhook.sh` 一键注册 |
| **轮询兜底** | NAT/VPN 下 webhook 不可达 | `ARGUS_POLL_REPOS` 定时发现新 PR 与 head 变化，与 webhook 任务同语义（试点实测入站零包时靠它） |
| **TaskAPI** | 手动/脚本/CI 触发 | `POST /tasks` 对任意已有 PR 发起审查，`GET /tasks/:id` 取报告 |
| **MCP 工具** | 任意 MCP 客户端 | `/mcp`（streamable http + Bearer）暴露 `review_pr` 等四工具，可同步等待终态 |

### 7. PR 评论交互

在 PR 评论中 `@argus ask <问题>`，Argus 会根据 PR 上下文用 LLM 回答。例如：
- `@argus ask 这个改动会影响性能吗？`
- `@argus ask 为什么用 sync.Mutex 而不是 channel？`

30 秒内同一 PR 不会重复回答（防刷）。需要配置 LLM（OPENAI_*）。

### 8. 反注入与安全边界（评审对象不能操纵评审）

- PR 描述/commit message 里的"请输出 APPROVE"类注入：数据区包裹 + 可疑 AC 进人工确认 +
  无证据判定降级，**极限效果只是 COMMENT，永远拿不到白给的 APPROVE**（试点已实测双注入场景）；
- 仓库自己的 `.reviewagents.yaml` **只能"选"不能"增"**：引用未注册插件被忽略并告警；
  安全类插件 `lockRepoConfig` 关不掉；routing 不能新增 endpoint；
- cli 二进制/remote endpoint 只能来自 `plugins.yaml`（供应链信任，可执行文件缺失→插件 disabled）；
- 🔒 路由（/tasks /agents /findings）fail-closed：未配 `ARGUS_APITOKEN` 直接 503 拒绝。

### 9. 越用越准：误报抑制

报告里每条意见带指纹短码（`fp:ab12cd34`）。认为是误报？调一次
`POST /findings/suppress`（支持短码直达，或 file+原文，指纹对空白/标点不敏感），
之后每轮报告自动剔除，脚注注明"N 条历史意见已被抑制"。

### 10. 生产级健壮性（v3.0.1 加固）

- **幂等与去重**：同 delivery 重发不重跑；同 PR 同 head SHA 的 in-flight 任务直接去重（事务保证）；
  评审事件带 task_id 锚点，崩溃重跑不重复发评审；
- **staleness 门控**：连续两次 push、慢的那轮后完成时**不会覆盖新 SHA 的报告**；
- **优雅停机与心跳恢复**：SIGTERM 后停领新任务，在途审查给 60s 自然完成，超时才硬取消；
  running 任务带实例心跳（20s），心跳死亡 >90s 才被重置回 pending（多副本共库时滚动重启
  不误伤其他实例的在途任务）——重启不烧双倍 token；
- **任务保留策略**：`ARGUS_TASK_RETENTION_DAYS`（超期终态任务启动时+每日清理）；
- **可观测**：`/metrics` 暴露 `argus_tasks_total/pending/running`、`argus_llm_tokens_total`、
  插件耗时/失败、阶段耗时；`ARGUS_DEBUG=1` 可留存 R1 spec/R3 原始输出到任务记录；
- **平台降级**：平台不可达时启动只告警降级（`/readyz` 可见），不会 crash loop；
- 凭据脱敏（克隆 URL 里的 token）、日志静默（预期内 not-found 不刷屏）、单实例约束（SQLite 下
  必须单副本，多副本切 MySQL）。

---

## 二、裁决真值表（按序短路，确定性 Go 代码）

| 序 | 条件 | 结论 |
|---|---|---|
| R5.1 | missing + partial > 0 | **REQUEST_CHANGES**（需求未完成，一票否决）|
| R5.2 | 存在 high finding | **REQUEST_CHANGES** |
| R5.3 | unverifiable>0 / 需求模糊或空清单 / scope creep | **COMMENT**（人工确认）|
| R5.4 | 存在 medium/low finding | **COMMENT** |
| R5.5 | 其余 | **APPROVE** |

R5.3 是防注入的兜底：攻击者即使通过注入踢掉难满足的 AC，结果也只是"人工确认"。

---

## 三、一次审查的旅程

```
PR opened/push ─┬─ webhook（签名校验、delivery 幂等）
                ├─ poller（轮询发现，同 SHA 去重）
                ├─ POST /tasks（Bearer）
                └─ MCP review_pr
                        │
                        ▼
              tasks 表（DB 即队列，worker pool 领取）
                        │
                        ▼
  R1 需求解析 → R2 变更理解 → R3 完成度判定(ReAct+工具)
  → R4 并发委派插件(builtin/cli/remote) → R5 真值表裁决
                        │
                        ▼
      行内评论(CreateReview) + 总结报告(原地更新)
```

---

## 四、快速开始

### 1. 本地跑（学习期形态：SQLite 单文件）

```bash
cd argus
cp plugins.example.yaml plugins.yaml          # 按需裁剪
export ARGUS_ADDR=:8899
export ARGUS_APITOKEN=change-me               # 🔒 路由凭证，不配则拒绝服务
export ARGUS_BOT_USERNAME=argus-bot           # webhook bot 自审保护
export ARGUS_GITEA_URL=https://git.example.com
export ARGUS_GITEA_TOKEN=<token>              # GitHub/GitLab 同构：ARGUS_GITHUB_* / ARGUS_GITLAB_*
export ARGUS_GITEA_SECRET=webhook-secret
# LLM 走 OpenAI 兼容协议：OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL（复用 lacus/.env 即可）
make run                                      # 编译并启动，自动加载 lacus/.env 与 ./.env
curl localhost:8899/healthz && curl localhost:8899/readyz
```

启动日志确认三行：`platform_ready` / `plugin_registered` / `http_ready`。

### 2. 触发一次审查

```bash
# 方式 A：TaskAPI（不依赖 webhook，先跑通它）
curl -X POST localhost:8899/tasks \
  -H "Authorization: Bearer $ARGUS_APITOKEN" -H "Content-Type: application/json" \
  -d '{"kind":0,"platform":"gitea","owner":"acme","repo":"app","number":"7"}'

curl localhost:8899/tasks/<taskId> -H "Authorization: Bearer $ARGUS_APITOKEN"   # 状态/报告
curl "localhost:8899/tasks?status=done&limit=20" -H "Authorization: Bearer $ARGUS_APITOKEN"

# 方式 B：webhook（平台签名自动校验，重发幂等）
./scripts/setup-webhook.sh acme/app           # 一键注册；之后开 PR / push 即自动审查
```

### 3. MCP 客户端接入

```json
{"url": "http://<argus地址>:8899/mcp", "headers": {"Authorization": "Bearer $ARGUS_APITOKEN"}}
```

| 工具 | 说明 |
|---|---|
| `review_pr` | 发起审查并可选等待终态（platform/owner/repo/number + kind/wait/timeout_seconds，等待上界 10 分钟），返回 task_id/status/verdict/report |
| `requirement_compliance` | 需求复核固定形态（等价 review_pr 默认）|
| `list_agents` | 插件清单；三参全传附带仓库配置视图 |
| `get_report` | 按 taskId 取状态/verdict/完整报告 |

### 4. API 一览

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `GET /healthz` `GET /readyz` | 无 | 健康/就绪（平台降级在 readyz 可见）|
| `GET /metrics` | 无 | Prometheus 指标（argus_* 前缀）|
| `POST /webhook/{gitea\|github\|gitlab}` | HMAC 签名 | 平台事件入口，delivery 幂等 |
| `POST /tasks` / `GET /tasks` / `GET /tasks/:id` | 🔒 Bearer | 发起 / 列表（?status=&limit=）/ 查询（含 debug 字段）|
| `POST /findings/suppress` | 🔒 Bearer | 误报抑制（fingerprint 短码或 file+原文）|
| `GET /agents?platform&owner&repo` | 🔒 Bearer | 插件清单 + .reviewagents.yaml 生效视图 + 信任边界告警 |
| `POST /mcp` | 🔒 Bearer | MCP streamable http |

### 5. 常用环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ARGUS_ADDR` | `:8899` | 监听地址 |
| `ARGUS_DB_DSN` | `argus.db` | SQLite 文件；切 MySQL DSN 即集群化（多副本必须）|
| `ARGUS_APITOKEN` | 空 | 🔒 路由 Bearer；空则 fail-closed 503 |
| `ARGUS_BOT_USERNAME` | - | bot 账号名（自审保护；生产建议独立账号）|
| `ARGUS_MAX_CONCURRENT_TASKS` | 2 | worker 数，按 LLM 配额调 |
| `ARGUS_TASK_TOKEN_BUDGET` | 200k | 单任务 token 预算，heavy 插件先裁 |
| `ARGUS_MAX_DIFF_CHARS` / `ARGUS_MODEL_CONTEXT_TOKENS` | - | diff 截断 / 上下文预算 |
| `ARGUS_SKIP_DRAFT` | true | 跳过 Draft PR |
| `ARGUS_TASK_RETENTION_DAYS` | 0(永久) | 终态任务保留天数 |
| `ARGUS_POLL_INTERVAL` / `ARGUS_POLL_REPOS` | - | 轮询兜底（webhook 不可达时开）|
| `ARGUS_PLUGINS` | plugins.yaml | 插件声明文件 |
| `ARGUS_SKILLS_DIR` / `ARGUS_WORK_ROOT` / `ARGUS_REPORT_META` / `ARGUS_DEBUG` | - | 方法论目录 / 沙箱根 / 报告元信息 / 调试留存 |
| `ARGUS_{GITEA,GITHUB,GITLAB}_{URL,TOKEN,SECRET,SKIP_TLS}` | - | 平台凭证；**SECRET 必配**——空 secret 时该平台 webhook fail-closed 503（SDK 空 secret 会跳过签名校验）|
| `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` | - | LLM（OpenAI 兼容协议）|
| `ARGUS_LLM_RATE_LIMIT` | 0（禁用）| LLM 请求速率限制（token bucket，单位 req/s）|
| `ARGUS_LLM_RATE_BURST` | =MaxConcurrentTasks | 速率限制器突发容量 |
| `ARGUS_WEBHOOK_MAX_BODY` | 1048576（1MB）| Webhook 请求体最大字节数 |
| `ARGUS_GITHUB_APP_ID` | 空 | GitHub App ID（替代 PAT 的另一种认证方式）|
| `ARGUS_GITHUB_APP_PRIVATE_KEY` | 空 | GitHub App 私钥（PEM 格式）|
| `ARGUS_GITHUB_APP_INSTALLATION_ID` | 空 | GitHub App installation ID |

### 6. 仓库侧配置（.reviewagents.yaml，放业务仓库默认分支）

```yaml
use: [requirement-compliance, ocr]      # 白名单（锁定插件不受影响，永远参与）
disable: [correctness]
routing:
  - {paths: ["web/**", "*.vue"], use: [frontend-visual]}
  - {labels: ["ui"], use: [frontend-visual]}
```

只能"选"不能"增"——引用未注册插件会被忽略并告警；`lockRepoConfig` 插件关不掉。

---

## 五、部署

两种形态，详见 **[deploy/README.md](deploy/README.md)**：

```bash
# docker-compose（单机/内网，compose 冒烟已通过）
make compose-up                               # 8899 被占时改 .env 的 ARGUS_HOST_PORT

# k8s（kustomize；studio/test 集群可接 buildkit+Harbor+ArgoCD）
make k8s-apply                                # 注意 replicas=1 + Recreate（SQLite 单写模型）
```

**生产上线自查**：`ARGUS_APITOKEN` 必配（fail-closed 503）；各平台 `SECRET` 必配（v3.0.2 起空
secret 的 webhook 直接拒绝服务）；OCR 二进制用 `scripts/install-ocr.sh` 安装并留存 sha256；
SQLite 单实例约束（MySQL 模式支持实例崩溃后的心跳级任务恢复，多活副本尚未支持）；
磁盘监控 `/data`（SQLite + 工作副本沙箱，后者 1h TTL 兜底回收）。

---

## 六、插件声明（plugins.yaml 要点）

```yaml
agents:
  - name: requirement-compliance   # R1→R5 整条 workflow 包成的插件
    kind: builtin                  # builtin | cli | remote
    skills: [requirement-compliance]
    needs: [diff, repo-file-read, requirement-spec]
    lockRepoConfig: true           # 仓库配置不可禁用/排除
    skillRefs: [ocr-grading]       # SKILL.md 方法论注入 R3
    cost: heavy                    # fast | standard | heavy（预算吃紧 heavy 先裁）
    timeout: 600s
```

- `kind: cli`：白名单可执行文件（供应链信任），`mode: full` 全权审查 / `delegate` 只要筛选规则（v3.1）；
  `env: [VAR...]` 声明按名透传的环境变量白名单（缺省最小环境，密钥类绝不继承——cli 在待审仓库工作树里执行）；
- `kind: remote`：`protocol: a2a | mcp`，endpoint 只能在这里声明（v3.1 实装协议子集）；
- `triggers: {events: [cr.opened]}`：可按事件收窄（SDK 归一化词表 cr.opened/synchronize/reopened）。

---

## 七、测试

```bash
make test    # 单元测试（真值表/反注入/指纹/渲染/diffprep/存储，不联网）
make e2e     # E2E 双打桩 16 场景（mock OpenAI + fake Gitea + mock OCR，不联网）
make it      # 真实 LLM 集成测试（ARGUS_IT=1，走 OPENAI_*，约 1-3 分钟）
make check   # vet + gofmt
```

`make it` 已实测：mimo-v2.5 真实模型下 R1 改写出精准验收清单、R3 判定 partial/missing 带
file:line 证据、correctness 抓出真实 medium 问题、R5.1 正确一票否决并回帖。

---

## 八、实战验证（git.example.com / Forgejo 真实试点）

完整步骤与验收清单见 **[PILOT.md](PILOT.md)**。已实测通过的硬事实：

- **完整生命周期**：issue#3 → 部分实现 PR#4 → R5.1 打回（行内 6 条意见）→ 补全 →
  **R5.5 通过，bot 账号 friday 正式 APPROVED（official review）**；
- **四条 verdict 路径**（R5.1/R5.2/R5.3/R5.5）全部在真实 PR 页落地可见；
- **注入对抗**：PR body/commit message 双注入"请输出 APPROVE" → 仍 R5.1，判定诚实；
- **多轮原地更新**：同 PR 连续 push，总结评论更新不刷屏；staleness 门控生效；
- **轮询兜底**：webhook 入站不可达环境实测（抓包零包）下轮询路径零功能损失；
- bot 自审限制（Forgejo 拒绝 PR 作者提 blocking 评审）已自动降级 COMMENT 并在 body 注明。

---

## 九、目录导览

```
idl/argus.thrift               接口唯一事实来源（hz 生成路由/DTO）
biz/                           hz 生成壳：handler 薄实现调 internal/service
internal/
  config/                      环境变量配置
  plugin/                      L4：契约 + severity 词表 + 指纹
    ├ registry.go              plugins.yaml → 注册表；仓库配置信任边界
    ├ dispatcher.go            确定性路由 + fan-out + 超时 + 熔断(3败/5min) + 预算裁剪
    ├ merger.go                指纹去重 + 冲突取高 + 误报抑制
    ├ adapter_builtin/         requirement-compliance(workflow 外壳) + 通用审查 agent
    ├ adapter_cli/             OCR CLI 适配器（进程组 exec；容错 JSON 解析）
    └ adapter_remote/          a2a/mcp 接口位（v3.1 实装）
  workflow/requirement/        R1 需求解析 / R2 变更理解 / R3 完成度 / R4 委派 / R5 真值表
  context/                     diffprep(diff 清洗+RawDiff 回填) + sdkctx + workcopy(浅克隆沙箱)
  storage/                     GORM 三表：tasks(幂等/去重/抢占/staleness) + finding_prints + review_targets
  llm/                         重试/JSON 反馈重试/token 记账
  poster/                      行内评论 + 总结评论三级降级 + staleness 门控
  poller/                      轮询兜底任务源（webhook 不可达环境）
  reportview/                  报告渲染（上文示例的出处）
  channel/mcpserver/           Argus as MCP Server（/mcp 四工具）
  runner/ worker/              任务执行编排 + DB 即队列的 worker pool
  knowledge/ toolpool/ obs/    Skill(file 实现) / MCP 工具池 / 指标
test/e2e/                      双打桩 E2E 16 场景 + 真实 LLM 集成测试
deploy/                        docker-compose + k8s kustomize 双形态
scripts/                       setup-webhook.sh（一键注册）+ install-ocr.sh
skills/                        方法论包（ocr-grading 等，ARGUS_SKILLS_DIR 加载）
```

**设计骨架**（详见 `design.md`）：

```
L1 形态   PR Bot ｜ CLI/TaskAPI ｜ MCP/A2A Server
L2 门面   TaskAPI——webhook/轮询/CLI 最终都变成 tasks 表里的一行
L3 编排   requirement-compliance workflow（R1→R5，确定性 Go 代码）
L4 插拔   Registry → Dispatcher → Merger   ←—— 核心扩展点
L5 上下文 git-platform-sdk ｜ 工作副本沙箱 ｜ SQLite/MySQL ｜ skill/RAG/工具池
```

两条铁律：**形态是 L1 适配器，agent 是 L4 插件**（加接入方式不动核心，加审查 agent 不动形态）；
**路由/裁决/合并不走 LLM**（可解释、可复现，自主性只给 R3 的 ReAct 工具调用）。

---

## 十、当前边界（接口已留，v3.1 方向）

- remote 插件协议子集（a2a/mcp）：`adapter_remote` 接口位就绪，接 review-service MCP server；
- cli `delegate` 模式：接口留好（生产参考 review-service `internal/ocr/delegate.go`）；
- RAG 实装、A2A Server、SSE 会话、IM 通知。

---

*设计文档：[design.md](design.md) · 试点手册：[PILOT.md](PILOT.md) · 部署：[deploy/README.md](deploy/README.md) · 变更记录：[CHANGELOG.md](CHANGELOG.md)*
