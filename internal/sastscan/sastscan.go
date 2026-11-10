// Package sastscan 确定性 SAST 前置扫描的门面（v3.80.0 实现下沉
// oagentkit/tools/sast——扫描核心与嵌入方 finding 契约解耦；本包保留 dispatch.Outcome
// 契约映射与部署纪律：bin 路径只来自服务级 env、子进程环境白名单）。
//
// 与 cli 插件的区别不变：扫描结果作为独立 Outcome 注入合并，密钥泄漏等确定性
// 结论可直接驱动 R5.2 一票否决；Secret 字段在 SDK 侧即不进 Finding（红线）。
package sastscan

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/oagentkit/tools/sast"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

// Config SAST 工具配置（SDK 形态别名；全部来自服务级 env，默认全空 = 关闭）。
type Config = sast.Config

// sastEnvAllow SAST 子进程环境白名单（按名透传当前进程变量，叠加在 procx
// 基础集之上）——部署纪律留在 argus（§8 供应链：绝不继承 ARGUS_*/OPENAI_*）。
var sastEnvAllow = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// filterRegularFiles 扫描清单的安全过滤（v3.134.1）：剔除软链/非常规文件——
// gitleaks/semgrep 等外部工具会跟随软链读取工作副本外文件（如 -> /etc 或
// /proc/self/environ），泄漏内容会以 finding 形式外带。sast 子进程环境白名单
// 已封密钥 env 面，此为文件面旁路的补齐（与 codesearch/semsearch 的
// IsRegular 守卫同口径）。
func filterRegularFiles(dir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		info, err := os.Lstat(filepath.Join(dir, p))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Run 执行全部已配置工具，产出 dispatch.Outcome（失败置 Err，不 panic 不中断）。
// dir 为工作副本根；baseRef 非空时 gitleaks 按提交范围扫描；paths 为 diffprep
// Included 的聚焦清单（nil/空 = 全仓）。
func Run(ctx context.Context, cfg Config, dir, baseRef string, paths []string, log sast.Logger) []dispatch.Outcome {
	cfg.EnvAllow = sastEnvAllow
	if len(paths) > 0 {
		paths = filterRegularFiles(dir, paths)
	}
	results := sast.Scan(ctx, cfg, dir, baseRef, paths, log)
	if len(results) == 0 {
		return nil
	}
	outcomes := make([]dispatch.Outcome, 0, len(results))
	for _, res := range results {
		o := dispatch.Outcome{Name: res.Tool, Manifest: manifest(res.Tool, cfg.TimeoutResolved())}
		o.Err = res.Err
		if len(res.Findings) > 0 || res.Err == nil {
			findings := make([]plugin.Finding, 0, len(res.Findings))
			for _, f := range res.Findings {
				findings = append(findings, plugin.Finding{
					File: f.File, Line: f.Line, Severity: f.Severity,
					Comment: f.Comment, By: f.Tool,
				})
			}
			o.Result = &plugin.Result{Findings: findings, Notes: res.Notes}
		}
		outcomes = append(outcomes, o)
	}
	return outcomes
}

// manifest SAST outcome 的清单（Kind 置 cli 表意外部工具；Name 供合并/透出）。
func manifest(name string, timeout time.Duration) plugin.AgentManifest {
	return plugin.AgentManifest{
		Name: name, Kind: plugin.KindCLI,
		Cost: plugin.CostFast, Timeout: timeout,
	}
}
