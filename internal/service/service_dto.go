// service_dto.go —— IDL DTO 转换与输入校验小件：kind/protocol 映射、指针助手、平台路径白名单、artifacts 解码。

package service

import (
	"encoding/json"
	"math"

	"fmt"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"

	"github.com/oekit/pkg/ptrx"
)

// strPtr 可选字段指针化（hz 生成 DTO 的 optional 字段均为指针）。
func strPtr(s string) *string { return ptrx.Of(s) }

func strPtrOrEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return ptrx.Of(s)
}

// ---------- helpers ----------

// kindIDLTable IDL 枚举 → 内部 kind 词表的映射注册表（canonical 词表见
// domain/taskkind.go，经 plugin re-export）。新增任务类型时在此补一行映射。
var kindIDLTable = map[argus.TaskKind]string{
	argus.TaskKind_REQUIREMENT_COMPLIANCE: domain.TaskKindRequirementCompliance,
	argus.TaskKind_CODE_REVIEW:            domain.TaskKindCodeReview,
	argus.TaskKind_VISUAL_CHECK:           domain.TaskKindVisualCheck,
	argus.TaskKind_DESCRIBE:               domain.TaskKindDescribe,
	argus.TaskKind_CHANGELOG:              domain.TaskKindChangelog,
	argus.TaskKind_CI_ANALYSIS:            domain.TaskKindCIAnalysis,
}

func kindFromIDL(k argus.TaskKind) (string, error) {
	kind, ok := kindIDLTable[k]
	if !ok {
		return "", fmt.Errorf("未知任务类型 %d", int(k))
	}
	return kind, nil
}

// taskFindings 任务 ResultJSON 的 findings 载荷解码单源（GetTask/Findings 两个
// 投影共用；写侧在 runner 落库，wire 契约形变只改这里）。解析失败返回
// (nil, false)，空 findings 数组返回 (空切片, true)——两态语义对消费方可区分。
func taskFindings(resultJSON string) ([]plugin.Finding, bool) {
	var payload struct {
		Findings []plugin.Finding `json:"findings"`
	}
	if json.Unmarshal([]byte(resultJSON), &payload) != nil {
		return nil, false
	}
	return payload.Findings, true
}

func kindToIDL(k plugin.Kind) argus.AgentKind {
	switch k {
	case plugin.KindCLI:
		return argus.AgentKind_CLI
	case plugin.KindRemote:
		return argus.AgentKind_REMOTE
	default:
		return argus.AgentKind_BUILTIN
	}
}

func protocolToIDL(p plugin.Protocol) argus.AgentProtocol {
	switch p {
	case plugin.ProtocolA2A:
		return argus.AgentProtocol_A2A
	case plugin.ProtocolMCP:
		return argus.AgentProtocol_MCP
	default:
		return argus.AgentProtocol_NONE
	}
}

// clampInt32 int → int32 饱和钳制。Finding.Line 存于自有 DB（ResultJSON）但
// 内容源自插件/模型输出，垃圾大数直接截断会回绕成负行号污染 API 消费方
// （gosec G115）。
func clampInt32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// artifactsMaxBytes artifacts 序列化总量上限（16KB：正常注入面只有 testgen/
// checkpoint 类小产物）。
const artifactsMaxBytes = 16 * 1024

// isPlatformPath 平台 owner/repo 标识白名单：字母数字、. _ -，repo 侧允许
// 子组斜杠。拒绝引号/空白/控制符/unicode 异体等进平台 API URL 的形态。
func isPlatformPath(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-', c == '/':
		default:
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------- Q&A 评论交互（@argus ask）----------
