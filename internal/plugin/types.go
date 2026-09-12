// Package plugin 是 Argus 的 L4 插拔层（design §3）：
// 统一契约（AgentPlugin/Task/Result）→ Registry → Dispatcher → Merger。
// 原则：路由/裁决/合并不走 LLM，全部确定性 Go 代码。
package plugin

import (
	"context"
	"time"
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
	if len(t.Events) > 0 && eventType != "" && !contains(t.Events, eventType) {
		return false
	}
	if len(t.Paths) > 0 && !matchAnyGlob(t.Paths, changedFiles) {
		return false
	}
	if len(t.Labels) > 0 && !matchAnyLabel(t.Labels, labels) {
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
	IsDraft       bool
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

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Finding 单条审查意见。Severity 必须经过 NormalizeSeverity 归一化。
type Finding struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Severity     string `json:"severity"`
	Comment      string `json:"comment"`
	By           string `json:"by"`
	SuggestedFix string `json:"suggested_fix,omitempty"` // 可选：修复建议代码片段
}

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
//（runner 从 pattern_signals 拉低置信度模式渲染，学习环关闭或无样本时无此键）。
const ArtifactReviewHints = "review_hints"
