# review-service 对接现状（Argus v3.9.x）

> 本文 2026-08-30 版为纯调研（方案 B：独立引擎 + 协议对接，v3.0 设计补充）。
> 2026-09-16/17 起**对接已部分落地**（push 上报 + 反馈回流），本文改记现状；
> 调研结论中仍有效的部分压缩保留在 §1。
> 真实仓位置：`/Users/demo-user/my_project/review-service`（旧文写的 iCloud 路径已过时；
> 另有兄弟目录 review-platform/review-console）。

---

## 1. review-service 是什么（调研结论，仍有效）

生产级**代码质量评审网关**（Go + Hertz + uber/fx + GORM/PostgreSQL + Vue 控制台），
与 argus 使用同一套 `git-platform-sdk`。已具备：五平台 webhook 接入（PG 持久化队列/
重试/死信）、OCR 子进程集成、多 agent workflow + discussion 编排器、行内评论回写 +
commit status 质量门禁、LLM 成本预算、finding 指纹防重、周报/Grafana/web 控制台。

关键发现：其 `internal/service/requirements/` 完成度判定是**词法匹配占位实现**
（`strings.Contains`，无 LLM、无证据引用、不读仓库代码）——argus 的 requirement-compliance
workflow（R1–R5，LLM agent + 证据工具）是该模块的升级替换路径，互补而非竞争：
**review-service 守"代码质量 + 平台接入"，argus 管"需求做没做完 + agent 插拔编排"**。

## 2. 已落地的对接（v3.9.x：push 上报 + 反馈回流）

### 2.1 结果上报（argus → review-service，单向旁路）

- **argus 侧 sink**：`internal/sink`（Reporter + BuildPayload）。审查管线终点
  （报告落库/评论回写后）POST `ARGUS_SINK_URL`；Bearer `ARGUS_SINK_TOKEN`；
  MaxFindings=500、10s 超时、2 次重试；异步旁路**失败只告警不阻断**。
  `ARGUS_SINK_URL` 非空即启用，runner 有 `Sink.Enabled()` 守卫（关闭时零开销）。
- **payload**：`{argus_task_id, platform/owner/repo/number/title/head_sha/base_branch,
  verdict（R5 真值表结果，非模型自评）, findings[{file,line,severity,comment,
  suggested_fix,fp,by}], stats{plugins,duration_ms,token_usage}, occurred_at}`。
- **review-service 侧**：`internal/domain/argusingest`（纯领域类型）+ postgres 实现
  （migration `0008_argus_ingest.sql`；upsert 业务键 (platform,owner,repo,number,
  head_sha)，意见先删后插 (review_id,fp) 部分唯一索引 fp<>' '）+
  `POST /api/v1/argus/reviews`（ingest）+ list/detail 查询端点 + Vue「Argus 审查」
  任务清单/详情两页。鉴权 Bearer `ARGUS_INGEST_TOKEN`。

### 2.2 误报抑制回流（review-service → argus，反馈闭环）

- review-service 前端详情页「误报抑制」按钮 → `POST /api/v1/argus/reviews/:id/findings/suppress`
  → 服务端代理调用 **argus 已有** `POST /findings/suppress`（🔒 `ARGUS_APITOKEN`）。
- fp 归属校验通过且 argus 返回 matched 才落 `suppress_state`（migration 0009）；
  argus 学习环随之生效——两系统反馈闭环打通。
- argus 侧契约：`{platform,owner,repo,number 必填 + fingerprint(≥4 位前缀) / file+comment 二选一}`。

### 2.3 状态

两侧代码均已实施并各自通过单测/build/lint（argus 侧随 v3.8.0+v3.9.0 合批已提交
main 192d6fa；review-service 侧含 IDL 化改造，**仓内提交待办**）。
剩余：argus-smoke 仓全链路联调（webhook→审查→上报→前端→抑制回流）。

## 3. 长期方向（未实施）

- **remote/mcp 通道**：review-service 已有现成 MCP server（`create_review`/
  `get_review`/findings），design §3.5 已修订为 **MCP-first**——由 review-service
  编排 argus 作为审查专家（互证场景），argus 侧 plugins.yaml `review-service` 条目
  已留位（`enabled:false` 等就绪）。旧文 §3「HTTP/a2a 注册进 AgentManager」设想由
  MCP-first 取代。
- Dashboard 概览卡（review-service 前端，可选）。

## 4. argus 独立性（2026-09-16 已逐项确认，勿重查）

argus 可完全脱离 review-service 独立运行：go.mod/源码零依赖；sink 单向可选旁路；
plugins.yaml `review-service` 通道条目 enabled:false 且仓库配置无权打开；OCR 三通道
（ocrshim/delegate/cli）均不经 review-service；数据主权在 argus（指纹/抑制/学习环/
报告全在本地，review-service 只替用户调 argus API）。独立运行最小依赖 = git 平台
（webhook 或轮询）+ LLM 凭证。
