package plugin

// 任务 kind 词表（v3.5.6 自 workflow/requirement 上提为 canonical 定义）。
// service / poller 等门面与轮询层只需要词表本身，不应为拿常量反向依赖 L3 编排层；
// requirement 包保留 Kind* 别名兼容包内既有引用。与 IDL TaskKind 对齐
// （service.kindFromIDL/kindToIDL 负责映射）。
const (
	TaskKindRequirementCompliance = "requirement_compliance"
	TaskKindCodeReview            = "code_review"
	TaskKindVisualCheck           = "visual_check"
)
