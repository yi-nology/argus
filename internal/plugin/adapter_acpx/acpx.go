// Package adapter_acpx acpx 形态适配器：把 CLI 编码 agent（codex/claude/kimi 等，
// agentkit/acpx 注册表）接入 Argus 的 R4 专家委派。
//
// 与 adapter_cli 的区别：cli 适配器执行确定性命令行（OCR），本适配器驱动的是
// 有自主工具调用能力的编码 agent——它能在工作副本里读代码、跑 git，按提示词
// 契约输出 findings JSON。
//
// 安全模型（继承 acpx 底座）：沙箱只读（SandboxReadonly）、环境变量白名单、
// 进程组三段式清理；agent 输出视为不可信数据（severity 归一化 + JSON 容错解析）。
package adapter_acpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/acpx"
	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/severity"
	"git.example.com/enjoydream/agentkit/workcopy"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"
)

// ErrBinaryMissing agent 可执行文件不存在（插件停用而非报错启动）。
var ErrBinaryMissing = fmt.Errorf("acpx agent 可执行文件不存在")

// bins 词表：agent 名 → CLI 二进制（LookPath 探测用，与 acpx 预设一一对应）。
var bins = map[string]string{
	"claude": "claude", "zcode": "zcode", "codex": "codex",
	"opencode": "opencode", "minimax": "minimax-code", "kimi": "kimi",
	"gemini": "gemini", "qwen": "qwen", "mimo": "mimo",
}

// newAgent 按名构造 acpx Agent（与 acpx.NewRegistry 的缺省集一致）。
func newAgent(name string) (acpx.Agent, bool) {
	switch name {
	case "claude":
		return acpx.NewClaudeCode(), true
	case "zcode":
		return acpx.NewZCode(), true
	case "codex":
		return acpx.NewCodex(), true
	case "opencode":
		return acpx.NewOpenCode(), true
	case "minimax":
		return acpx.NewMinimax(), true
	case "kimi":
		return acpx.NewKimi(), true
	case "gemini":
		return acpx.NewGemini(), true
	case "qwen":
		return acpx.NewQwen(), true
	case "mimo":
		return acpx.NewMimo(), true
	default:
		return nil, false
	}
}

// Plugin acpx 专家审查插件。
type Plugin struct {
	manifest plugin.AgentManifest
	agent    acpx.Agent
	wc       *workcopy.Pool
	log      logx.Logger
}

// New 构造 acpx 插件。agentName 未注册返回错误；二进制缺失返回 ErrBinaryMissing
// （调用方应停用插件而非启动失败——环境没有装对应 CLI 是正常形态）。
func New(m plugin.AgentManifest, agentName string, wc *workcopy.Pool, log logx.Logger) (*Plugin, error) {
	agent, ok := newAgent(agentName)
	if !ok {
		return nil, fmt.Errorf("acpx: 未注册的 agent %q", agentName)
	}
	bin, ok := bins[agentName]
	if !ok {
		bin = agentName
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBinaryMissing, bin)
	}
	return &Plugin{manifest: m, agent: agent, wc: wc, log: log}, nil
}

func (p *Plugin) Manifest() plugin.AgentManifest { return p.manifest }

// Invoke 执行链：工作副本就绪（可选）→ agent 只读审查 → findings JSON 容错解析。
func (p *Plugin) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	workDir, release := p.ensureWorkDir(ctx, task)
	defer release()

	prompt := buildPrompt(task, workDir != "")
	p.log.Info("argus.acpx.run.start", "plugin", p.manifest.Name, "agent", p.agent.Name(),
		"prompt_chars", len([]rune(prompt)), "workdir", workDir != "")
	start := time.Now()
	res, err := p.agent.Run(ctx, acpx.RunRequest{
		Prompt:  prompt,
		WorkDir: workDir,
		// 审查是只读任务：sandbox=readonly（claude→plan / codex→read-only / gemini→default）
		Sandbox: acpx.SandboxReadonly,
	})
	if err != nil {
		// agentkit v0.7.0 sentinel：超时/取消程序化区分（dispatcher 层 ctx 常先于
		// 插件自身超时到——此前两者在日志里无法区分）
		kind := "error"
		switch {
		case errors.Is(err, acpx.ErrTimeout):
			kind = "timeout"
		case errors.Is(err, acpx.ErrCanceled):
			kind = "canceled"
		}
		p.log.Warn("argus.acpx.run.error", "plugin", p.manifest.Name, "agent", p.agent.Name(),
			"duration_ms", time.Since(start).Milliseconds(), "kind", kind, "error", err.Error())
		return plugin.Result{}, fmt.Errorf("acpx agent %s 执行失败（%s）: %w", p.agent.Name(), kind, err)
	}

	findings := parseFindings(res.Text)
	for i := range findings {
		findings[i].By = p.manifest.Name
	}
	p.log.Info("argus.acpx.run.end", "plugin", p.manifest.Name, "agent", p.agent.Name(),
		"duration_ms", time.Since(start).Milliseconds(),
		"findings", len(findings), "text_chars", len([]rune(res.Text)),
		"prompt_tokens", res.Usage.InputTokens, "completion_tokens", res.Usage.OutputTokens,
		"cost_usd", res.Usage.CostUSD, "session_id", res.SessionID)
	return plugin.Result{Findings: findings, Notes: fmt.Sprintf("%s(%s) 完成", p.manifest.Name, p.agent.Name())}, nil
}

// ensureWorkDir 需要工作副本时 Ensure（失败降级为无目录纯 diff 审查，不失败）。
// 返回目录与释放函数（无目录时 release 为 no-op）；引用必须覆盖 agent 运行全程，
// 提前释放会触发引用归零删目录。
func (p *Plugin) ensureWorkDir(ctx context.Context, task plugin.Task) (string, func()) {
	noop := func() {}
	if p.wc == nil || task.PR.HeadSHA == "" {
		return "", noop
	}
	key := workcopy.WorktreeKey{
		Platform: task.PR.Platform, Owner: task.PR.Owner, Repo: task.PR.Repo,
		Number: task.PR.Number, HeadSHA: task.PR.HeadSHA, DefaultBranch: task.PR.DefaultBranch,
	}
	dir, err := p.wc.Ensure(ctx, key)
	if err != nil {
		p.log.Warn("argus.acpx.workcopy_failed", "plugin", p.manifest.Name, "error", err.Error(),
			"fallback", "纯 diff 审查")
		return "", noop
	}
	return dir, func() { p.wc.Release(key) }
}

// buildPrompt 审查提示词：角色 + 聚焦 + 输出契约（JSON 数组）+ diff 数据区。
// diff 来自 diffprep 清洗产物，不依赖沙箱内 git 拓扑（浅克隆场景 two-dot diff 不可靠）。
func buildPrompt(task plugin.Task, hasWorkDir bool) string {
	var b strings.Builder
	b.WriteString("你是资深代码审查专家。请审查以下代码变更，只报告有实际影响的问题" +
		"（正确性缺陷/安全漏洞/并发竞态/资源泄漏），不要报纯风格意见。\n\n")
	if hasWorkDir {
		b.WriteString("变更所在仓库已检出在当前工作目录，如需上下文可查阅仓库文件（只读）。\n\n")
	}
	b.WriteString("输出契约（严格遵守）：只输出一个 JSON 数组，不要任何解释或 markdown 围栏。\n" +
		`[{"file":"文件路径","line":行号,"severity":"high|medium|low","comment":"问题说明"}]` + "\n" +
		"无问题输出 []。\n\n请审查以下 diff：\n```diff\n")
	if task.Diff != nil && task.Diff.Text != "" {
		b.WriteString(task.Diff.Text)
	} else {
		b.WriteString("（diff 为空）")
	}
	b.WriteString("\n```")
	return b.String()
}

// rawFinding agent 输出的 finding JSON 结构。
type rawFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Comment  string `json:"comment"`
}

// parseFindings 从 agent 自由文本中容错提取 findings（ExtractJSON 兼容围栏/前后缀噪声；
// severity 经词表归一化，越界归 low）。
func parseFindings(text string) []plugin.Finding {
	raw := llm.ExtractJSON(text)
	if raw == "" {
		return nil
	}
	var items []rawFinding
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	out := make([]plugin.Finding, 0, len(items))
	for _, it := range items {
		if it.File == "" || it.Comment == "" {
			continue
		}
		sev, _ := severity.Normalize(it.Severity)
		out = append(out, plugin.Finding{
			File: it.File, Line: it.Line, Severity: sev, Comment: it.Comment,
		})
	}
	return out
}
