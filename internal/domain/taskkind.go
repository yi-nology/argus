// taskkind.go —— 任务 kind 词表（canonical 定义，v3.106.0 自 plugin 下沉；
// v5.1.0 删除 plugin 侧 re-export 别名，全仓直连本包——间接层完成使命）。
// storage 等数据层被分层规则禁止依赖 plugin（ARCHITECTURE 规则表"数据层不
// 反向依赖域"），此前 isReviewKind 被迫裸字符串克隆词表并已实际漂移。本包是
// storage 与 service/runner 的公共下游，词表落此即单源；
// service.kindFromIDL/kindToIDL 负责与 IDL 枚举映射。

package domain

// 任务 kind 全量词表（7 类）。
const (
	TaskKindRequirementCompliance = "requirement_compliance"
	TaskKindCodeReview            = "code_review"
	TaskKindVisualCheck           = "visual_check"
	// 写作面（v3.71.0，对标 PR-Agent /describe 与 /update_changelog）：
	// 单步 LLM 写作，无 R 系列裁判与插件派发，产物直接平台写回。
	TaskKindDescribe   = "describe"
	TaskKindChangelog  = "changelog"
	TaskKindCIAnalysis = "ci_analysis"
	// 本地 diff 预审（v3.123.0）：无 PR 语义的异步预审任务——输入是提交方
	// 送来的 staged diff 文本（artifacts["diff"]），输出报告落库供轮询，
	// 不回帖。与 review_diff 同一执行体，区别仅在任务化（可异步可轮询）。
	TaskKindDiffPreview = "diff_preview"
)

// taskKindInfo 单条词表注册项：review 标记该 kind 是否走 R 系列审查工作流。
type taskKindInfo struct {
	review bool
}

// taskKinds 词表注册表（registry）：新增任务类型只改这张表。
var taskKinds = map[string]taskKindInfo{
	TaskKindRequirementCompliance: {review: true},
	TaskKindCodeReview:            {review: true},
	TaskKindVisualCheck:           {review: true},
	TaskKindDescribe:              {},
	TaskKindChangelog:             {},
	TaskKindCIAnalysis:            {},
	TaskKindDiffPreview:           {},
}

// IsKnownKind 建任务白名单判定：kind 是否在注册表内。
func IsKnownKind(kind string) bool {
	_, ok := taskKinds[kind]
	return ok
}

// IsReviewKind 审查类任务判定（last_head_sha 去重语义的适用面）：R 系列审查
// 三类。写作面（describe/changelog）与 ci_analysis 不适用——它们不写也不继承
// LastHeadSHA，否则"MR 先审完、CI 再挂"时同 SHA 的归因任务会被误挡。
func IsReviewKind(kind string) bool {
	return taskKinds[kind].review
}
