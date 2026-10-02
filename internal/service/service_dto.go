// service_dto.go —— IDL DTO 转换与输入校验小件：kind/protocol 映射、指针助手、平台路径白名单、artifacts 解码。
package service

import (
	"encoding/json"
	"fmt"

	api "argus/biz/model/api"
	"argus/internal/plugin"
)

// strPtr 可选字段指针化（hz 生成 DTO 的 optional 字段均为指针）。
func strPtr(s string) *string { return &s }

func strPtrOrEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------- helpers ----------

func kindFromIDL(k api.TaskKind) (string, error) {
	switch k {
	case api.TaskKind_REQUIREMENT_COMPLIANCE:
		return plugin.TaskKindRequirementCompliance, nil
	case api.TaskKind_CODE_REVIEW:
		return plugin.TaskKindCodeReview, nil
	case api.TaskKind_VISUAL_CHECK:
		return plugin.TaskKindVisualCheck, nil
	case api.TaskKind_DESCRIBE:
		return plugin.TaskKindDescribe, nil
	case api.TaskKind_CHANGELOG:
		return plugin.TaskKindChangelog, nil
	case api.TaskKind_CI_ANALYSIS:
		return plugin.TaskKindCIAnalysis, nil
	default:
		return "", fmt.Errorf("未知任务类型 %d", int(k))
	}
}

func kindToIDL(k plugin.Kind) api.AgentKind {
	switch k {
	case plugin.KindCLI:
		return api.AgentKind_CLI
	case plugin.KindRemote:
		return api.AgentKind_REMOTE
	default:
		return api.AgentKind_BUILTIN
	}
}

func protocolToIDL(p plugin.Protocol) api.AgentProtocol {
	switch p {
	case plugin.ProtocolA2A:
		return api.AgentProtocol_A2A
	case plugin.ProtocolMCP:
		return api.AgentProtocol_MCP
	default:
		return api.AgentProtocol_NONE
	}
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

// decodeArtifactsMap JSON 对象解码（坏数据安全返回空表）。
func decodeArtifactsMap(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}
