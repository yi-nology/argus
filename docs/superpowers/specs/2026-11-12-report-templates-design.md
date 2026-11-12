# 报告契约模板系统（report templates）设计

日期：2026-11-12 · 状态：已批准（用户全权委托实施）· 目标版本：v6.10.0

## 1. 背景与目标

起因是 `obs/kylin-insights-agent` MR!187 上 `suoxiaocong` 的一篇分析师风格深度评审
（note_39868）：声明-证据对照表（✅⚠️❌➕❗ 五态）、风险链 R1-R7（🔴🟠🟡🟢，追到消费侧
反向验证）、代码坏味、测试缺口、待确认清单。该报告叠加在 argus 的 30 条结构化意见之上，
还纠正了 argus 一条 MEDIUM 定级——说明 argus 缺的是"综合层"。

目标：argus 报告输出支持**白名单契约模板**三枚——`standard`（现状不动）/ `analyst`
（例文同款深度报告）/ `minimal`（TL;DR+高危极简）。模板=「提示词契约片段 + 渲染分发」
单源定义，仓库只能在词表里**选**，不能自定义内容（防提示注入面）。

参考资产：suoxiaocong 的 `mr-review` skill（评审 playbook）中的检查表已提炼折入 analyst
契约（漏项清单/坏味词表/结构维度/输出纪律），原文档引用见 §4.5。

## 2. 非目标（v1 显式不做）

- 仓级自定义模板内容（`.argus/report-template.md` 类）——提示注入面，后续若要做得带围栏评审；
- 任务级 commentcmd 覆盖（`@argus review --template`）；
- repo_settings（admin 可信面）设模板默认——先 env + yaml 两级；
- 本 MR 已有人工评论去重、流水线状态进报告头、影响面六维语义表（C 类记账，独立能力另立项）；
- MCP/IDE 侧结构化消费面暴露 analyst 新字段（v1 仅报告渲染，result_json 顺手带但文档不宣）。

## 3. 总体架构

```
.reviewagents.yaml review.report_template ─┐
ARGUS_REPORT_TEMPLATE (服务默认) ──────────┤ resolveTemplate() → key
                                           │
        ┌──────────────────────────────────┤
        ▼                                  ▼
Task.Artifacts[report_template]      workflow.分析师综合段（仅 analyst）
        │                                  │（merge 后一次 LLM 调用）
        ▼                                  ▼
builtin Reviewer systemPrompt       Output.Analyst{Claims, Design,
（+chain 指令，仅 analyst）           TestGaps, OpenQuestions}
        │                                  │
        └───────────┬──────────────────────┘
                    ▼
        reportview.Report.Template → Render 按模板分发
                    ▼
        standard（现状）/ analyst（例文骨架）/ minimal（结论+高危）
```

核心决策（探勘期修正）：

1. **analyst 新段落是"全报告综合产物"，不是插件产物**——让 R4 每个专家插件各产一份
   claims/design 会互相冲突。因此由 workflow 在 merge 后跑一次**条件性「分析师综合段」**
   LLM 调用统一产出；R4 插件面仅加可选 `chain` 字段（证据链）指令。
2. **standard/minimal 提示词零改动**——minimal 与 standard 共用同一 FindingsWire 契约，
   只是渲染子集；analyst 的额外要求只出现在综合段提示词 + builtin chain 指令里。
3. **模板定义单源**——新包 `internal/reporttmpl`：每枚模板 `{Key, SynthesisPrompt, Render}`
   同文件定义，提示词侧（workflow）与渲染侧（reportview/poster）都从这里取，
   防"提示词要求 X、渲染器不认 X"的契约-渲染漂移。

## 4. 详细设计

### 4.1 模板注册表 `internal/reporttmpl`

```go
package reporttmpl // import "github.com/yi-nology/argus/internal/reporttmpl"

type Template struct {
    Key             string  // standard | analyst | minimal（词表唯一出处）
    SynthesisPrompt string  // 分析师综合段系统提示词（standard/minimal 为空）
}

func Lookup(key string) (Template, bool)
func Keys() []string                    // 有序词表（钉测试用）
func IsValid(key string) bool
```

`Render` 不挂注册表——渲染就是 `reportview.Render`，内部按 `Report.Template` 分发
（reportview 是既有渲染单源，不拆包）。注册表只管"选择面 + 综合段提示词"，
避免 reportview 反向依赖 reporttmpl 成环。

### 4.2 线格式扩展 `internal/plugin/findingswire.go`

`FindingsWire` 是三条路径（builtin ReAct/单轮、remote）同 schema 的单源，扩展为：

```go
type FindingsWire struct {
    Findings      []Finding   `json:"findings"`
    Notes         string      `json:"notes"`
    Claims        []Claim     `json:"claims,omitempty"`         // analyst 综合段产物
    Design        *Design     `json:"design,omitempty"`         // 触发式，nil=未命中/未启用
    TestGaps      []string    `json:"test_gaps,omitempty"`
    OpenQuestions []string    `json:"open_questions,omitempty"`
}

type Claim struct {
    Claim   string `json:"claim"`              // MR 描述/需求中的一条可验收声明
    Evidence string `json:"evidence"`          // 代码证据 file:line 或差距说明
    Status  string `json:"status"`             // 五词表
}

// ClaimStatus 五词表（参考点①：skill 步骤 5.1）
const (
    ClaimImplemented   = "implemented"   // ✅ 实现
    ClaimPartial       = "partial"       // ⚠️ 部分实现
    ClaimMissing       = "missing"       // ❌ 未实现
    ClaimUndeclared    = "undeclared"    // ➕ 超出声明（描述没提但改了对外语义）
    ClaimInconsistent  = "inconsistent"  // ❗ 口径不一致（与声明/既有模块规则不一致）
)

type Design struct {
    Triggered    bool       `json:"triggered"`     // 30 秒判定是否命中（命中才要求产出）
    ObjectGraph  []string   `json:"object_graph"`  // 创建者→持有者→写者→读者→生命周期
    Cohesion     []string   `json:"cohesion"`      // 内聚问题（带 path:line 证据）
    Coupling     []string   `json:"coupling"`      // 耦合问题（带修法）
    Migration    string     `json:"migration"`     // 目标骨架+P0/P1+"是否阻塞合入"+"如果只做一件事"
}

```

`domain.Finding` 加一个可选字段：

```go
// Chain 证据链叙述（analyst 模板下由模型补充：改动侧/配套改动/消费侧决定性证据/后果）。
// 渲染属性不进指纹（指纹身份=File+原始 comment+Line，置信度/chain 同类教训）。
Chain string `json:"chain,omitempty"`
```

**解析纪律**：`DecodeFindingsJSON` 已走 jsonrepair 宽容解析；新字段全为 optional，
旧模型输出/旧插件（remote）不带新字段时零影响。claims/test_gaps/open_questions 长度
钳制（各 ≤20 条、单条 ≤500 runes）在综合段解码处做（敌对输入对待：模型输出也是数据）。

### 4.3 配置面

```go
// internal/config：ReportTemplate string（ARGUS_REPORT_TEMPLATE，缺省 "" → standard）
// internal/plugin ReviewSettings 加：
ReportTemplate string `yaml:"report_template"` // sanitize 词表校验，非法值整份拒载（现成模式）
```

`sanitizeReviewSettings` 增词表校验：`"" | standard | analyst | minimal`，非法值返回
error（整份 .reviewagents.yaml 降级为无配置+告警，不炸任务——同 min_severity 纪律）。

**解析优先级**：`review.report_template`（按仓）> `ARGUS_REPORT_TEMPLATE`（服务）> standard。

**三向钉测试**（platformVocab 先例）：reporttmpl.Keys() × sanitize 词表 × config 缺省
解析，三方相等测试钉死。

### 4.4 builtin 适配器 chain 指令（R4 面）

模板经 `Task.Artifacts[plugin.ArtifactReportTemplate]`（新增常量，值=模板键）传到
适配器 Invoke 时读取。`Reviewer.systemPrompt()` 每次 run 重建，按 Artifacts 键条件追加：

```
6.（仅深度报告模式）如该问题涉及对外语义/上报口径/协议变更，在 findings JSON 该条加
  "chain" 字段：改动侧是什么、消费侧哪里依赖旧语义（file:line）、不改的后果。
```

standard/minimal 不加（Artifacts 键缺失或值为 standard/minimal 时提示词与现状逐字节一致——
用现有提示词 golden 测试钉住）。

### 4.5 分析师综合段（workflow 新条件阶段）

位置：`mergeAndReflect` 之后、Output 组装处（`workflow.go` Run 尾部）。

```go
// Output 扩展（requirement.Output + plugin.WorkflowOutput 同改，别名同步）
Analyst *plugin.AnalystSections // nil = 非 analyst 模板或综合段失败降级

type AnalystSections struct {
    Claims        []plugin.Claim
    Design        *plugin.Design
    TestGaps      []string
    OpenQuestions []string
}
```

触发条件：`resolveTemplate(task)` == "analyst" 且合并后 findings 非空或 diff 非空。
模型：ReflectModel → R3ChatModel → LLM.RawModel() 逐级回退（与反思同链）；调用计入
任务预算（budget 非 nil 时，同 R3.5 纪律）。

提示词（reporttmpl.SynthesisPrompt，参考点②③④折入，提炼版 ≤3000 runes）要点：
- 输入：MR 描述（数据区围栏）、diff 摘要、合并后 findings 清单；
- claims：从 MR 描述抽可验收声明逐条对代码取证，五态判定；**"描述没提但语义变了"必须
  标 undeclared**（例文 R1 的 off_cpu 就是这么来的）；
- 漏项清单：新字段写入侧赋值/读取侧零值兜底/序列化 tag、"与 X 模块一致"逐行比对、
  构造原缺陷场景验证修复、接口变更向后兼容（老客户端/灰度双版本）；
- design：先做 30 秒判定（共享可变状态/并发调度/刷新时机/跨模块契约，命中任一才产出；
  未命中 Triggered=false 全段省略）——对象图/内聚/耦合/P0P1 迁移，代码级问题**禁止**写进
  design（与坏味分离纪律）；
- 输出纪律：结论必落 path:line；不可追溯的一律写 open_questions 并附核实方法。

**降级**：综合段 LLM 调用失败/解析失败 → Analyst=nil + Completeness.DegradedNotes 追加
注明（"分析师综合段未产出，报告回退标准段落"），报告其余部分照常——analyst 渲染对新字段
N=0 全省略，自然退化为 standard 骨架。

### 4.6 渲染层 `internal/reportview`

`Report` 加 `Template string`（+ Analyst 段字段）。`Render` 入口按 Template 分发：

- **standard / 空**：现有渲染逐字节不动。
- **minimal**：`## 📋 Argus 审查结论` + 结论行（VerdictText）+ 仅 high/critical findings
  （≤10 条，超出注计数）+ 一行 meta。无 compliance/影响面/降级段。
- **analyst**（例文骨架，段落 N=0 一律省略）：
  1. 一段话总结（含规模统计：N 文件 +A/−B）——由现有 VerdictText+Summary 组装；
  2. **声明对照表**（claims 渲染，✅⚠️❌➕❗ 五态图标；与 issue 验收清单并存：清单是
     确定性判定、claims 是模型对 MR 描述的取证）；
  3. **风险链**：findings 按严重度分组渲染（critical/high 为 🔴🟠 风险链主体），chain
     字段缩进子段渲染；
  4. **代码坏味**：low/medium findings 归组编号渲染（例文"三、代码坏味"形态）；
  5. **结构维度**（design.Triggered 才渲染）：对象图/内聚/耦合/迁移 + "如果只做一件事"行；
  6. **测试缺口与待确认**：test_gaps + open_questions 两小节；
  7. 结论 + meta（复用现有 renderMeta）。

### 4.7 选择链路接线

1. `runner.loadRepoConfig` 后 `resolveTemplate(rc, r.Cfg)` → `ptask.Artifacts[plugin.ArtifactReportTemplate]=key`（inject 时）；
2. workflow Run 尾部读同一 Artifacts 键决定综合段是否跑（workflow 直接持有 task）；
3. `buildReport` 把 key + out.Analyst 塞进 `reportview.Report`；
4. poster 不需要改（reportview.Render 内部分发）。

## 5. 信任与降级

- 模板词表硬编码白名单，仓库只能选；模型综合段输出按敌对数据处理（长度钳制+渲染全走
  fence.EscapeUntrusted 纪律）；
- 任何解析失败都退可降级路径：新字段丢 → standard 骨架照常发；
- chain 不进指纹（v3.8.0 纪律）；analyst 新段不参与 R5 裁决（裁决仍只读 findings+AC，
  行为不变）。

## 6. 测试策略

- reporttmpl：词表钉测试；
- plugin：sanitize 词表校验（合法三值+非法拒载）、wire 扩展字段解码兼容（旧 JSON 零值安全）；
- adapter_builtin：analyst Artifacts 键 → systemPrompt 含 chain 指令；无键/standard → 与现状 golden 逐字节一致；
- workflow：综合段触发/不触发（minimal/standard 零调用）、LLM 失败降级 Analyst=nil；
- reportview：三模板渲染 × 各段 N=0 分支、五态图标、design 未触发省略；
- runner：resolveTemplate 优先级三档；
- 全量回归 + arch 门禁。

## 7. 文档契约面

README§3 / skills/argus-mcp/SKILL.md / references/tools.md / ide-mcp 四处契约文档补
report_template 配置项；工具/配置计数同步；发版后「版本·计数·改名·平台·语言」五词 grep
清扫（v6.6.0 教训）。

## 8. 实施顺序

1. reporttmpl 注册表 + 词表钉测试
2. wire/domain 扩展（Claim/Design/AnalystSections/Chain）
3. 配置面（env + yaml + sanitize + 三向钉）
4. builtin chain 指令（Artifacts 通道）
5. workflow 综合段
6. reportview 三模板渲染
7. runner 接线 + 全量测试
8. 文档契约面 + 发版（v6.10.0，部署走 Mac 构建 swap-deploy 链）

---

## 9. 演进记录（v6.11.0，同批次落地）

§2 非目标中三项解禁 + C 类记账一项落地，均按本节留档防重探：

1. **任务级模板覆盖**：`@argus review --template <key>`（词表校验失败截停报错，
   不吞意图）。commentcmd 新增 `ExtractTemplateFlag` 纯词法（保持零依赖，词表归
   service 用 reporttmpl.IsValid 复核）；模板键经 CreateTaskDirect artifacts 进
   任务体，runner 侧再 IsValid 防御复核（API 建任务可绕开 commentcmd）。
2. **repo_settings admin 面**：`report_template` 指针旋钮（形状校验在 Parse，
   词表校验在 runner 消费端——reposettings 刻意零 internal 依赖）。裁决链升为
   四级：**任务级 > repo_settings admin > 仓 yaml > env**。
3. **analyst 规模统计行**（v6.10.0 留空 TODO 兑现）：DiffContext 就地汇总
   N 文件 +A/−B 进报告头一句话总结。
4. **流水线状态进 meta**（C 类记账第一项）：SDK `CommitStatusManager.
   ListCommitStatuses`（optional capability 门）+ `CombineCommitStatusStates`
   统一词表聚合（worst-state），PRContext.CIStatus → 全模板 meta 行
   （✅通过/❌失败/⏹取消/⏳进行中；取不到省略不显式报"无"）。**剩余 C 类记账**：
   本 MR 人工评论去重、影响面六维语义表——仍是独立能力，另立项。

v1 仍不做：仓级自定义模板内容（提示注入面红线不变）。
