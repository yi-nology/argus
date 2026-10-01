// Package sastscan 确定性 SAST 前置扫描（v3.15.0 P0-3，对标 CodeRabbit 的
// Semgrep/Snyk 集成）：在工作副本上执行 gitleaks / semgrep，产出直接归一为
// plugin.Finding 并入 Merger——全程零 LLM，与"路由/裁决/合并不走 LLM"纪律一致。
//
// 与 cli 插件的区别：cli 插件的 stdout 仍会进 LLM 审查链路；本包是 OCR delegate
// 同款的确定性前置范式——扫描结果作为独立 Outcome 注入合并，密钥泄漏等确定性
// 结论可直接驱动 R5.2 一票否决。
//
// 安全纪律（§8 供应链）：二进制路径只来自服务级 env（部署方信任），仓库配置
// 不可启用/替换；gitleaks 全程 --redact 且 Secret 字段绝不进 Finding（防密钥
// 二次泄漏进 PR 评论）；子进程最小环境，不继承 ARGUS_*/OPENAI_* 密钥。
package sastscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.example.com/enjoydream/agentkit/procx"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/reportutil"
	"git.example.com/enjoydream/agentkit/textutil"

	"argus/internal/plugin"
)

// Config SAST 工具配置（全部来自服务级 env，默认全空 = 功能关闭）。
type Config struct {
	GitleaksBin   string        // ARGUS_SAST_GITLEAKS_BIN（绝对路径）
	SemgrepBin    string        // ARGUS_SAST_SEMGREP_BIN
	SemgrepConfig string        // ARGUS_SAST_SEMGREP_CONFIG（--config 值：路径/registry 名）
	Timeout       time.Duration // ARGUS_SAST_TIMEOUT（0 = 120s）
}

// Enabled 任一工具 bin 已配置即启用。
func (c Config) Enabled() bool { return c.GitleaksBin != "" || c.SemgrepBin != "" }

// TimeoutResolved 生效超时。
func (c Config) TimeoutResolved() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 120 * time.Second
}

// Logger 最小日志口（logx.Logger 满足）。
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
}

// maxFindingsPerTool 单工具 findings 封顶（防全仓扫描刷爆合并层）。
const maxFindingsPerTool = 50

// Run 执行全部已配置工具，每工具产出一个 Outcome（失败置 Err，不 panic 不中断）。
// dir 为工作副本根；baseRef 非空时 gitleaks 按 origin/<base>..HEAD 提交范围扫描
// （工作副本已 deepen 到 merge-base），为空退化为全仓 --no-git 扫描。
// paths 为本次审查实际覆盖的变更文件清单（diffprep Included，v3.21.0 起传给
// semgrep 做聚焦扫描——全仓扫描在大仓上既慢又淹没信噪比；nil/空 = 全仓）。
// 未配置 config 的 semgrep 不产 Outcome（不算覆盖缺口，只告警）。
func Run(ctx context.Context, cfg Config, dir, baseRef string, paths []string, log Logger) []plugin.Outcome {
	if !cfg.Enabled() {
		return nil
	}
	timeout := cfg.TimeoutResolved()
	var outcomes []plugin.Outcome
	if cfg.GitleaksBin != "" {
		outcomes = append(outcomes, runGitleaks(ctx, cfg, dir, baseRef, timeout, log))
	}
	if cfg.SemgrepBin != "" {
		if strings.TrimSpace(cfg.SemgrepConfig) == "" {
			log.Warn("argus.sast.semgrep_no_config",
				"hint", "ARGUS_SAST_SEMGREP_BIN 已配但 ARGUS_SAST_SEMGREP_CONFIG 为空，semgrep 跳过")
		} else {
			outcomes = append(outcomes, runSemgrep(ctx, cfg, dir, paths, timeout, log))
		}
	}
	return outcomes
}

// runGitleaks gitleaks detect（JSON 报告落临时文件后解析）。
func runGitleaks(ctx context.Context, cfg Config, dir, baseRef string,
	timeout time.Duration, log Logger) plugin.Outcome {

	o := plugin.Outcome{Name: "gitleaks", Manifest: manifest("gitleaks", timeout)}
	o.Manifest.Kind = plugin.KindCLI

	tmp, err := os.CreateTemp("", "argus-gitleaks-*.json")
	if err != nil {
		o.Err = fmt.Errorf("创建报告临时文件失败: %w", err)
		return o
	}
	reportPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(reportPath)

	logOpts := ""
	if baseRef != "" {
		logOpts = "origin/" + baseRef + "..HEAD"
	}
	args := gitleaksArgs(dir, logOpts)
	args = append(args, "--report-path", reportPath)
	// v0.10.14 深度结合：进程纪律单源 procx（进程组托管/超时宽限/输出上限，
	// 取代手写 combinedOutput + timer kill）
	stdout, stderr, code, err := procx.Run(ctx, procx.RunRequest{
		Argv:    append([]string{cfg.GitleaksBin}, args...),
		Dir:     dir,
		Env:     sastEnvAllow,
		Timeout: timeout,
	})
	if err != nil {
		o.Err = fmt.Errorf("gitleaks 执行失败: %v: %s", err, truncLine(stderr, 300))
		return o
	}
	if code != 0 {
		o.Err = fmt.Errorf("gitleaks 退出码 %d: %s", code, truncLine(stdout+"\n"+stderr, 300))
		return o
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		o.Err = fmt.Errorf("读取 gitleaks 报告失败: %w", err)
		return o
	}
	var entries []struct {
		RuleID      string `json:"RuleID"`
		Description string `json:"Description"`
		File        string `json:"File"`
		StartLine   int    `json:"StartLine"`
		// Secret / Commit 刻意不消费：密钥原文绝不进 Finding（--redact 之外的
		// 第二道防线——PR 评论是比日志更广的分发面）。
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		o.Err = fmt.Errorf("gitleaks 报告解析失败: %w", err)
		return o
	}
	o.Result = &plugin.Result{Findings: []plugin.Finding{}}
	for i, e := range entries {
		if i >= maxFindingsPerTool {
			o.Result.Notes = fmt.Sprintf("gitleaks 产出超 %d 条已截断", maxFindingsPerTool)
			break
		}
		o.Result.Findings = append(o.Result.Findings, plugin.Finding{
			File: e.File, Line: e.StartLine, Severity: reportutil.High, By: "gitleaks",
			Comment: fmt.Sprintf("疑似硬编码敏感信息（gitleaks 规则 %s）：请将凭证移出源码并轮换已泄漏密钥。",
				e.RuleID),
		})
	}
	log.Info("argus.sast.gitleaks_done", "findings", len(o.Result.Findings))
	return o
}

// gitleaksArgs 命令行构造（单测直接断言）。
func gitleaksArgs(dir, logOpts string) []string {
	args := []string{"detect", "--source", dir, "--no-git",
		"--report-format", "json", "--redact", "--exit-code", "0"}
	if logOpts != "" {
		// 有 merge-base 的工作副本走提交范围扫描（diff 聚焦）；--no-git 与
		// --log-opts 互斥，真机 gitleaks 以 --log-opts 优先，此处按范围扫描组装
		args = []string{"detect", "--source", dir,
			"--report-format", "json", "--redact", "--exit-code", "0",
			"--log-opts", logOpts}
	}
	return args
}

// semgrepMaxPaths 聚焦扫描的文件数上限（防巨型 PR 撑爆 argv；超出回退全仓——
// 变更面已经足够大，聚焦收益归零）。
const semgrepMaxPaths = 100

// runSemgrep semgrep scan --json（dir 为扫描根；paths 非空时聚焦变更文件）。
func runSemgrep(ctx context.Context, cfg Config, dir string, paths []string,
	timeout time.Duration, log Logger) plugin.Outcome {

	o := plugin.Outcome{Name: "semgrep", Manifest: manifest("semgrep", timeout)}
	o.Manifest.Kind = plugin.KindCLI
	args := []string{"scan", "--json", "--quiet", "--config", cfg.SemgrepConfig}
	if focused := semgrepPaths(paths); len(focused) > 0 {
		args = append(args, focused...)
	} else {
		args = append(args, ".")
	}
	stdout, stderr, code, err := procx.Run(ctx, procx.RunRequest{
		Argv:    append([]string{cfg.SemgrepBin}, args...),
		Dir:     dir,
		Env:     sastEnvAllow,
		Timeout: timeout,
	})
	if err != nil || code != 0 {
		o.Err = fmt.Errorf("semgrep 执行失败: exit=%d err=%v: %s", code, err, truncLine(stdout+"\n"+stderr, 300))
		return o
	}
	var parsed struct {
		Results []struct {
			CheckID string `json:"check_id"`
			Path    string `json:"path"`
			Start   struct {
				Line int `json:"line"`
			} `json:"start"`
			Extra struct {
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"extra"`
		} `json:"results"`
		Errors []any `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		o.Err = fmt.Errorf("semgrep 输出解析失败: %w", err)
		return o
	}
	o.Result = &plugin.Result{Findings: []plugin.Finding{}}
	for i, r := range parsed.Results {
		if i >= maxFindingsPerTool {
			o.Result.Notes = fmt.Sprintf("semgrep 产出超 %d 条已截断", maxFindingsPerTool)
			break
		}
		o.Result.Findings = append(o.Result.Findings, plugin.Finding{
			File: r.Path, Line: r.Start.Line,
			Severity: semgrepSeverity(r.Extra.Severity), By: "semgrep",
			Comment: fmt.Sprintf("静态分析（semgrep %s）：%s", r.CheckID, r.Extra.Message),
		})
	}
	log.Info("argus.sast.semgrep_done", "findings", len(o.Result.Findings), "errors", len(parsed.Errors))
	return o
}

// semgrepPaths 聚焦文件清单整形（存在性不做校验——以 diff 清单为准，缺失文件
// semgrep 自行忽略；>semgrepMaxPaths 回退 nil = 全仓）。
func semgrepPaths(paths []string) []string {
	if len(paths) == 0 || len(paths) > semgrepMaxPaths {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] || strings.Contains(p, "..") { // 防路径穿越出工作副本
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// semgrepSeverity semgrep 三档 → argus 词表。
func semgrepSeverity(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "ERROR":
		return reportutil.High
	case "WARNING":
		return reportutil.Medium
	default:
		return reportutil.Low
	}
}

// manifest SAST outcome 的清单（Kind 置 cli 表意外部工具；Name 供合并/透出）。
func manifest(name string, timeout time.Duration) plugin.AgentManifest {
	return plugin.AgentManifest{
		Name: name, Kind: plugin.KindCLI,
		Cost: plugin.CostFast, Timeout: timeout,
	}
}

// minimalEnv cli 子进程最小环境（与插件 cli 同纪律：绝不继承密钥）。
// sastEnvAllow SAST 子进程环境白名单（按名透传当前进程变量，叠加在 procx
// 基础集之上）——取代手写 minimalEnv 拼装。
var sastEnvAllow = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// truncLine 错误输出截断（进 Err 链，最终上报告警）。
// v3.76.0 收敛 textutil.TruncEllipsis：旧实现按字节切片会腰斩多字节 UTF-8，
// 错误链进入 LLM prompt 可能 400。
func truncLine(s string, n int) string {
	return textutil.TruncEllipsis(strings.TrimSpace(s), n)
}
