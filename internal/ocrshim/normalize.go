// Package ocrshim 把 alibaba/open-code-review（OCR）CLI 包装成 MCP server
// （streamable HTTP），使 argus 能以 remote/mcp 插件形态调用 OCR——
// OCR 本体只支持 MCP 客户端方向（官方文档 §MCP），被 MCP 调用必须经本 shim。
//
// sidecar 形态：argus 侧准备工作副本（workcopy 池），目录经 payload.workcopy_dir
// 下发；shim 在该目录执行 OCR（git 凭证已嵌在 clone URL 中，unshallow 后 OCR 的
// merge-base range 审查可用），把 OCR JSON 归一化为 argus findings 契约返回。
// 进程托管/环境白名单/stdout 限容全部复用 agentkit/acpx（与 adapter_cli 同底座）。
package ocrshim

import (
	"encoding/json"
	"strings"

	"git.example.com/enjoydream/agentkit/reportutil"

	"argus/internal/plugin"
	"argus/internal/plugin/adapter_cli"
)

// ocrComment OCR v1.12+ `review --format json` 单条意见（上游 internal/model.LlmComment 同构）。
type ocrComment struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Severity       string `json:"severity,omitempty"`
}

// normalizeFindings OCR stdout → argus findings：
//   - 新 schema（path/content/start_line/severity）本地映射，critical 归 high；
//   - 旧 schema（path/comment/line/level 变体）回退 adapter_cli 容错解析；
//   - 空数组 = 合法零意见；解析不出 = 错误上抛（Merger 部分失败容忍）。
func normalizeFindings(stdout string) ([]plugin.Finding, error) {
	items, found := extractCommentsArray(strings.TrimSpace(stdout))
	if !found {
		return adapter_cli.ParseFindings(stdout)
	}
	if len(items) == 0 {
		return nil, nil
	}
	var out []plugin.Finding
	for _, raw := range items {
		var c ocrComment
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		if c.Path == "" || strings.TrimSpace(c.Content) == "" {
			continue
		}
		line := c.StartLine
		if line == 0 {
			line = c.EndLine
		}
		sev, _ := reportutil.Normalize(c.Severity) // critical→high；越界归 low（Merger 兜底告警）
		out = append(out, plugin.Finding{
			File:         c.Path,
			Line:         line,
			Severity:     sev,
			Comment:      strings.TrimSpace(c.Content),
			SuggestedFix: strings.TrimSpace(c.SuggestionCode),
		})
	}
	if len(out) == 0 {
		// 条目全部缺 content → 可能是旧 schema，回退容错解析（其零条目即报错语义）
		return adapter_cli.ParseFindings(stdout)
	}
	return out, nil
}

// extractCommentsArray 提取意见数组：裸数组或 {"comments":[...]}。
// found=false 表示两种形态都不符（交由旧版兜底报错）。
func extractCommentsArray(s string) (items []json.RawMessage, found bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &probe); err == nil {
		raw, ok := probe["comments"]
		if !ok {
			return nil, false
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false
		}
		return items, true
	}
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, false
	}
	return items, true
}
