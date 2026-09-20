// sandbox_verify.go —— testgen 产物的隔离执行验证（v3.22.0）。
//
// 沙箱纪律（§8）：argus 自身绝不直接执行待审代码——本文件只做「产物整形 +
// 工作副本写入 + 调用部署方声明的隔离包装命令」；包装命令模板含 {dir} 占位符
// （如 `docker run --rm -v {dir}:/w -w /w golang:1.24 go test ./...`），
// 网络/文件系统/资源限制等隔离责任全部在包装命令侧，与 cli 插件供应链同构。
// 未配置包装命令时整段功能不存在（回帖保持「未经编译验证」声明）。
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.example.com/enjoydream/agentkit/logredact"
	"git.example.com/enjoydream/agentkit/procx"

	"argus/internal/plugin"
)

// VerifyConfig 包装命令配置（来自服务级 env）。
type VerifyConfig struct {
	Cmd     string        // 模板，{dir} 为工作副本根占位符
	Timeout time.Duration // 0 = 5m
}

// VerifyResult 单个生成文件的验证结果。
type VerifyResult struct {
	Path    string
	Skipped string // 非空 = 未写入/未执行的原因（已存在同名/非法路径）
	OK      bool
	Output  string // 包装命令输出（截断；失败时回帖可见）
}

// verifyTimeoutResolved 生效超时。
func (c VerifyConfig) verifyTimeoutResolved() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 5 * time.Minute
}

// extractGoTestFiles 从 testgen 产物提取「### <路径> + ```go 代码块」。
// 安全整形：仅收 _test.go 结尾、不含 ".." 的相对路径；同名键去重后取首块。
// 非测试文件（LLM 偶发把实现代码写成普通 .go）一律拒收——验证只写测试文件。
func extractGoTestFiles(generated string) map[string]string {
	files := map[string]string{}
	lines := strings.Split(generated, "\n")
	var curPath string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "### ") {
			continue
		}
		curPath = sanitizeTestPath(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
		// 向下找首个 ```go 开栏，再收集到闭栏
		for ; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(trimmed, "```") {
				continue
			}
			if trimmed != "```go" {
				break // 非 go 代码块，跳过该栏
			}
			var b strings.Builder
			i++
			for ; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "```" {
					break
				}
				b.WriteString(lines[i] + "\n")
			}
			if curPath != "" {
				if _, dup := files[curPath]; !dup {
					files[curPath] = strings.TrimRight(b.String(), "\n")
				}
			}
			break
		}
	}
	return files
}

// sanitizeTestPath 测试文件路径整形（非法返回空）。
func sanitizeTestPath(p string) string {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "- "), "*")
	p = strings.Trim(p, "`\"' ")
	if p == "" || !strings.HasSuffix(p, "_test.go") {
		return ""
	}
	if strings.Contains(p, "..") || strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return ""
	}
	if strings.ContainsAny(p, "\\\n\t") {
		return ""
	}
	return filepath.ToSlash(p)
}

// VerifyGenerated 把生成文件写入工作副本并调用隔离包装命令。
// 纪律：不覆盖已存在的同名文件（不碰用户代码）；argus 不解释包装命令内容，
// 只做 {dir} 替换与 shell 执行——隔离语义由部署方声明。
func VerifyGenerated(ctx context.Context, cfg VerifyConfig, dir string,
	files map[string]string) []VerifyResult {

	if len(files) == 0 || strings.TrimSpace(cfg.Cmd) == "" {
		return nil
	}
	var results []VerifyResult
	written := 0
	for rel, code := range files {
		abs := filepath.Join(dir, rel)
		if _, err := os.Stat(abs); err == nil {
			results = append(results, VerifyResult{Path: rel,
				Skipped: "工作副本已存在同名文件，未覆盖"})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			results = append(results, VerifyResult{Path: rel,
				Skipped: "目录创建失败: " + logredact.Redact(err.Error())})
			continue
		}
		if err := os.WriteFile(abs, []byte(code), 0o644); err != nil {
			results = append(results, VerifyResult{Path: rel,
				Skipped: "写入失败: " + logredact.Redact(err.Error())})
			continue
		}
		written++
		results = append(results, VerifyResult{Path: rel, OK: true})
	}
	if written == 0 {
		return results
	}
	// 一次包装命令验证全部写入文件（go test 按包编译，逐文件跑会重复编译）。
	ok, output := runVerifyCmd(ctx, cfg, dir)
	for i := range results {
		if results[i].Skipped == "" {
			results[i].OK = ok
			if !ok {
				results[i].Output = truncVerify(output)
			}
		}
	}
	// 失败：清理写入的测试文件（工作副本保持只读语义——不留 argus 产物）。
	if !ok {
		for rel, code := range files {
			_ = os.Remove(filepath.Join(dir, rel))
			_ = code
		}
	}
	return results
}

// runVerifyCmd 执行 {dir} 替换后的包装命令（shell -c，与 cli 插件 argv 白名单
// 不同：模板本身就是部署方声明的一段 shell，argus 只注入工作副本路径）。
// v0.10.14 深度结合：进程纪律单源 procx（进程组托管/超时宽限/输出上限）。
func runVerifyCmd(ctx context.Context, cfg VerifyConfig, dir string) (bool, string) {
	cmdline := strings.ReplaceAll(cfg.Cmd, "{dir}", dir)
	stdout, stderr, code, err := procx.Run(ctx, procx.RunRequest{
		Argv:    []string{"sh", "-c", cmdline},
		Dir:     dir,
		Timeout: cfg.verifyTimeoutResolved(),
	})
	combined := strings.TrimSpace(stdout)
	if stderr != "" {
		combined = strings.TrimSpace(combined + "\n" + stderr)
	}
	switch {
	case err != nil:
		return false, combined + "\n" + err.Error()
	case code != 0:
		return false, combined + fmt.Sprintf("\nexit status %d", code)
	}
	return true, combined
}

// truncVerify 失败输出截断（回帖可见，防刷屏）。
func truncVerify(s string) string {
	s = strings.TrimSpace(s)
	const max = 1500
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…（截断）"
}

// renderVerifySection 验证结果段（含数据区纪律；空 = 全部成功也要透出结论）。
func renderVerifySection(results []VerifyResult) string {
	var b strings.Builder
	allOK := true
	for _, r := range results {
		switch {
		case r.Skipped != "":
			allOK = false
			fmt.Fprintf(&b, "- ⏭️ %s：%s\n", r.Path, r.Skipped)
		case r.OK:
			fmt.Fprintf(&b, "- ✅ %s：沙箱编译与测试通过\n", r.Path)
		default:
			allOK = false
			fmt.Fprintf(&b, "- ❌ %s：沙箱验证失败\n\n```\n%s\n```\n", r.Path, r.Output)
		}
	}
	header := "## 🧪 沙箱验证结果（隔离执行环境）\n\n"
	if allOK {
		header = "## 🧪 沙箱验证结果：全部通过\n\n"
		b.Reset()
		for _, r := range results {
			fmt.Fprintf(&b, "- ✅ %s\n", r.Path)
		}
		return plugin.DataFence("沙箱验证结论（确定性执行结果）", header+b.String())
	}
	return plugin.DataFence("沙箱验证结论（确定性执行结果）", header+b.String()+
		"\n以上为隔离环境中的确定性执行结果；失败代码块保留供人工修正。")
}
