package ocrshim

import (
	"context"
	"errors"
	"fmt"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/runtime/procx"
)

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
	rr, err := procx.RunDetailed(ctx, procx.RunRequest{
		Argv:      argv,
		Dir:       dir,
		Env:       cfg.OcrEnv,
		Timeout:   cfg.Timeout,
		MaxStdout: cfg.MaxStdout,
	})
	if err != nil {
		return "", errors.New(logredact.Redact(fmt.Sprintf(
			"OCR 执行失败: %v；stderr: %s", err, tail(rr.Stderr, 400))))
	}
	// 截断事实纳入守门（procx.RunResult，procx#92）：stdout 是 findings JSON，
	// 采集截断即 JSON 不完整——容错解析会把半截数组修成「少了几条」的假完整，
	// 宁可失败上抛（argus 侧 Merger 部分失败容忍），不出假结果。
	// stderr 是 --audience agent 模式下的进度流（不进解析），其截断不影响结论。
	if rr.StdoutTruncated {
		return "", errors.New("OCR 输出超过采集上限被截断，拒绝解析不完整 JSON")
	}
	return rr.Stdout, nil
}

// tail 取 stderr 尾部（诊断用，限长防爆）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
