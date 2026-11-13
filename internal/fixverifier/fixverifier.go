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
	"strings"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/runtime/procx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// Config 修复验证配置。
type Config struct {
	Cmd     string        // 包装命令模板（{dir} 占位符）；空 = 功能整体停用
	Timeout time.Duration // 0 = 3m
}

// Result 单条 finding 的验证结果。
type Result struct {
	Finding domain.Finding
	Applied bool   // patch 是否成功写入文件（定位失败/文件缺失 = false）
	OK      bool   // 包装命令通过
	Skipped string // 非空 = 未验证原因（无修复建议/文件不在副本/配置缺失）
	// Truncated 采集层截断（procx.RunResult 语义为准，procx#92）：stdout/stderr
	// 超采集上限、有内容被丢。Output 本身按契约只保留尾部（尾窗裁剪是展示口径，
	// 不是截断信号）。
	Truncated bool
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

// resolveSafePath 工作副本内的 symlink 安全路径解析（guardx/sandbox.FSFence
// 单源收编；原手写三段式「文本前缀判界 + EvalSymlinks 归一 + 二次包含判定」
// 删去）。威胁面：仓库可携带指向沙箱外的 symlink（恶意 PR 提交 link ->
// /etc/cron.d），os.WriteFile 跟随 symlink 即以 bot 权限写出沙箱。
// FSFence 与手写语义同向且更强（guardx#117）：构造期 root EvalSymlinks 归一
// （macOS /var 陷阱）、显式 ".." 一律拒判（文本抵消看不见符号链接）、目标缺失
// 降级最深已存在祖先 + 尾段悬空软链拒判；越界/断链/非法一律拒绝（fail-closed，
// errors.Is 可对表 sandbox.ErrFenceOutside / ErrFenceInvalid）。
// 与旧实现的语义差：目标不存在不再于解析期拒绝（写新文件是围栏的合法用例），
// 由调用方读写步骤自然失败（「文件不在工作副本」）。
// 返回值是**落地路径**（symlink 已解析），实际读写必须用它（FSFence 的 TOCTOU
// 残窗缓解纪律）。
func resolveSafePath(dir, rel string) (string, error) {
	fence, err := sandbox.NewFSFence(dir)
	if err != nil {
		return "", fmt.Errorf("副本根解析失败: %w", err)
	}
	return fence.Resolve(rel)
}

// Verify 对 findings 中带 SuggestedFix 的条目做应用→验证→还原。
// dir 为工作副本根；每条验证独立进行（前一条的文件修改已还原，互不污染）。
func Verify(ctx context.Context, cfg Config, dir string, findings []domain.Finding) []Result {
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
func verifyOne(ctx context.Context, cfg Config, dir string, f domain.Finding) (res Result) {
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
		if rerr := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); rerr != nil {
			// best-effort 恢复失败留痕（v5.4.3 先例）：工作副本将残留验证改动，
			// 消费方从 Output 尾注感知——不再静默吞错。
			res.Output += "\n[fixverifier] 恢复原文件失败（工作副本残留验证改动）: " + rerr.Error()
		}
	}()

	cmd := plugin.ResolveShellTemplate(cfg.Cmd, dir)
	runCtx, cancel := context.WithTimeout(ctx, cfg.timeoutResolved())
	defer cancel()
	// RunDetailed 拿结构化截断事实（procx#92）：Truncated 以 RunResult 为准，
	// 不再用「尾窗是否填满」猜（尾窗裁剪是 Output 的展示口径，与采集上限是两回事）。
	rr, rerr := procx.RunDetailed(runCtx, procx.RunRequest{
		Argv:      []string{"sh", "-c", cmd},
		Dir:       dir,
		Timeout:   cfg.timeoutResolved(),
		MaxStdout: outputTailBytes * 4,
	})
	out := rr.Stdout
	if rr.Stderr != "" {
		out += "\n" + rr.Stderr
	}
	if len(out) > outputTailBytes {
		out = out[len(out)-outputTailBytes:]
	}
	res = Result{Finding: f, Applied: true, Output: out, Truncated: rr.Truncated()}
	if rerr != nil {
		res.Output = out + "\n" + rerr.Error()
		return res
	}
	res.OK = rr.ExitCode == 0
	return res
}

// Apply 把已验证通过的修复应用进工作副本（不还原——push 环专用，Verify 的
// 每条会还原，两者不可混用于同一批文件）。返回实际写入的文件集合。
func Apply(_ context.Context, dir string, findings []domain.Finding) (map[string]bool, error) {
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
