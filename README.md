# Argus —— PR 自动审查 Bot（可插拔多 Agent 审查平台）

> 给它一个 PR，它读完代码、对照需求、派出多个审查专家，然后把一份带证据的报告回帖到 PR 上，
> 并给出 `APPROVE / COMMENT / REQUEST_CHANGES` 的正式评审结论。
> 设计文档：`design.md`（六轮评审定稿）｜试点手册：`PILOT.md`｜变更记录：`CHANGELOG.md`

当前版本 **v3.91.0**（版本口径以 origin/main 提交历史为准，CHANGELOG 记至 v3.69.0；依赖以 `go.mod` 为准）。真实 Forgejo（git.example.com）
完整生命周期试点验证通过，并已在生产环境常驻运行（内网 GitLab git.example.com：4 仓库 webhook、
OCR 等 4 插件启用）。v3.1→v3.5 演进全记录见 [CHANGELOG.md](CHANGELOG.md)，架构深读见
[docs/architecture-deep-dive.md](docs/architecture-deep-dive.md)；review 闭环设计见
[docs/review-closed-loop-design.md](docs/review-closed-loop-design.md)。

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
- 可插 **CLI 外部工具**、**CLI 编码 agent**（`kind: acpx`，v3.2：codex/kimi/claude 等作为 R4 审查专家，只读沙箱）
  和 **remote 远端 agent**（a2a/mcp 协议子集，v3.1 实装）。内置 **OCR**（alibaba/open-code-review）
  提供三条调用通道（v3.9.0）：**ocrshim MCP sidecar**（OCR 全权审查，同镜像双二进制）、
  **delegate 委托前置**（OCR 端零 LLM 的文件分诊+仓库规则注入 R4，审查循环仍归 argus，
  `ARGUS_OCR_BIN` 启用）、`kind: cli` 回退位（默认关闭）；
- 支持**方法论注入**（skillRefs）：把 SKILL.md 里的评分标准注入对应阶段的 prompt；
- **Reviewer 也能带工具（v3.3）**：通用审查 agent 可选挂载 R3 同款工具表（读文件/读 diff/
  查知识库）走 ReAct 模式核实线索，失败自动落回单轮审查——工具链故障不影响出结果；
- **确定性路由与合并**：按 paths/labels/事件路由插件，指纹去重（不同专家发现同一问题只报一次）、
  冲突取高severity、token 预算吃紧时 heavy 插件先裁（锁定插件豁免）。路由/裁决/合并不走 LLM，可解释、可复现。
- **交叉审查编队（v3.3）**：plugins.yaml `squads:` 按 paths/labels 触发，把指定成员（通常是
  多个 acpx 编码 agent）追加进本轮分派，Merger 对"仅一方报告"的意见标注交叉分歧——高危路径
  多模型互查；
- **编队两种协作模式（v3.4）**：默认**互盲独立**（成员并发盲审，分歧由 Merger 事后标注"另一专家
  未复现"）；`mode: consensus` 切换为**黑板共识**（agentkit/blackboard）——成员按轮互看共享黑板、
  只补充他人未覆盖的新问题，一整轮无人补充即共识收敛（上限 2 轮），适合"要收敛结论"而非"要分歧
  信号"的高危变更；
- **报告反思闸门（v3.4，可选）**：合并去重后由终审编辑（agentkit/reflection 批判→修订收敛循环）
  按误报特征 Rubric 复核 findings——剔除幻觉引用/纯风格/无证据定位条目、修正严重级别（只删改不新增，
  默认关闭，`ARGUS_REFLECTION_ROUNDS` 开启）。
- **best-of-N 同质采样（v3.10.0，高危路径 opt-in）**：插件声明 `reviewN: 3` 即对同一审查者并发
  采样 3 次，复现 ≥2 的发现确定性升级严重度并标注「采样复现 k/n」——指纹匹配投票，不经任何模型，
  与「裁决不走 LLM」铁律一致；单次出现的 low 意见标注「单次采样」。成本 ×N，`/usage` 可见。
- **审查域评测集（v3.10.0）**：`eval/golden/` 48 条 golden PR 夹具（injection/partial/scope-creep/
  unverifiable/merge/guard/repo-config/best-of-n 等场景）双模式回归——`make eval` 打桩固化编排语义；
  `make eval-real`
  换真实模型测 verdict 命中率（模型 A/B 固定 harness，见 eval/EXPERIMENTS.md）。真实模式期望
  集合按「从严合法性」逐夹具建模（v3.16.0）：注入类夹具 approve 永不计通过，rc 期望精确比对。
- **评测指标面（v3.14.0）**：eval-real 每轮落盘 `eval/results/latest.json`，服务侧周期加载后在
  `/metrics` 暴露 `argus_eval_verdict_match_ratio{model,route}` / `argus_eval_cases_total` /
  `argus_eval_cases_passed` / `argus_eval_results_timestamp_seconds`——审查质量趋势与运行时指标
  同面抓取（`ARGUS_EVAL_RESULTS_PATH` 改路径；文件不存在=该部署未跑过评测，静默跳过）。CI 侧
  `golden-eval` job 必跑打桩回归，`eval-real` workflow 定时跑真实模型并归档结果制品。
- **Q&A 长会话记忆（v3.12.0）**：@argus 追问的 verbatim 上下文保持 6 轮，更早轮次由 QA 模型
  滚动压缩成摘要注入（失败退化为纯 FIFO）——第 8 轮仍能引用第 1 轮结论；
- **任务断点续审（v3.12.0）**：R1/R2 阶段产物成功即持久化，同任务失败重试跳过已成功阶段
  （只在任务内生效，新任务恒全量重跑，staleness 语义不受影响）。
- **数据出口（v3.13.0）**：`GET /reports/health`（Bearer）按需拉取学习环飞轮状态；
  完备性裁判结构化产出进 `GET /tasks/:id` 的 result_json（副驾类消费方免解析 markdown）；
  `go run ./cmd/exportdata` 把 suppress/regret/pattern 原料导出 JSONL（审查域微调的训练集管道）。

### 3. 确定性裁决 + 双层回帖

按序短路的真值表（`R5.1`–`R5.5`，见下文）给出结论，然后：

- **行内评论**：审查意见钉到具体代码行，随 CreateReview 事件附上（每轮独立）；带修复建议的意见渲染为 ```suggestion 围栏（平台端"一键应用"）；
- **仓库自定义审查指引**：仓库根放 `.argus/review-guidelines.md`（默认分支），内容注入内置审查插件的 prompt，调整审查重点（缺失时静默降级）；
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

### 5.1 仓库级配置面（v3.15.0，对标 .coderabbit.yaml）

仓库默认分支放一份 `.reviewagents.yaml`（只能"选"不能"增"的信任边界不变），除既有
插件 use/disable/routing 外，新增 `review` 段控制评审行为：

```yaml
review:
  exclude_paths: ["**/generated/**", "proto/**"]  # 仓库级路径排除（glob）
  include_paths: []                               # 非空 = 白名单模式
  min_severity: medium        # 低于阈值的意见不进报告与裁决（low|medium|high）
  max_comments: 20            # 行内评论上限（0=不限，超出保留严重度 top N 进总结）
  extra_instructions: "关注错误包装与并发边界"   # 附加审查指令（与 guidelines.md 合并注入）
describe:
  labels:                     # @argus describe 的建议标签词表（可省略）
    - {name: bug, description: 缺陷修复}
```

### 5.2 确定性 SAST 前置（v3.15.0，默认关）

配置 `ARGUS_SAST_GITLEAKS_BIN` / `ARGUS_SAST_SEMGREP_BIN`（+
`ARGUS_SAST_SEMGREP_CONFIG`，`ARGUS_SAST_TIMEOUT` 缺省 120s）后，审查会在工作副本上
先跑 gitleaks/semgrep，findings **零 LLM** 直进合并——密钥泄漏等确定性结论可直接驱动
一票否决。供应链纪律与 cli 插件一致：二进制路径只来自服务级 env，仓库配置不可启用；
gitleaks 全程 `--redact` 且密钥原文绝不进意见。

### 5.3 Go 符号上下文（v3.15.0，`ARGUS_CODEGRAPH` 默认开）

变更含 Go 文件且工作副本可用时，审查 agent 额外获得 `get_symbol`（定义/签名定位）与
`get_references`（跨文件调用方/使用点）两个工具，并自动注入「本次变更触达的符号」入口
清单（纯 go/parser 确定性索引，零 LLM；非 Go 变更自动跳过）。

### 5.4 testgen 沙箱验证（v3.22.0，默认关）

`ARGUS_TESTGEN_VERIFY_CMD='docker run --rm -v {dir}:/w -w /w golang:1.24 go test ./...'` 配置后，
@argus testgen 的产物会写入工作副本（只写 _test.go、不覆盖既有文件、失败即清理）并经你声明的
隔离命令执行，回帖附确定性「沙箱验证结果」。隔离责任（网络/文件系统/资源）在包装命令侧——
argus 自身永不执行待审代码。`ARGUS_TESTGEN_VERIFY_TIMEOUT` 缺省 5m。

### 5.5 多语言符号上下文（v3.22.0）

codegraph 在 Go 之外新增 Python/JS/TS 词法函数表（`get_poly_symbol`）与 Go 调用形态索引
（`get_callers`）——词法级（非类型解析），工具描述已声明核实纪律。零新增依赖，`ARGUS_CODEGRAPH` 开关沿用。

### 5.5.1 全文检索工具（v3.84.0，`ARGUS_CODESEARCH` 默认开）

审查 agent（R3/R4/reviewer）与 Q&A 引擎（v3.91.0）额外获得 `search_code`：工作副本上的
受控正则搜索（确定性、零 LLM），覆盖符号索引到不了的词法线索——字符串/配置/非 Go 代码。
命中数、单文件大小、输出长度三重封顶，`.git`/vendor/node_modules/testdata 等目录自动排除；
命中不等于结论，工具描述已声明"引用前须 get_file 核实"。Q&A 冷克隆有 30s 预算，
超时本轮静默收窄工具面（不拖首答）。

### 5.6 修复命令（v3.85.0，`ARGUS_FIX_AGENT` 启用）

`@argus fix fp:短码`：按指纹取回入库 finding → 工作副本本地克隆出独立修复沙箱
（源目录零写入）→ 编码 agent（claude/codex/gemini 族，workspace-write 沙箱档；
无沙箱旗标的 agent fail-closed 拒绝）出 patch → `ARGUS_FIX_VERIFY_CMD` 沙箱验证
（复用 testgen 的包装命令纪律：argus 自身永不执行待审代码）→ 回帖 diff。
修复成立与否由确定性验证判定，不由 agent 自评；**不自动 push**（写分支仍是
v3.74.1 `ARGUS_FIX_PUSH=1` 管线环的职权）。服务启用后报告意见行附一键修复入口
（`@argus fix fp:xxxx`）。`ARGUS_FIX_TIMEOUT` 缺省 10m。

### 5.7 副驾参考客户端（v3.22.0）

`cmd/argus-copilot`：MCP 参考客户端（stages/findings/suggest/ask/review/fix/poll 七命令；`review -async` 异步提交预审任务 + `poll` 提交方轮询构成提交前闭环），IDE/agent 客户端接入样例见 [docs/ide-mcp.md](docs/ide-mcp.md)，AI agent 技能文件见 [skills/argus-mcp/SKILL.md](skills/argus-mcp/SKILL.md) 与 [skills/argus-precommit/SKILL.md](skills/argus-precommit/SKILL.md)；脚本
集成的参考实现：

```bash
go run ./cmd/argus-copilot -url http://argus:8899/mcp -token $TOKEN findings -task <id>
```

### 6. 四种接入方式，同一个引擎

| 方式 | 场景 | 说明 |
|---|---|---|
| **Webhook** | 平台可达 Argus | Gitea/Forgejo/GitHub/GitLab/Gitee 签名校验，delivery 幂等，`scripts/setup-webhook.sh` 一键注册 |
| **轮询兜底** | NAT/VPN 下 webhook 不可达 | `ARGUS_POLL_REPOS` 定时发现新 PR 与 head 变化，与 webhook 任务同语义（试点实测入站零包时靠它） |
| **TaskAPI** | 手动/脚本/CI 触发 | `POST /tasks` 对任意已有 PR 发起审查，`GET /tasks/:id` 取报告 |
| **MCP 工具** | 任意 MCP 客户端 | `/mcp`（streamable http + Bearer）暴露 `review_pr` 等十三工具（清单见 §3），可同步等待终态；AI agent 直驱装 [skills/argus-mcp/SKILL.md](skills/argus-mcp/SKILL.md) |

### 7. PR 评论交互

在 PR 评论中 `@argus ask <问题>`（评论需以 `@机器人用户名` 开头），Argus 会根据 PR 上下文用 LLM 回答。例如：
- `@argus ask 这个改动会影响性能吗？`
- `@argus ask 为什么用 sync.Mutex 而不是 channel？`
- `@argus suppress fp:ab12cd34` —— 误报反馈（抑制 + 知识回流）
- `@argus restore fp:ab12cd34` —— 误抑制恢复（suppress 反向：误杀的意见重新可见，不回流知识）
- `@argus fix fp:ab12cd34` —— 一键修复（v3.85.0，`ARGUS_FIX_AGENT` 启用）：编码 agent 在隔离沙箱出 patch，沙箱验证后回帖 diff（不自动 push）
- `@argus describe [补充要求]` —— 生成 PR 变更说明 + 建议标签（v3.15.0，回帖评论不写回 PR body）
- `@argus testgen [目标路径]` —— 按 diff 生成单元测试骨架（v3.15.0；未经编译验证，需人工核对）

三平台评论事件均接入（v3.5.4 补齐 GitLab）：GitHub `issue_comment`、GitLab `note`
（仓库 webhook 需勾选 Note events）、Gitea/Forgejo 评论兜底；`setup-webhook.sh` 已含
`issue_comment` 事件。GitLab 系统注释（改指派等自动生成）与非 MR 评论自动忽略。

**评论意图路由（v3.4，`ARGUS_COMMENT_ROUTER=1` 开启）**：`@argus` 后的自由文本先分类再分派——
- 要求重新审查（"请重新审查"、"再审一遍"）→ 自动建全量复审任务并回执任务号，作者推完修复
  不用再等 webhook；
- 其余一切（含低置信/分类失败）→ Q&A 问答。短命令关键词零分类调用直达，歧义文本才走一次
  快模型分类（`ARGUS_ROUTER_MODEL` 可覆盖）。

**Q&A 本体（v3.3）**：`ask` 不是对着摘要猜——它是一个 ReAct agent（get_diff / get_file /
search_knowledge，toolprior 三层约束 + 数据区围栏），回答可引用 file:line 证据；同一 PR 的
追问带历史语境（一 PR 一会话，6 轮封顶 FIFO + 24h 过期重置）。ReAct 失败自动落回单轮直调。
IDE/其他 agent 可经 MCP `ask_argus` 工具直接提问或取某 PR 最新审查结论。

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
v3.8.0 起报告短码与入库指纹一致（修复标注条目无法抑制的问题）；v3.7.0 之前报告中的旧短码不可解析。

**知识飞轮（v3.3，可选）**：配置 Milvus（`ARGUS_MILVUS_ADDR` + Embedding 三件套）后，
每次人工 suppress 的误报结论会向量化回流知识库；R3 的 `search_knowledge` 检索命中历史
误报即带抑制语境——同型误报率随使用下降。不配 Milvus 自动回退本地词元检索（knowledge/ 目录）。

### 10. 生产级健壮性（v3.0.1 加固）

- **幂等与去重**：同 delivery 重发不重跑；同 PR 同 head SHA 的 in-flight 任务直接去重（事务保证）；
  评审事件带 task_id 锚点，崩溃重跑不重复发评审；
- **staleness 门控**：连续两次 push、慢的那轮后完成时**不会覆盖新 SHA 的报告**；
- **优雅停机与心跳恢复**：SIGTERM 后停领新任务，在途审查给 60s 自然完成，超时才硬取消；
  running 任务带实例心跳（20s），心跳死亡 >90s 才被重置回 pending（多副本共库时滚动重启
  不误伤其他实例的在途任务）——重启不烧双倍 token；
- **任务保留策略**：`ARGUS_TASK_RETENTION_DAYS`（v3.9.2 起默认 30；超期终态任务启动时+每日清理，显式设 0 关闭并告警）；
- **成本记账（v3.3）**：obsx 回收全部模型调用的真实 usage（Generate 直连与 ReAct RawModel
  两条路径都覆盖），任务级 + 全局 CostTracker；报告元信息行显示"成本 ≈ $X"
  （费率 `ARGUS_LLM_COST_*` 配置），`GET /usage/monthly` 按仓库出月度 token 用量；
- **多副本横向扩展（v3.3）**：切 MySQL 后可多活副本——poller 走 `leader_leases` 租约选主
  （失联 ≤ttl 自动换主、优雅停机主动让位），工作副本沙箱按实例隔离防误删；
  SQLite 保持单写（replicas=1 + Recreate）；
- **可观测**：`/metrics` 暴露 `argus_tasks_total/pending/running`、`argus_llm_tokens_total`、
  插件耗时/失败、阶段耗时、golden 评测命中率（`argus_eval_verdict_match_ratio`，v3.14.0）；
  `GET /events` SSE 任务进度流；octopus IM 推送（`ARGUS_OCTOPUS_*`）；
  `ARGUS_DEBUG=1` 可留存 R1 spec/R3 原始输出到任务记录；
- **平台降级**：平台不可达时启动只告警降级（`/readyz` 可见），不会 crash loop；
- **LLM 降级链**：主模型故障自动切备用模型（Resilient + FallbackChain，v3.0.3 引入），
  分阶段模型路由（P6）可按 R1/R2/R3/R3.5/Reviewer/QA/Router 各配各的模型与窗口；
- 凭据脱敏（克隆 URL 里的 token）、日志静默（预期内 not-found 不刷屏）；
- **结果外推 sink（v3.9.0，可选）**：审查完成后把结构化结果（verdict + findings +
  统计）push 给外部承接端（review-service 的「Argus 审查」页），`ARGUS_SINK_URL`
  非空即启用，失败只告警不阻断；其前端「误报抑制」按钮可经代理回流到 argus 的
  `/findings/suppress`，形成两系统反馈闭环（见 review-service-integration.md）。

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
  → R4 并发委派插件(builtin/cli/acpx/remote；编队可互盲或共识)
  → [R3.5 报告反思，可选] → R5 真值表裁决
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
export ARGUS_GITEA_TOKEN=<token>              # GitHub/GitLab/Gitee 同构：ARGUS_GITHUB_* / ARGUS_GITLAB_* / ARGUS_GITEE_*
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
| `review_diff` | 本地 diff 单轮预审（不建任务不落库不回帖；≤120KB；服务端未配 LLM 时报错，v3.74.1）|
| `review_diff_async` | diff 异步预审任务：秒回 task_id，报告落库供 `get_report` 轮询（≤48KB；提交方 hook 闭环，copilot `review -async` + `poll`，v3.123.0）|
| `list_agents` | 插件清单；三参全传附带仓库配置视图 |
| `get_report` | 按 taskId 取状态/verdict/完整报告 |
| `list_tasks` | 任务清单枚举（status/limit 可选，倒序摘要面）——巡检/找 task_id 入口（v3.121.0）|
| `ask_argus` | Q&A：无 question 返回某 PR 最新审查结论（verdict/findings/摘录）；带 question 则基于报告+仓库上下文追问（ReAct，v3.3）|
| `get_task_stages` | 任务阶段时间线（R1→R5 完成状态，v3.15.0）——「审得全不全」可观测 |
| `list_findings` | 结构化意见直出（fp 短码/suggested_fix，免解析 markdown，v3.15.0）|
| `get_suggestion` | 按指纹短码取回单条修复建议（一键回写由客户端执行，v3.15.0）|
| `search_code` | PR 工作副本受控全文检索（R3/R4 同款纪律；ARGUS_CODESEARCH 门控，v3.121.0）|
| `fix_finding` | 按 PR+短码同步执行沙箱修复（agent 出 patch+确定性验证，回 diff；绝不 push；工具常驻注册，服务未配 ARGUS_FIX_AGENT 时调用报错，v3.89.0）|

> AI agent 直接驱动的完整工作流（何时调哪个工具/失败降级/安全边界）见 agent skill [skills/argus-mcp/SKILL.md](skills/argus-mcp/SKILL.md)，参数级参考见其 `references/tools.md`。

### 4. API 一览

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `GET /healthz` `GET /readyz` | 无 | 健康/就绪（平台降级在 readyz 可见）|
| `GET /metrics` | 无 | Prometheus 指标（argus_* 前缀）|
| `POST /webhook/{gitea\|github\|gitlab}` | HMAC 签名 | 平台事件入口，delivery 幂等 |
| `POST /tasks` / `GET /tasks` / `GET /tasks/:id` | 🔒 Bearer | 发起 / 列表（?status=&limit=）/ 查询（含 debug 字段）|
| `GET /reports/:ref` | 🔒 Bearer | 按 head SHA 查最近一次成功报告（CI 门禁联动）|
| `GET /stats/overview` | 🔒 Bearer(read) | 按仓聚合任务量/verdict 分布/抑制计数（?days=30&platform=&owner=&repo=，v3.88.0 仪表盘数据面）|
| `GET /usage/monthly` | 🔒 Bearer | 本月各仓库 token 用量 |
| `GET /events` | 🔒 Bearer | SSE 任务进度流（阶段事件实时推送）|
| `POST /findings/suppress` | 🔒 Bearer | 误报抑制（fingerprint 短码或 file+原文）|
| `GET /agents?platform&owner&repo` | 🔒 Bearer | 插件清单 + .reviewagents.yaml 生效视图 + 信任边界告警 |
| `POST /mcp` | 🔒 Bearer | MCP streamable http |

### 5. 常用环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ARGUS_ADDR` | `:8899` | 监听地址 |
| `ARGUS_DB_DSN` | `argus.db` | SQLite 文件；切 MySQL DSN 即集群化（多副本必须）|
| `ARGUS_APITOKEN` | 空 | 🔒 路由 Bearer（等价 admin 档）；与 ARGUS_API_TOKENS 全空则 fail-closed 503 |
| `ARGUS_API_TOKENS` | 空 | 🔒 多 token 分级（v3.87.0）：`tok1:admin,tok2:ci,tok3:read`——read 只读 / ci 可建任务与 MCP / admin 全权（含误报抑制） |
| `ARGUS_BOT_USERNAME` | - | bot 账号名（自审保护；生产建议独立账号）|
| `ARGUS_MAX_CONCURRENT_TASKS` | 2 | worker 数，按 LLM 配额调 |
| `ARGUS_PLUGIN_MAX_CONCURRENT` | 2 | 单任务内插件 fan-out 并发上限（防 API burst 429） |
| `ARGUS_TASK_TOKEN_BUDGET` | 200k | 单任务 token 预算，heavy 插件先裁 |
| `ARGUS_MAX_DIFF_CHARS` / `ARGUS_MODEL_CONTEXT_TOKENS` | - | diff 截断 / 上下文预算 |
| `ARGUS_SKIP_DRAFT` | true | 跳过 Draft PR |
| `ARGUS_TASK_RETENTION_DAYS` | 30 | 终态任务保留天数（0=永久；tasks 产物每任务 MB 级，长期运行建议保留默认） |
| `ARGUS_POLL_INTERVAL` / `ARGUS_POLL_REPOS` | - | 轮询兜底（webhook 不可达时开）|
| `ARGUS_PLUGINS` | plugins.yaml | 插件声明文件 |
| `ARGUS_SKILLS_DIR` / `ARGUS_WORK_ROOT` / `ARGUS_REPORT_META` / `ARGUS_DEBUG` | - | 方法论目录 / 沙箱根 / 报告元信息 / 调试留存 |
| `ARGUS_{GITEA,GITHUB,GITLAB}_{URL,TOKEN,SECRET,SKIP_TLS}` | - | 平台凭证；**SECRET 必配**——空 secret 时该平台 webhook fail-closed 503（SDK 空 secret 会跳过签名校验）|
| `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` | - | LLM（OpenAI 兼容协议）|
| `ARGUS_MILVUS_ADDR` / `ARGUS_MILVUS_COLLECTION` | - | Milvus 向量知识库（需配 `ARGUS_EMBEDDING_BASE_URL/API_KEY/MODEL/DIM`；不配回退本地词元检索，知识飞轮随之关闭）|
| `ARGUS_OCTOPUS_URL` / `_CHANNEL_ID` / `_USER` / `_TOKEN` | - | octopus IM 推送（审查完成通知）|
| `ARGUS_LLM_COST_PROMPT_PER_1K` / `ARGUS_LLM_COST_COMPLETION_PER_1K` | 0 | 成本费率（$/1k token），报告元信息"成本 ≈ $X" |
| `ARGUS_LLM_RATE_LIMIT` | 0（禁用）| LLM 请求速率限制（token bucket，单位 req/s）|
| `ARGUS_LLM_RATE_BURST` | =MaxConcurrentTasks | 速率限制器突发容量 |
| `ARGUS_SDK_TIMEOUT` | 120s | 单次平台 SDK 调用兜底超时（入参 ctx 无 deadline 时生效；负值关闭）——弱网/VPN 下防连接停滞挂死 |
| `ARGUS_MODEL_{R1,R2,R3,R35,ROUTER}` / `ARGUS_REVIEWER_MODEL` / `ARGUS_QA_MODEL` | - | 分阶段模型覆盖（P6 扩展）：大窗口吃 R1 长输入、快模型跑 R2/路由、强模型保 R3/R35 质量；`_CONTEXT_TOKENS` 后缀配各阶段窗口 |
| `ARGUS_REFLECTION_ROUNDS` | 0（关闭）| R3.5 报告反思轮数（1~3）：批判→修订收敛，剔除误报嫌疑 findings |
| `ARGUS_COMMENT_ROUTER` | false | 评论意图路由：@argus 自由文本分类分派（review=重审任务 / ask=问答）|
| `ARGUS_ROUTER_MIN_CONFIDENCE` | 0.6 | 意图分类置信度下限，低于则回落 ask |
| `ARGUS_WEBHOOK_MAX_BODY` | 1048576（1MB）| Webhook 请求体最大字节数 |
| `ARGUS_GITHUB_APP_ID` | 空 | GitHub App ID（替代 PAT 的另一种认证方式）|
| `ARGUS_GITHUB_APP_PRIVATE_KEY` | 空 | GitHub App 私钥（PEM 格式）|
| `ARGUS_GITHUB_APP_INSTALLATION_ID` | 空 | GitHub App installation ID |
| `ARGUS_SINK_URL` / `ARGUS_SINK_TOKEN` | 空 | 结果上报 sink（v3.9.0）：审查完成后 push 给 review-service；URL 非空即启用，失败只告警不阻断 |
| `ARGUS_OCR_BIN` / `ARGUS_OCR_TIMEOUT` | 空 / 90s | OCR delegate 委托前置（v3.9.0）：OCR 文件分诊+仓库规则注入 R4，审查循环仍归 argus |
| `ARGUS_OCR_HISTORY_DIR` | 空 | OCR 原始产物留档目录（delegate/ocrshim 双通道） |
| `ARGUS_REACT_MAX_ITERATIONS` | 12 | builtin ReAct 迭代上限 |
| `ARGUS_CODESEARCH` | true | search_code 全文检索工具开关（v3.84.0，=0 关闭）|
| `ARGUS_RELATED_REPOS` | false | 跨仓契约快照总门（v3.90.0）：允许 .reviewagents.yaml related 段拉取关联仓契约文件注入审查（扩大数据访问面，部署方显式授权）|
| `ARGUS_REFLECTION_EVIDENCE` | off | R3.5 反思证据门模式（strict = 剔除证据不足条目）|
| `ARGUS_REGRET_LEARNING` | false | 模式级 regret 学习环（低置信标注 SoftFloor / 抑制 HardFloor；`ARGUS_REGRET_MIN_SAMPLES/SOFT_FLOOR/HARD_FLOOR/INJECT_FLOOR/INJECT_TOP_N` 子项见 config.go）|
| `ARGUS_DREAM_INTERVAL` | 0（关闭） | 长期记忆整合周期（internal/dream）|
| `ARGUS_HEALTH_REPORT_INTERVAL` | 0（关闭） | 审查健康报告周期（v3.11.0，建议 168h 周报）：聚合学习环状态经 IM 推送——噪声处置建议（人工门控）+ guidelines 增量建议（只生成建议文本，绝不自动改仓库文件）|
| `ARGUS_COMPLETENESS_POLICY` | annotate | 完备性裁判策略（v3.11.0）：annotate=失败项只注记报告「完备性检查」段；demote=任一检查不过时 APPROVE→COMMENT（R5.C）|
| `ARGUS_COMPLETENESS_RETRY` | false | 受限补审（v3.11.0）：执行失败的插件在剩余预算内重试一次，成功替换原结果（不叠加 findings）|
| `ARGUS_COMMENT_TIMEOUT` / `ARGUS_COMMENT_CONCURRENCY` | 10m / 2 | 评论回帖整体超时 / 并发数 |
| `ARGUS_TASK_MAX_RETRIES` | 2 | failed 任务自动重试上限（指数退避）|
| `ARGUS_WORKER_GRACE` | 60s | worker 优雅停机等待在途任务时长 |
| `ARGUS_METRICS_AUTH` | false | /metrics 鉴权开关（公网/办公网直露部署建议开启）|
| `ARGUS_EVAL_RESULTS_PATH` | eval/results/latest.json | golden 评测结果文件路径（eval-real 落盘 → /metrics 的 argus_eval_* 指标；不存在则静默跳过）|
| `ARGUS_REPO_MONTHLY_TOKEN_BUDGET` | 0（不限） | 仓库月度 token 预算 |

### 6. 仓库侧配置（.reviewagents.yaml，放业务仓库默认分支）

```yaml
use: [requirement-compliance, ocr]      # 白名单（锁定插件不受影响，永远参与）
disable: [correctness]
routing:
  - {paths: ["web/**", "*.vue"], use: [frontend-visual]}
  - {labels: ["ui"], use: [frontend-visual]}
related:                                # 跨仓契约快照（v3.90.0，服务级 ARGUS_RELATED_REPOS=1 才生效）
  - repo: acme/api-contracts            # 同平台 owner/name；ref 缺省借本仓默认分支
    paths: ["openapi.yaml", "proto/user.proto"]   # 显式路径（无 glob，≤3 仓 × ≤8 文件）
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
SQLite 为单写模型（replicas=1 + Recreate），横向扩副本切 MySQL + 租约选主（v3.3 已支持，
容量公式与验证清单见 **[deploy/k8s/README-scaling.md](deploy/k8s/README-scaling.md)**）；
磁盘监控 `/data`（SQLite + 工作副本沙箱，后者 1h TTL 兜底回收）。

---

## 六、插件声明（plugins.yaml 要点）

```yaml
agents:
  - name: requirement-compliance   # R1→R5 整条 workflow 包成的插件
    kind: builtin                  # builtin | cli | remote | acpx
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
- `kind: acpx`（v3.2）：`agent: codex|kimi|mimo|claude|...` —— CLI 编码 agent 作为 R4 审查专家
  （agentkit/acpx 驱动，只读沙箱 + findings JSON 契约；CLI 未安装时插件自动停用不影响启动）；
- `triggers: {events: [cr.opened]}`：可按事件收窄（SDK 归一化词表 cr.opened/synchronize/reopened）。

编队声明（v3.3，`squads:` 段；v3.4 支持共识模式）：

```yaml
squads:
  - name: security-double-check
    paths: ["**/auth/**", "**/*.sql"]   # paths/labels 任一命中即触发
    labels: [security]
    members: [claude, kimi]             # 引用上面声明的插件（通常 acpx）
  - name: core-consensus
    mode: consensus                     # 黑板共识：互看补充、收敛即止（默认互盲独立）
    paths: ["internal/**"]
    members: [claude, codex]
```

---

## 七、测试

```bash
make test    # 单元测试（真值表/反注入/指纹/渲染/diffprep/存储，不联网）
make e2e    # E2E 双打桩场景集（mock OpenAI + fake Gitea/GitLab + mock OCR，不联网）
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

**生产运行**：已在内网 GitLab（git.example.com，自签证书）常驻运行——4 仓库 webhook
接入、OCR 等 4 插件启用、MaxConcurrent=2。仓库 HEAD 已演进至 v3.13.x（v3.10 评测集+best-of-N、
v3.11 完备性裁判+健康报告、v3.12 长会话记忆+断点续审、v3.13 数据出口+导出管道），生产升级部署待验证。

---

## 九、目录导览

```
idl/argus.thrift               接口唯一事实来源（hz 生成路由/DTO）
biz/                           hz 生成壳：handler 薄实现调 internal/service
internal/
  config/                      环境变量配置（含分阶段模型/成本费率/Milvus）
  bootstrap/                   装配：Registry 构建 + 依赖接线
  plugin/                      L4：契约 + severity 词表 + 指纹
    ├ registry.go              plugins.yaml → 注册表；仓库配置信任边界
    ├ dispatcher.go            确定性路由 + fan-out + 超时 + 熔断(3败/5min) + 预算裁剪
    ├ merger.go                指纹去重 + 冲突取高 + 误报抑制 + 交叉分歧标注
    ├ squad_consensus.go       编队黑板共识（v3.4，agentkit/blackboard 驱动）
    ├ adapter_builtin/         requirement-compliance(workflow 外壳) + 通用审查 agent(可选 ReAct)
    ├ adapter_cli/             OCR CLI 适配器（进程组 exec；容错 JSON 解析）
    ├ adapter_acpx/            CLI 编码 agent 适配器（v3.2，agentkit/acpx 只读沙箱）
    └ adapter_remote/          a2a/mcp 远端插件（v3.1 实装协议子集）
  workflow/requirement/        R1 需求解析 / R2 变更理解 / R3 完成度 / R3.5 反思(可选, v3.4)
                               / R4 委派 / R5 真值表
  context/                     diffprep(diff 清洗+RawDiff 回填) + sdkctx + workcopy(浅克隆沙箱, 按实例隔离)
  storage/                     GORM：tasks(幂等/去重/心跳/staleness) + finding_prints
                               + review_targets + qa_sessions(v3.3) + leader_leases(v3.3)
  service/                     门面 API + 评论意图路由(v3.4) + Q&A ReAct(v3.3)
  poster/                      行内评论 + 总结评论三级降级 + staleness 门控
  poller/                      轮询兜底任务源（租约选主门控, v3.3）
  reportview/                  报告渲染（上文示例的出处）
  channel/mcpserver/           Argus as MCP Server（/mcp 十工具）
  runner/ worker/              任务执行编排 + DB 即队列的 worker pool
  toolpool/ obs/ notify/       MCP 工具池 / 指标+成本记账 / octopus IM 推送
  progress/                    SSE 任务进度事件流
test/e2e/                      双打桩 E2E 场景集 + 真实 LLM 集成测试
deploy/                        docker-compose + k8s kustomize 双形态（+ 多副本指南）
scripts/                       setup-webhook.sh（一键注册）+ install-ocr.sh
skills/                        方法论包（ocr-grading 等，ARGUS_SKILLS_DIR 加载）
                               + agent 技能（argus-mcp 直驱审查 / argus-precommit 提交前自查）
knowledge/                     本地知识库（markdown，R3 search_knowledge 检索）
```

> LLM 客户端（重试/JSON 反馈重试/token 记账/降级链/StageRouter 分阶段路由）与
> 知识子系统（rag/skill）已收敛进共享库 **agentkit**（v3.0.3 起去重，argus 只做领域接线）；
> Milvus 模式下 knowledge/ 目录内容会在启动时索引进向量库。

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

## 十、当前边界

**已落地**（v3.1–v3.4，细节见 [CHANGELOG.md](CHANGELOG.md)）：remote 插件协议子集（a2a/mcp）、
cli delegate 模式、MCP Server 五工具（含 ask_argus）、SSE 进度流、IM 推送（octopus）、
1M 上下文窗口单一基准、GitHub App 认证、acpx 编码 agent 插件、成本记账（任务级+月度仓库用量）、
多副本横向扩展（租约选主+实例隔离）、Q&A ReAct + 多轮会话、分阶段模型路由（StageRouter）、
Milvus 知识飞轮、报告反思闸门、评论意图路由、编队黑板共识。

**有意不做**（架构纪律拍板）：
- Plan-and-Execute：R1→R5 已是固化确定性编排，叠 P&E 是杀鸡用牛刀（v3.4 拍板）；
- 反思写 suppress：持久抑制只来自人工反馈，防自动化误杀指纹库；
- 路由/裁决/合并走 LLM：确定性优先的原则不变。

**剩余待办/已知边界**：
- 生产升级部署验证（生产现运行 v3.0.x，仓库 HEAD 已 v3.9.x）；
- 总结评论扫描兜底受 SDK 分页限制（评论被顶出第一页后走新建路径）；
- 编队共识上限 2 轮（比 agentkit 缺省 3 收紧）；反思阶段调用不计任务 token 预算
  （成本经 obsx/费率记账可见）；
- remote 插件仅实装协议子集（a2a/mcp 的审查调用面），完整 agent 间互操作待需求驱动。


## 十一、安全与贡献

漏洞报告与支持范围见 [SECURITY.md](SECURITY.md)；本地门禁与改动准则见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

*设计文档：[design.md](design.md) · 试点手册：[PILOT.md](PILOT.md) · 部署：[deploy/README.md](deploy/README.md) · 变更记录：[CHANGELOG.md](CHANGELOG.md)*
