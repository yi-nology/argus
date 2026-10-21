// fix_sandbox.go —— @argus fix 的修复执行沙箱（v3.167.0 自 fix.go 拆出；零逻辑变更）：
// 本地克隆沙箱 → agent（workspace-write）→ diff 采集 → 确定性验证 → 陈旧沙箱清扫。
// 隔离语义由 ARGUS_TESTGEN_VERIFY_CMD 同族的部署方包装声明承担（§8 纪律）。
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oagentkit/runtime/procx"
	"github.com/oagentkit/tools/acpx"
	"github.com/oekit/kitx/uidgen"
)

// diff → 确定性验证。任何一步失败整体报错（回帖有感知），不留半截沙箱。
func runFixSandbox(ctx context.Context, cfg fixRunConfig) (*fixResult, error) {
	reg := cfg.Registry
	if reg == nil {
		reg = acpx.NewRegistry()
	}
	agent, ok := reg.Get(cfg.AgentName)
	if !ok {
		return nil, fmt.Errorf("未知 agent %q（可用：%s）", cfg.AgentName, strings.Join(reg.Names(), ","))
	}
	// 沙箱能力闸：fixer 需要 workspace-write；kimi/mimo 等无沙箱旗标的 agent
	// 显式拒绝（fail-closed），绝不裸跑可写 agent。
	if bad := acpx.UnsupportedFields(agent, acpx.RunRequest{Sandbox: acpx.SandboxWorkspace}); len(bad) > 0 {
		return nil, fmt.Errorf("agent %s 不支持沙箱档位（%s）——fixer 需要 workspace-write 能力（codex/claude/gemini 族）",
			cfg.AgentName, strings.Join(bad, ","))
	}
	if cfg.SrcDir == "" {
		return nil, fmt.Errorf("工作副本已回收或不可用，请稍后重发（重新审查会自动重建工作副本）")
	}
	if err := safeRelPath(cfg.Finding.File); err != nil {
		return nil, err
	}

	fixDir, cleanup, err := cloneFixSandbox(ctx, cfg.SrcDir, cfg.WorkRoot)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	began := time.Now()
	runRes, err := agent.Run(ctx, acpx.RunRequest{
		Prompt:  buildFixPrompt(cfg.Finding, fixDir),
		WorkDir: fixDir,
		Sandbox: acpx.SandboxWorkspace,
		Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("agent 执行失败: %w", err)
	}
	env := parseFixEnvelope(runRes.Text)

	stat, diff, trunc, err := gitDiffCapped(ctx, fixDir)
	if err != nil {
		return nil, fmt.Errorf("读取修复 diff 失败: %w", err)
	}

	verified, vout := false, ""
	if cfg.Verify.Cmd != "" {
		verified, vout = runVerifyCmd(ctx, cfg.Verify, fixDir)
	}
	return &fixResult{
		AgentName: cfg.AgentName,
		Took:      time.Since(began).Round(time.Second),
		Fixed:     env.Fixed,
		Summary:   env.Summary,
		Files:     env.Files,
		DiffStat:  stat,
		Diff:      diff,
		DiffTrunc: trunc,
		Verified:  verified,
		VerifyOut: vout,
	}, nil
}

// cloneFixSandbox 从工作副本本地克隆出独立修复沙箱（git clone --local 走
// 硬链接 objects、工作树全新检出，源目录零写入）。返回沙箱目录与清理函数。
func cloneFixSandbox(ctx context.Context, src, workRoot string) (string, func(), error) {
	sweepStaleFixSandboxes(workRoot)
	if workRoot == "" {
		workRoot = os.TempDir()
	}
	dst := filepath.Join(workRoot, "fix-"+uidgen.ShortUUID())
	if _, _, code, err := procx.Run(ctx, procx.RunRequest{
		Argv:      []string{"git", "clone", "--quiet", "--local", src, dst},
		Timeout:   2 * time.Minute,
		MaxStdout: 1 << 20,
	}); err != nil || code != 0 {
		return "", nil, fmt.Errorf("修复沙箱克隆失败（exit=%d）: %v", code, err)
	}
	return dst, func() { _ = os.RemoveAll(dst) }, nil
}

// sweepStaleFixSandboxes 回收孤儿沙箱（v3.91.1）：进程中途死亡时 defer 清理
// 不会执行，fix-* 目录会遗留——每次新建前顺手清掉 2h 以上的旧目录。
func sweepStaleFixSandboxes(workRoot string) {
	if workRoot == "" {
		return
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-2 * time.Hour)
	for _, en := range entries {
		if !en.IsDir() || !strings.HasPrefix(en.Name(), "fix-") {
			continue
		}
		if info, err := en.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(workRoot, en.Name()))
		}
	}
}
