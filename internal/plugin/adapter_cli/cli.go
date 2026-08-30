// Package adapter_cli cli 形态适配器（design §3.2）：包装外部命令行（首个实战对象是
// alibaba/open-code-review）。子进程管理照抄 review-service 范式：
// Setpgid 建进程组 → 超时/取消先向 -pid 发 SIGTERM → 3s grace 后 SIGKILL 杀整组防孤儿。
// 供应链信任（§8）：cmd 只能来自 Hub plugins.yaml 白名单，仓库配置无权声明（§3.4 红线 2）。
package adapter_cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	argusctx "argus/internal/context"
	"argus/internal/plugin"
)

// CLIPlugin 通用 cli 插件。
type CLIPlugin struct {
	manifest plugin.AgentManifest
	Cmd      []string
	Mode     string // full | delegate（delegate v3.0 留接口，§2）
	wc       *argusctx.Workcopy
	log      logx.Logger
}

// New 构造 cli 插件。
func New(m plugin.AgentManifest, cmd []string, mode string, wc *argusctx.Workcopy, log logx.Logger) *CLIPlugin {
	if m.Timeout <= 0 {
		m.Timeout = 300 * time.Second
	}
	return &CLIPlugin{manifest: m, Cmd: cmd, Mode: mode, wc: wc, log: log}
}

func (p *CLIPlugin) Manifest() plugin.AgentManifest { return p.manifest }

// Invoke 执行链：工作副本就绪 → exec 白名单命令 → 解析 JSON → Result。
func (p *CLIPlugin) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	if strings.EqualFold(p.Mode, "delegate") {
		// delegate 子模式留接口（§2）：OCR 只出文件筛选+规则，审查循环由 Argus builtin 执行。
		// 生产参考实现：review-service internal/ocr/delegate.go，v3.1 照抄其范式实装。
		return plugin.Result{}, fmt.Errorf("cli 插件 delegate 模式 v3.0 留接口，v3.1 实装")
	}
	if p.wc == nil {
		return plugin.Result{}, fmt.Errorf("cli 插件未接入工作副本服务")
	}
	dir, err := p.wc.Ensure(ctx, task.PR)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("准备工作副本失败: %w", err)
	}
	defer p.wc.Release(task.PR)

	stdout, err := runProcessGroup(ctx, p.Cmd, dir, p.manifest.Timeout)
	if err != nil {
		return plugin.Result{}, err
	}
	findings, err := ParseFindings(stdout)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("解析 cli JSON 输出失败: %w", err)
	}
	for i := range findings {
		findings[i].By = p.manifest.Name
	}
	return plugin.Result{Findings: findings}, nil
}

// runProcessGroup 进程组执行：Setpgid + ctx 取消三段式（TERM → 3s → KILL，均作用于 -pid 整组）。
func runProcessGroup(ctx context.Context, cmdLine []string, dir string, timeout time.Duration) (string, error) {
	if len(cmdLine) == 0 {
		return "", fmt.Errorf("cli 命令为空")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, cmdLine[0], cmdLine[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 %s 失败: %w", cmdLine[0], err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-cctx.Done(): // 超时/取消：TERM 整组 → grace 后 KILL 整组
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				time.Sleep(3 * time.Second)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	if waitErr != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // 兜底清组
		tail := stderr.String()
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		if cctx.Err() != nil {
			return "", fmt.Errorf("cli 执行超时（%s）", cmdLine[0])
		}
		return "", fmt.Errorf("cli 执行失败（%s）: %v: %s", cmdLine[0], waitErr, strings.TrimSpace(tail))
	}
	return stdout.String(), nil
}

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
		sev, ok := plugin.NormalizeSeverity(rawSev)
		if !ok {
			sev = plugin.SeverityLow // 越界归 low（Merger 亦会兜底告警）
		}
		if comment == "" {
			continue
		}
		out = append(out, plugin.Finding{File: file, Line: line, Severity: sev, Comment: comment})
	}
	if len(out) == 0 {
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
