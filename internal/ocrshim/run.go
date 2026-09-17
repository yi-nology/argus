package ocrshim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"git.example.com/enjoydream/agentkit/acpx"
	"git.example.com/enjoydream/agentkit/logredact"
)

// unshallowIfNeeded 浅克隆工作副本补全历史（OCR range 审查的 merge-base 在
// 浅历史下必挂："cannot find merge-base"；完整克隆跳过）。
// workcopy 的凭证嵌在 clone URL 中，git fetch 直接复用；refspec 必须显式全量：
// workcopy 以 `clone --depth 1 --branch <默认分支>` 建库，`--depth` 隐含
// `--single-branch`，裸 `fetch --unshallow` 只补默认分支的远端引用——PR base
// 非默认分支时 `origin/<base>` 不存在，review 必挂（与 ocrdeleg 同款纪律，
// 真件实测复现）。stderr 尾巴进错误链前必须过 logredact（git 传输失败会回显
// 带凭证的 clone URL，此错误经 MCP 原样回传 argus 并可落入报告/日志）。
func unshallowIfNeeded(ctx context.Context, dir string, timeout time.Duration) error {
	if _, err := os.Stat(filepath.Join(dir, ".git", "shallow")); err != nil {
		return nil // 完整克隆（或非 git 目录——交由后续 OCR 执行报错）
	}
	_, stderr, _, err := acpx.RunProcess(ctx, acpx.ProcessRequest{
		Argv:    []string{"git", "-C", dir, "fetch", "--unshallow", "origin", "+refs/heads/*:refs/remotes/origin/*"},
		Timeout: timeout,
	})
	if err != nil {
		// 整条消息脱敏后返回（不只 stderr 尾巴）：%w 链里的 acpx 错误自身也
		// 内嵌 stderr 原文（git 传输失败回显带凭证 clone URL）——此错误经 MCP
		// 原样回传 argus，可落入日志与 PR 评论。无调用方依赖 errors.Is（终端消费）。
		return errors.New(logredact.Redact(fmt.Sprintf(
			"浅克隆补全历史失败（git fetch --unshallow）: %v；stderr: %s", err, tail(stderr, 400))))
	}
	return nil
}

// runOCR 在工作副本目录执行 OCR range 审查，返回 stdout（JSON）。
// Dir 即工作副本（OCR 默认对当前目录做 git 操作）；LLM 凭证经白名单透传，
// 待审仓库工作树内的其他环境变量一律不继承（§8 纪律，与 adapter_cli 同底座）。
func runOCR(ctx context.Context, cfg Config, dir string, p shimPayload) (string, error) {
	argv := []string{
		cfg.OcrBin, "review",
		"--from", "origin/" + p.BaseBranch,
		"--to", "HEAD",
		"--format", "json",
		"--audience", "agent", // json 格式下进度走 stderr，stdout 只留 JSON
	}
	if t := p.Title; t != "" {
		// PR 标题作为业务上下文（--background）；正文攻击者可控且可能超长，不下发
		argv = append(argv, "--background", t)
	}
	stdout, stderr, _, err := acpx.RunProcess(ctx, acpx.ProcessRequest{
		Argv:      argv,
		Dir:       dir,
		Env:       cfg.OcrEnv,
		Timeout:   cfg.Timeout,
		MaxStdout: cfg.MaxStdout,
	})
	if err != nil {
		return "", errors.New(logredact.Redact(fmt.Sprintf(
			"OCR 执行失败: %v；stderr: %s", err, tail(stderr, 400))))
	}
	return stdout, nil
}

// tail 取 stderr 尾部（诊断用，限长防爆）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
