# review-service 调研与对接评估（Argus v3.0 补充设计）

日期：2026-08-30
定位：本文档是对 `design.md`（Argus v3.0 详细设计）的**补充**——对既有生产系统
review-service 的调研结论，以及 Argus 与它的对接方案。不改变 design.md 已拍板的
v3.0 范围与分层架构。

---

## 1. review-service 是什么

位置：iCloud `~/Library/Mobile Documents/com~apple~CloudDocs/Documents/review-service`
（Go 1.26 + Hertz + uber/fx + GORM/PostgreSQL + Vue 控制台）。

生产级**代码质量评审网关**，与本设计使用同一套 `git-platform-sdk`。已具备：

| 能力 | 实现要点 |
|---|---|
| 多平台 webhook 接入 | GitHub/GitLab/Gitea/GitCode/腾讯工蜂，签名校验、PG 持久化队列/重试/死信 |
| OCR 子进程集成 | `internal/ocr` 单一入口、`BuildConfig` 唯一配置合并、exec 三段式终止（SIGTERM→KillGrace→SIGKILL） |
| 多 agent workflow | `internal/service/workflow` + `agent.AgentManager` + discussion 编排器（agent 间多轮讨论已实现） |
| 回写 | 行内评论 + commit status 质量门禁（`docs/webhook-writeback-design.md`） |
| 工程配套 | LLM 成本预算、飞书/企微/钉钉通知、finding 指纹防重复误报、周报、Grafana、web 控制台 |

**结论：Argus 不重造接入层/回写层/质量审查引擎**——这些 review-service 已生产化，
且其 OCR 集成结论与 design.md §2 的"cli 插件主路径"一致（相互印证）。

## 2. 关键发现：需求复核是它的短板，恰是 Argus 的位置

`internal/service/requirements/` 模块已接入主流程（`WorkflowConfig.EnableRequirements`），
但完成度判定是**词法匹配占位实现**（`evaluator.go`）：

```go
// 需求文本分词后在 finding 标题+正文里 strings.Contains，命中即 partial/0.5
reqWords := strings.Fields(strings.ToLower(req.Text))
if len(word) > 3 && strings.Contains(findingText, word) { ... }
```

无 LLM、无证据引用、不读仓库代码。**Argus 的 requirement-compliance workflow
（R1-R5，LLM agent + 证据工具）就是该模块的升级替换路径**——这印证了 v3.0
"需求复核 workflow"切入点选得准。

## 3. 对接方案（方案 B：独立引擎 + 协议对接）

边界：**review-service 管"怎么把意见送到平台上"（接入/回写/门禁/成本），
Argus 管"需求做没做完 + agent 插拔编排"**。

| 对接面 | 机制 |
|---|---|
| Argus 注册进 review-service | 把 requirement-compliance 能力以 HTTP/a2a 形式注册进 `agent.AgentManager`，其 discussion 编排可实现"质量专家 ↔ 需求复核 agent"多轮互证（如质量专家发现某文件大改 → 复核 agent 追查是否需求外改动） |
| Argus 复用 review-service 接入 | Argus 独立部署时可不开自己的 webhook，由 review-service 审查流程中作为专家被调用 |
| 反向 | review-service 也可作为 Argus 的一个 cli/a2a 插件被调用（历史审查记录、finding 指纹去重） |

与 design.md L4 插拔层的关系：review-service 在 Argus 眼中就是**一个外部专家插件**
（a2a 或 cli 形态），不需要特权地位——这正好验证了插件契约
`AgentPlugin{Manifest/Invoke}` 的抽象。

## 4. 可直接借鉴的实现细节

1. **diff 动态截断**：`workflow.GetMaxDiffChars()` 按上下文窗口 35% 动态算上限
   （min 10k / max 500k，4 chars/token 估算）——优于固定阈值，纳入 L5 上下文层。
2. **exec 三段式终止**：SIGTERM → KillGrace → SIGKILL，OCR cli 插件的子进程管理照抄。
3. **BuildConfig 单点配置合并**（overrides > settings > fallback > defaults）：
   Argus 插件配置合并采用同一次序与单实现原则。
4. **finding 指纹**：跨轮次识别同一问题 + 误报抑制——Argus 增量重审（R1-R5 的
   synchronize 路径）直接复用该思路。
5. **精读清单**：`docs/webhook-writeback-design.md`（回写流）、
   `docs/OCR_CORE_DESIGN.md`（能力收口为单一入口的重构范式，Argus 的 internal
   包组织可参考）、`docs/CODE_REVIEW_POLICY.md`（敏感区审查门禁）。

## 5. 结论

- Argus 与 review-service 是**互补而非竞争**：前者补"需求复核 + agent 插拔平台"，
  后者守"代码质量 + 平台接入"。
- 对接走插件契约（a2a/cli），不引入特权耦合；v3.0 范围不变。
- requirements 占位实现的存在证明需求复核有真实升级需求，Argus 切入点成立。
