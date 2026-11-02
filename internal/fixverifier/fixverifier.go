// Package fixverifier 修复验证环（v3.74.0，agent loop 修复闭环第一环）：
// 对审查意见的 suggested_fix（单行替换语义，与 GitHub suggestion 一键应用一致）
// 在工作副本上真实应用 → 调部署方声明的隔离包装命令（编译/测试）→ 还原文件。
//
// 沙箱纪律与 service/sandbox_verify 同构（§8）：argus 自身绝不直接执行待审
// 代码——隔离责任在包装命令侧（含 {dir} 占位符）；未配置包装命令时整段功能
// 不存在，findings 保持「未经编译验证」状态（零行为变更）。
//
// 验证结论只做渲染标注（✅ 沙箱验证通过），不改变 findings 的指纹身份与
// verdict 语义——验证失败≠意见错误（可能只是 patch 上下文漂移），仅不加分。
package fixverifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oagentkit/runtime/procx"

	"argus/internal/plugin"
)

// Config 修复验证配置。
type Config struct {
	Cmd     string        // 包装命令模板（{dir} 占位符）；空 = 功能整体停用
	Timeout time.Duration // 0 = 3m
}

// Result 单条 finding 的验证结果。
type Result struct {
	Finding   plugin.Finding
	Applied   bool   // patch 是否成功写入文件（定位失败/文件缺失 = false）
	OK        bool   // 包装命令通过
	Skipped   string // 非空 = 未验证原因（无修复建议/文件不在副本/配置缺失）
	Truncated bool   // 输出被截断
	Output    string // 包装命令输出尾部（失败时可诊断）
}

// timeoutResolved 生效超时。
func (c Config) timeoutResolved() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 3 * time.Minute
}

// outputTailBytes 包装命令输出保留尾部（诊断信息在尾部）。
const outputTailBytes = 4000

// resolveSafePath 工作副本内的 symlink 安全路径解析：EvalSymlinks 归一后必须
// 仍落在 dir 内——仓库可携带指向沙箱外的 symlink（恶意 PR 提交 link ->
// /etc/cron.d），os.WriteFile 跟随 symlink 即以 bot 权限写出沙箱。解析失败
// （含断链）一律拒绝。
func resolveSafePath(dir, rel string) (string, error) {
	joined := filepath.Join(dir, filepath.FromSlash(rel))
	if !strings.HasPrefix(joined, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界: %s", rel)
	}
	// dir 本身也可能经由符号链接到达（macOS /var、容器挂载路径），归一后比较，
	// 否则合法文件被误判逃逸。
	root, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("副本根解析失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("路径解析失败: %w", err)
	}
	if !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("symlink 逃逸: %s", rel)
	}
	return resolved, nil
}

// Verify 对 findings 中带 SuggestedFix 的条目做应用→验证→还原。
// dir 为工作副本根；每条验证独立进行（前一条的文件修改已还原，互不污染）。
func Verify(ctx context.Context, cfg Config, dir string, findings []plugin.Finding) []Result {
	results := make([]Result, 0, len(findings))
	if cfg.Cmd == "" {
		for _, f := range findings {
			results = append(results, Result{Finding: f, Skipped: "未配置验证包装命令"})
		}
		return results
	}
	for _, f := range findings {
		if strings.TrimSpace(f.SuggestedFix) == "" {
			results = append(results, Result{Finding: f, Skipped: "无修复建议"})
			continue
		}
		res := verifyOne(ctx, cfg, dir, f)
		results = append(results, res)
	}
	return results
}

// verifyOne 单条验证：读文件 → 锚定行替换为建议 → 跑包装命令 → 还原。
func verifyOne(ctx context.Context, cfg Config, dir string, f plugin.Finding) Result {
	path, perr := resolveSafePath(dir, f.File)
	if perr != nil {
		return Result{Finding: f, Skipped: perr.Error()}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{Finding: f, Skipped: "文件不在工作副本: " + f.File}
	}
	lines := strings.Split(string(raw), "\n")
	if f.Line < 1 || f.Line > len(lines) {
		return Result{Finding: f, Skipped: fmt.Sprintf("锚定行越界 (%d/%d)", f.Line, len(lines))}
	}
	orig := lines[f.Line-1]
	lines[f.Line-1] = strings.TrimRight(f.SuggestedFix, "\n")
	if werr := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); werr != nil {
		return Result{Finding: f, Skipped: "写入失败: " + werr.Error()}
	}
	defer func() {
		lines[f.Line-1] = orig
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}()

	cmd := plugin.ResolveShellTemplate(cfg.Cmd, dir)
	runCtx, cancel := context.WithTimeout(ctx, cfg.timeoutResolved())
	defer cancel()
	stdout, stderr, code, rerr := procx.Run(runCtx, procx.RunRequest{
		Argv:      []string{"sh", "-c", cmd},
		Dir:       dir,
		Timeout:   cfg.timeoutResolved(),
		MaxStdout: outputTailBytes * 4,
	})
	out := stdout
	if stderr != "" {
		out += "\n" + stderr
	}
	if len(out) > outputTailBytes {
		out = out[len(out)-outputTailBytes:]
	}
	res := Result{Finding: f, Applied: true, Output: out, Truncated: len(out) >= outputTailBytes}
	if rerr != nil {
		res.Output = out + "\n" + rerr.Error()
		return res
	}
	res.OK = code == 0
	return res
}

// Apply 把已验证通过的修复应用进工作副本（不还原——push 环专用，Verify 的
// 每条会还原，两者不可混用于同一批文件）。返回实际写入的文件集合。
func Apply(_ context.Context, dir string, findings []plugin.Finding) (map[string]bool, error) {
	written := map[string]bool{}
	for _, f := range findings {
		if !f.Verified || strings.TrimSpace(f.SuggestedFix) == "" {
			continue
		}
		path, perr := resolveSafePath(dir, f.File)
		if perr != nil {
			return written, perr
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return written, fmt.Errorf("读取 %s 失败: %w", f.File, err)
		}
		lines := strings.Split(string(raw), "\n")
		if f.Line < 1 || f.Line > len(lines) {
			continue // 锚定行漂移（作者已改），该条放弃
		}
		lines[f.Line-1] = strings.TrimRight(f.SuggestedFix, "\n")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return written, fmt.Errorf("写入 %s 失败: %w", f.File, err)
		}
		written[f.File] = true
	}
	return written, nil
}
