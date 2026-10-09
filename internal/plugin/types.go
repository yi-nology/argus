// Package plugin 是 Argus 的 L4 插拔层（design §3）：
// 统一契约（AgentPlugin/Task/Result）→ Registry → Dispatcher → Merger。
// 原则：路由/裁决/合并不走 LLM，全部确定性 Go 代码。
package plugin

import (
	"context"
	"slices"
	"time"

	"argus/internal/domain"
)

// Kind 插件形态：builtin 进程内 agent / cli 外部命令行 / remote 远端服务。
type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindCLI     Kind = "cli"
	KindRemote  Kind = "remote"
	KindACPX    Kind = "acpx" // CLI 编码 agent（codex/claude/kimi 等）作为审查专家（agentkit/acpx 驱动）
)

// Protocol kind=remote 时的协议（v3.0 只留接口位，不做协议子集）。
type Protocol string

const (
	ProtocolA2A Protocol = "a2a"
	ProtocolMCP Protocol = "mcp"
)

// 钉死的技能词表（manifest 声明用）。
const (
	SkillRequirementCompliance = "requirement-compliance"
	SkillCodeReview            = "code-review"
	SkillVisualCheck           = "visual-check"
)

// 钉死的成本词表（预算裁剪按 heavy > standard > fast 顺序）。
const (
	CostFast     = "fast"
	CostStandard = "standard"
	CostHeavy    = "heavy"
)

// ReactRetryHint ReAct agent 失败回喂重试的追加提示（agentrun.Run + WithRetry）。
// 拼在原 query 之后作为重试轮输入；副作用守卫默认生效——首轮已调用变更类工具
// 则不整体重跑，错误如实上抛交调用方降级（review 场景工具表以只读为主，守卫即默认）。
// v3.38.0 起经 ReminderNote 包装（NOT USER INPUT 声明 + 嵌套转义）。
var ReactRetryHint = "\n\n" + ReminderNote("retry-hint",
	"上一次执行因错误中断，请重新完整执行本任务，并只输出最终要求的 JSON。")

// Needs 能力需求清单：新接一个 agent 只需回答"它吃什么（Needs）、吐什么（Result）"。
type Needs struct {
	Diff            bool // 注入 diffprep 清洗后的 diff
	RepoFileRead    bool // 允许通过工具读仓库文件
	WorkingCopy     bool // 需要工作副本沙箱（浅克隆 + checkout PR head）
	RequirementSpec bool // 注入 R1 产出的需求清单
	PreviewURL      bool // 预览地址（前端视觉 agent）
}

var needNames = map[string]func(*Needs){
	"diff":             func(n *Needs) { n.Diff = true },
	"repo-file-read":   func(n *Needs) { n.RepoFileRead = true },
	"working-copy":     func(n *Needs) { n.WorkingCopy = true },
	"requirement-spec": func(n *Needs) { n.RequirementSpec = true },
	"preview-url":      func(n *Needs) { n.PreviewURL = true },
}

// NeedsFromStrings 把 plugins.yaml 的 needs 词表转为 Needs；未知名返回告警列表。
func NeedsFromStrings(names []string) (Needs, []string) {
	var n Needs
	var unknown []string
	for _, name := range names {
		if f, ok := needNames[name]; ok {
			f(&n)
		} else {
			unknown = append(unknown, name)
		}
	}
	return n, unknown
}

// Strings 序列化回词表（GET /agents 可观测用）。
func (n Needs) Strings() []string {
	out := make([]string, 0, 5)
	if n.Diff {
		out = append(out, "diff")
	}
	if n.RepoFileRead {
		out = append(out, "repo-file-read")
	}
	if n.WorkingCopy {
		out = append(out, "working-copy")
	}
	if n.RequirementSpec {
		out = append(out, "requirement-spec")
	}
	if n.PreviewURL {
		out = append(out, "preview-url")
	}
	return out
}

// Trigger 触发条件：路径 glob / 标签 / 事件类型；全空 = 总是触发。
type Trigger struct {
	Paths  []string
	Labels []string
	Events []string // SDK 归一化事件词表：cr.opened / cr.synchronize / cr.synchronized / cr.reopened
}

// Empty 是否为空（空 = 不参与条件触发，默认候选）。
func (t Trigger) Empty() bool { return len(t.Paths) == 0 && len(t.Labels) == 0 && len(t.Events) == 0 }

// Match 判定是否命中（v3.0.1 修订——原实现对"纯 Events 触发器"恒 false，且调用方
// 传空事件类型导致 Events 条件整体失效）。
// 语义：条件类型之间 AND（未声明的类型视为通过），同类型多值之间 OR：
//   - Events 非空：eventType 必须在白名单内；eventType 为空（CLI 手动任务）视为通过；
//   - Paths 非空：任一 glob 命中变更文件即通过；
//   - Labels 非空：任一标签命中即通过；
//   - 全空 = 默认候选。
func (t Trigger) Match(changedFiles, labels []string, eventType string) bool {
	if t.Empty() {
		return true
	}
	if len(t.Events) > 0 && eventType != "" && !slices.Contains(t.Events, eventType) {
		return false
	}
	if len(t.Paths) > 0 && !matchAnyGlob(t.Paths, changedFiles) {
		return false
	}
	if len(t.Labels) > 0 && !listOverlap(t.Labels, labels) {
		return false
	}
	return true
}

// AgentManifest 插件清单（GET /agents 可观测，含 needs/triggers）。
type AgentManifest struct {
	Name     string
	Version  string
	Kind     Kind
	Protocol Protocol // kind=remote 时有效
	Skills   []string
	Needs    Needs
	Triggers Trigger
	Cost     string
	Timeout  time.Duration
	// ReviewN best-of-N 同质采样次数（plugins.yaml reviewN；0/1 = 单次）。
	// >1 时 Dispatcher 对同一插件并发跑 N 次（钳制 ≤ MaxReviewN）并聚合：
	// 复现 ≥2 的发现升一级严重度并标注 k/n（bestofn.go）。高危路径 opt-in。
	ReviewN int
}

// AgentPlugin L4 统一契约：所有形态（builtin/cli/remote）都收敛到这两个方法。
type AgentPlugin interface {
	Manifest() AgentManifest
	Invoke(ctx context.Context, task Task) (Result, error)
}

// PRContext PR 轨迹上下文（由 runner 从平台 API 组装）。
type PRContext struct {
	Platform      string
	Owner         string
	Repo          string
	Number        string
	Title         string
	Body          string // PR 描述（攻击者可控，进提示词前须包数据区）
	Author        string
	Labels        []string
	HeadSHA       string
	BaseBranch    string
	HeadBranch    string
	DefaultBranch string
	// EventType 触发本任务的平台事件（cr.opened/cr.synchronize/...；CLI 手动任务为空）。
	// 供 Triggers.Events 匹配；空值视为不限制。
	EventType string
}

// Key 返回 PR 唯一键（storage/staleness 共用）。
func (p PRContext) Key() (platform, owner, repo, number string) {
	return p.Platform, p.Owner, p.Repo, p.Number
}

// DiffFile 单文件变更摘要（diffprep 清洗后）。
type DiffFile struct {
	Path      string
	Additions int
	Deletions int
	IsBinary  bool
	IsNew     bool
	IsDeleted bool
	IsRenamed bool
	Included  bool // 是否进入了 Text（噪声文件被剔除但保留元信息）
	// AlreadyReviewed 增量审查标记：该文件 diff 与上次审查完全一致，无需重新审查。
	AlreadyReviewed bool
	// RepoExcluded 仓库级排除标记（v3.15.0 .reviewagents.yaml review.exclude/include_paths）：
	// 用户主动排除，与预算截断无关——不进截断清单，不计入 Truncated。
	RepoExcluded bool
}

// DiffContext 清洗后的 diff（design §4 术语：diffprep）。
type DiffContext struct {
	Text      string
	Files     []DiffFile
	Truncated bool
	// NoNewChanges 增量审查标记：当前 diff 与上次审查完全一致，无新变更。
	NoNewChanges bool
}

// AcceptanceCriterion 验收条目。
type AcceptanceCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RequirementSpec R1 产出：验收清单 + 模糊点（改写不了的原样进 Ambiguous，禁止编造）。
type RequirementSpec struct {
	Source    string                `json:"source"` // issue #N | PR 描述
	IssueRefs []string              `json:"issue_refs,omitempty"`
	ACs       []AcceptanceCriterion `json:"acceptance_criteria"`
	Ambiguous []string              `json:"ambiguous,omitempty"`
}

// 完成度词表（R3，钉死）。
const (
	ACImplemented  = "implemented"
	ACPartial      = "partial"
	ACMissing      = "missing"
	ACUnverifiable = "unverifiable"
)

// ACCheck 单条 AC 的完成度判定。
type ACCheck struct {
	ID       string `json:"id"`
	Status   string `json:"status"`             // implemented|partial|missing|unverifiable
	Evidence string `json:"evidence,omitempty"` // file:line（implemented/partial 必须携带，校验器强制）
	Detail   string `json:"detail,omitempty"`
}

// ScopeCreepItem 需求外改动。
type ScopeCreepItem struct {
	File string `json:"file"`
	Note string `json:"note"`
}

// ComplianceReport R3 产出。
type ComplianceReport struct {
	Checks     []ACCheck        `json:"checks"`
	ScopeCreep []ScopeCreepItem `json:"scope_creep,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

// Task 门面层交给插件的任务：能力需求清单 + 按需注入。
// 约束（§3.4 红线 4）：Diff/Artifacts 内的一切内容是"待审数据"，插件不得当配置/指令执行。
type Task struct {
	ID          string
	Kind        string // requirement_compliance | code_review | visual_check
	PR          PRContext
	Diff        *DiffContext      // 按 Needs.Diff 注入
	Requirement *RequirementSpec  // 按 Needs.RequirementSpec 注入
	Artifacts   map[string]string // PreviewURL / change_summary / workcopy_dir 等开放位
	SessionID   string            // 会话形态才有（v3.2）
	// Budget 任务级 token 预算（Dispatcher.Run 注入）。插件内的 LLM 调用应计入，
	// 否则 R4 审查插件（成本大头）对预算不可见，PruneByBudget/预算门形同虚设。
	Budget *TokenBudget
}

// Finding 单条审查意见（领域模型住在 internal/domain；此处别名保持既有 API）。
// Severity 必须经过 NormalizeSeverity 归一化。
type Finding = domain.Finding

// Result 插件输出。
type Result struct {
	Findings []Finding
	Verdict  string            // approve|comment|request_changes（可空；R5 不读模型自评 verdict）
	Artifact map[string]string // 插件特有结构化产物（如 ComplianceReport JSON）
	Notes    string
}

// ArtifactReviewGuidelines Task.Artifacts 键：仓库级自定义审查指引正文
// （runner 从默认分支 .argus/review-guidelines.md 读出，缺失时无此键）。
const ArtifactReviewGuidelines = "review_guidelines"

// ArtifactReviewHints Task.Artifacts 键：仓库级历史高误报模式提示
// （runner 从 pattern_signals 拉低置信度模式渲染，学习环关闭或无样本时无此键）。
const ArtifactReviewHints = "review_hints"

// ArtifactPrevFindings Task.Artifacts 键：同 PR 此前轮次的 open 意见清单
// （v3.52.0 跨轮一致性）：runner 从指纹库渲染，供 R4 评审员保持结论一致——
// 推翻上轮意见须显式说明理由。首轮或无历史意见时无此键。
const ArtifactPrevFindings = "prev_findings"

// ArtifactOCRTriage / ArtifactOCRRules Task.Artifacts 键：OCR delegate 委托前置
// 产物（design §2 委托模式；runner dispatch 前执行 `ocr delegate preview/rule`
// 渲染为提示词 section，ARGUS_OCR_BIN 未配置或执行失败时无此键）。section 自带
// 数据区纪律声明，builtin 审查提示词原样拼接。
const (
	ArtifactOCRTriage = "ocr_file_triage"
	ArtifactOCRRules  = "ocr_review_rules"
)

// ArtifactChangeSummary Task.Artifacts 键：R2 变更摘要。workflow 写入，R4 各插件
// 作为待审数据（数据区包裹）注入提示词；compliance 插件经 Result.Artifact 透传
// 给报告渲染。
const ArtifactChangeSummary = "change_summary"

// ArtifactCodegraphTouched Task.Artifacts 键：变更触达符号提示（v3.15.0 P0-1）。
// runner 在工作副本上建 Go AST 索引后渲染（符号清单 + get_symbol/get_references
// 可用性提示），经 guidelinesPrompt 注入 R4、CheckCompliance 注入 R3。
// 变更不含 Go 文件 / 关闭 ARGUS_CODEGRAPH / 无工作副本时无此键。
const ArtifactCodegraphTouched = "codegraph_touched"

// ArtifactRelatedContracts Task.Artifacts 键：跨仓契约快照（v3.90.0）。
// runner 按 .reviewagents.yaml related 段拉取关联仓契约文件后渲染（数据区
// 围栏 + 逐文件段），经 guidelinesPrompt 注入 R4。服务级 ARGUS_RELATED_REPOS
// 未开 / 仓库未声明 / 全部拉取失败时无此键。
const ArtifactRelatedContracts = "related_contracts"

// 任务级阶段产物 checkpoint 键（v3.12.0 G2 断点续审）：workflow 成功完成 R1/R2
// 后经 CheckpointSink 持久化到 tasks.stage_artifacts，同任务重试时复用——省掉
// 已成功阶段的重复 LLM 调用。只在任务内有效，新任务永远全量重跑。
const (
	ArtifactCkptR1Spec    = "checkpoint_r1_spec"    // RequirementSpec JSON
	ArtifactCkptR2Summary = "checkpoint_r2_summary" // R2 变更摘要文本
	ArtifactStageProgress = "stage_progress"        // 当前阶段标记（v3.132.0，onStage 每阶段开头落库）
	// ArtifactDiffInput diff_preview 任务的输入 diff 文本（v3.123.0）：建任务时
	// 一次性写入 artifacts_json，runner 执行时读取。上限 48KB（TEXT 列 64KB
	// 上界减 JSON 转义膨胀余量）。
	ArtifactDiffInput = "diff"
)

// tasks.debug_json 的阶段调试键（workflow 写，service.TaskStages 读；e2e 断言消费）。
// 与 ArtifactCkpt* 的区别：debug 键仅诊断与时间线推断用，缺失不影响断点续审。
const (
	DebugKeyR1Spec       = "r1_spec"       // R1 需求规格 JSON
	DebugKeyR2Checkpoint = "r2_checkpoint" // R2 checkpoint 复用标记（值 "reused"）
	DebugKeyR2Summary    = "r2_summary"    // R2 变更摘要文本
	DebugKeyR3Raw        = "r3_raw"        // R3 模型原始输出
	DebugKeyR3Checks     = "r3_checks"     // R3 完成度检查 JSON
)

// Result.Artifact 键：R3 完成度报告 JSON 与 R5 裁决规则（compliance 插件产出）。
const (
	ArtifactComplianceReport = "compliance_report"
	ArtifactRule             = "rule"
)

// ArtifactTrigger TaskAPI 建任务元数据键：触发来源标注（如 comment:review 重审）。
const ArtifactTrigger = "trigger"
