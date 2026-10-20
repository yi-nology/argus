// Package adapter_cli cli 形态适配器（design §3.2）：包装外部命令行（首个实战对象是
// alibaba/open-code-review）。子进程托管整体委托 oagentkit/runtime/procx.Run（v0.7.1 起）——
// 进程组（Setpgid）→ 超时/取消 TERM 整组 → 3s grace 后 KILL、环境白名单、stdout 限容、
// ErrTimeout/ErrCanceled sentinel 全部由底座提供；本包只保留 OCR JSON 容错解析。
// 供应链信任（§8）：cmd 只能来自 Hub plugins.yaml 白名单，仓库配置无权声明（§3.4 红线 2）。
package adapter_cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oagentkit/runtime/procx"
	"github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"argus/internal/plugin"
)

// CLIPlugin 通用 cli 插件。
type CLIPlugin struct {
	manifest plugin.AgentManifest
	Cmd      []string
	Mode     string   // full | delegate（delegate v3.0 留接口，§2）
	Env      []string // 额外透传的环境变量名白名单（来自 plugins.yaml env 声明）
	wc       *workcopy.Pool
	log      logx.Logger
}

// New 构造 cli 插件。
func New(m plugin.AgentManifest, cmd []string, mode string, env []string,
	wc *workcopy.Pool, log logx.Logger) *CLIPlugin {
	if m.Timeout <= 0 {
		m.Timeout = 300 * time.Second
	}
	return &CLIPlugin{manifest: m, Cmd: cmd, Mode: mode, Env: env, wc: wc, log: log}
}

func (p *CLIPlugin) Manifest() plugin.AgentManifest { return p.manifest }

// Invoke 执行链：工作副本就绪 → exec 白名单命令 → 解析 JSON → Result。
func (p *CLIPlugin) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	if strings.EqualFold(p.Mode, "delegate") {
		// delegate 产物是"审查输入"（文件筛选+规则）而非 findings，且 Dispatcher
		// 并发 fan-out 无插件间传递管道——不在插件层实装。已实装为 runner 层
		// dispatch 前置步骤（internal/ocrdeleg，ARGUS_OCR_BIN 启用，v3.9.0）。
		return plugin.Result{}, fmt.Errorf("cli 插件不支持 delegate 模式（delegate 已由 runner 前置实装：ARGUS_OCR_BIN）")
	}
	if p.wc == nil {
		return plugin.Result{}, fmt.Errorf("cli 插件未接入工作副本服务")
	}
	dir, err := p.wc.Ensure(ctx, plugin.PRToKey(task.PR))
	if err != nil {
		return plugin.Result{}, fmt.Errorf("准备工作副本失败: %w", err)
	}
	defer p.wc.Release(plugin.PRToKey(task.PR))

	// 进程托管委托 oagentkit/tools/acpx（env 白名单按 plugins.yaml env 声明按名透传；
	// MaxStdout 维持 1MB 上限防异常 cli 刷屏）
	stdout, _, _, err := procx.Run(ctx, procx.RunRequest{
		Argv:      p.Cmd,
		Dir:       dir,
		Env:       p.Env,
		Timeout:   p.manifest.Timeout,
		MaxStdout: 1 << 20,
	})
	if err != nil {
		// v0.7.0 sentinel：超时与取消可程序化区分（观测/告警不再靠文案猜）
		switch {
		case errors.Is(err, procx.ErrTimeout):
			return plugin.Result{}, fmt.Errorf("cli 执行超时（%s）", p.manifest.Name)
		case errors.Is(err, procx.ErrCanceled):
			return plugin.Result{}, fmt.Errorf("cli 执行被取消（%s）: %w", p.manifest.Name, err)
		}
		return plugin.Result{}, fmt.Errorf("cli 执行失败: %w", err)
	}
	findings, err := ParseFindings(stdout)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("解析 cli JSON 输出失败: %w", err)
	}
	for i := range findings {
		findings[i].By = p.manifest.Name
		// finding 文本截断（v3.39.0）：cli stdout 半可信且文本无上限——超长
		// comment 经 R3.5 反思 wire 与报告进入上下文（条数有封顶、文本此前没有）
		// 截断留痕尾注经 textutil.TruncNote 单源（v3.64.0）
		findings[i].Comment = textutil.TruncNote(findings[i].Comment, findingCommentMaxRunes, "截断")
	}
	return plugin.Result{Findings: findings}, nil
}

// findingCommentMaxRunes 单条 finding 文本上限（rune）。
const findingCommentMaxRunes = 2000

// ---------- OCR JSON → Findings 容错解析 ----------
// OCR（open-code-review）`--format json` 的字段名存在变体（path/file、level/severity、
// comment/message），且输出可能是裸数组或 {comments:[...]} 包装——全部做容错归一。

// ParseFindings 解析 cli 的 JSON 输出为 Findings（severity 经词表归一化）。
func ParseFindings(stdout string) ([]plugin.Finding, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, fmt.Errorf("cli 无输出")
	}
	items, err := extractArray(trimmed)
	if err != nil {
		return nil, err
	}
	var out []plugin.Finding
	for _, it := range items {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		file := firstString(obj, "path", "Path", "file", "File", "filename", "Filename")
		if file == "" {
			continue
		}
		line, _ := strconv.Atoi(strings.TrimSpace(firstString(obj, "line", "Line", "Line_number", "line_number")))
		comment := firstString(obj, "comment", "Comment", "message", "Message", "body", "Body", "description", "Description")
		rawSev := firstString(obj, "level", "Level", "severity", "Severity", "priority", "Priority")
		sev, ok := reportutil.Normalize(rawSev)
		if !ok {
			sev = reportutil.Low // 越界归 low（Merger 亦会兜底告警）
		}
		if comment == "" {
			continue
		}
		suggestedFix := firstString(obj, "suggested_fix", "SuggestedFix", "suggestedFix", "fix", "Fix")
		out = append(out, plugin.Finding{File: file, Line: line, Severity: sev, Comment: comment, SuggestedFix: suggestedFix})
	}
	if len(out) == 0 {
		if len(items) == 0 {
			// 合法的"零意见"输出（干净 PR 的 [] / {"comments":[]}）——
			// 以前这里返回 error，连续 3 个干净 PR 会把插件熔断摘除 5 分钟。
			return nil, nil
		}
		return nil, fmt.Errorf("cli JSON 中未解析出任何 finding（输出前 200 字节: %.200s）", trimmed)
	}
	return out, nil
}

// extractArray 支持三种形态：[...]、{"comments":[...]}、{"data":{"comments":[...]}} 等。
func extractArray(s string) ([]any, error) {
	var raw []any
	if err := json.Unmarshal([]byte(s), &raw); err == nil {
		return raw, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return nil, fmt.Errorf("输出不是合法 JSON: %w", err)
	}
	for _, key := range []string{"comments", "findings", "issues", "data", "result", "results"} {
		if v, ok := obj[key]; ok {
			switch t := v.(type) {
			case []any:
				return t, nil
			case map[string]any:
				for _, k2 := range []string{"comments", "findings", "issues", "items"} {
					if v2, ok := t[k2].([]any); ok {
						return v2, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("JSON 中未找到 comments/findings 数组")
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return strings.TrimSpace(t)
			case float64:
				return strconv.Itoa(int(t))
			}
		}
	}
	return ""
}
