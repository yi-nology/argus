// taskkind.go —— 任务 kind 词表门面（v3.5.6 自 workflow/requirement 上提；
// v3.106.0 canonical 定义下沉 domain/taskkind.go——storage 被分层规则禁止
// 依赖 plugin，词表落公共下游 domain 即单源）。本包保留 re-export 别名：
// L4 契约单源门面不变，service/poller/runner 等既有消费方零改动。

package plugin

import "argus/internal/domain"

// 任务 kind 词表（canonical 见 internal/domain/taskkind.go）。
const (
	TaskKindRequirementCompliance = domain.TaskKindRequirementCompliance
	TaskKindCodeReview            = domain.TaskKindCodeReview
	TaskKindVisualCheck           = domain.TaskKindVisualCheck
	TaskKindDescribe              = domain.TaskKindDescribe
	TaskKindChangelog             = domain.TaskKindChangelog
	TaskKindCIAnalysis            = domain.TaskKindCIAnalysis
	TaskKindDiffPreview           = domain.TaskKindDiffPreview
)

// IsKnownKind 建任务白名单判定（转发 domain 词表注册表）。
func IsKnownKind(kind string) bool { return domain.IsKnownKind(kind) }
