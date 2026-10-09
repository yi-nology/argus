// review.go —— review/poll 子命令的机器可读面（v3.124.0，对标 Greptile CLI
// 退出码契约与 CodeRabbit --agent NDJSON）：
//
//   - 退出码：0=通过（无达阈值意见）/异步提交成功；1=有达阈值意见（建议阻断）；
//     2=错误（执行失败或用法错误）；4=空 diff 跳过；130=信号中断。
//   - -json：意见列表结构化输出（file/line/severity/note + 原始报告），
//     hook/CI 直接 jq 消费，不再解析 markdown。
//   - -staged/-uncommitted/-include-untracked：CLI 侧组装 diff——git diff 看不到
//     未跟踪文件，pre-commit 场景此前只能靠调用方自己拼（CodeRabbit
//     --include-untracked 同语义）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

// 退出码契约（usage 同源手写，改此须同步）。
const (
	exitClean    = 0 // 通过（无达阈值意见）或异步任务提交成功
	exitFindings = 1 // 有达阈值意见，建议阻断提交
	exitEmpty    = 4 // 无 diff 输入，跳过预审
)

// 输入与截断上限：与 MCP 服务端同源手写（review_diff 120KB 截断、
// review_diff_async 48KB 拒收——service_taskapi.go diffTaskMaxBytes），改此须同步。
const (
	syncDiffCap      = 120_000
	asyncDiffCap     = 48*1024 - 512
	untrackedFileCap = 16 * 1024 // 单个未跟踪文件进 diff 的内容上限
	binarySniffLen   = 512       // 二进制嗅探窗口（与 codesearch 同口径：含 NUL 即弃）
)

// reviewInput review/poll 的机器可读面入参（dispatch 解析 flag 后装配）。
type reviewInput struct {
	DiffFile         string // -f：diff 文件路径（与 -staged/-uncommitted 互斥）
	Staged           bool   // -staged：git diff --staged
	Uncommitted      bool   // -uncommitted：git diff HEAD（含未暂存）
	IncludeUntracked bool   // -include-untracked：追加未跟踪文件（仅配 -staged/-uncommitted）
	Async            bool   // -async：异步提交
	JSONOut          bool   // -json：结构化输出
	FailOn           string // -fail-on：退出码 1 的严重度阈值 high|medium|low|none
}

// cliFinding 单条预审意见（-json 输出形态；与 writing.ReviewDiff 的行格式
// `- file:line [severity] 说明` 一一对应）。
type cliFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Note     string `json:"note"`
}

// reviewResult -json 输出信封（sync/async/poll 共用；零值字段 omitempty）。
type reviewResult struct {
	Mode      string       `json:"mode"`                // sync | async | poll
	Clean     bool         `json:"clean"`               // 无意见（含「✅ 未发现问题」文案漂移免疫：按解析结果计）
	Findings  []cliFinding `json:"findings,omitempty"`  // 解析出的意见
	Report    string       `json:"report,omitempty"`    // 原始报告 markdown
	TaskID    string       `json:"task_id,omitempty"`   // async/poll
	Status    string       `json:"status,omitempty"`    // async/poll
	Verdict   string       `json:"verdict,omitempty"`   // poll（完整审查链路才有）
	Note      string       `json:"note,omitempty"`      // 服务端附注（async 提交回执/stale 说明）
	Truncated bool         `json:"truncated,omitempty"` // CLI 侧为适配上限截断过输入
}

// findingLineRe 意见行解析（writing.ReviewDiff prompt 钉死的行格式；
// (.+) 贪婪到「最后一个 :数字」，兼容路径本身含冒号的形态）。
var findingLineRe = regexp.MustCompile(`(?m)^-\s+(.+):(\d+)\s+\[([A-Za-z]+)\]\s*(.*)$`)

// parseFindings 从报告 markdown 解析意见列表（v3.124.0 退出码与 -json 的底座）。
// 模型输出无法 100% 保证格式——解析结果只用于退出码与结构化面，原始报告恒透传。
func parseFindings(report string) []cliFinding {
	matches := findingLineRe.FindAllStringSubmatch(report, -1)
	out := make([]cliFinding, 0, len(matches))
	for _, m := range matches {
		line := 0
		for _, c := range m[2] {
			if c < '0' || c > '9' {
				line = 0
				break
			}
			line = line*10 + int(c-'0')
		}
		out = append(out, cliFinding{
			File:     strings.TrimSpace(m[1]),
			Line:     line,
			Severity: strings.ToLower(strings.TrimSpace(m[3])),
			Note:     strings.TrimSpace(m[4]),
		})
	}
	return out
}

// failOnRank 阈值严重度权重（与 domain.SeverityRank 同序：high>medium>low）。
// none 恒 0 且单独判断——语义是「永不因意见退出 1」。
func failOnRank(failOn string) int {
	switch failOn {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

// thresholdExit 意见集对阈值的退出码判定。
func thresholdExit(findings []cliFinding, failOn string) int {
	if failOn == "none" {
		return exitClean
	}
	th := failOnRank(failOn)
	if th <= 0 {
		return exitClean
	}
	for _, f := range findings {
		if failOnRank(f.Severity) >= th {
			return exitFindings
		}
	}
	return exitClean
}

// runReview review 子命令执行体：装配 diff → 提交（同步/异步）→ 结构化输出与
// 退出码。返回 (输出, 退出码, 错误)；err 非 nil 时调用方按错误路径退出（2）。
func runReview(ctx context.Context, cli *mcpclient.Client, in reviewInput) (string, int, error) {
	// 输入源互斥：显式源至多一个，缺省 stdin。
	srcs := 0
	for _, ok := range []bool{in.DiffFile != "", in.Staged, in.Uncommitted} {
		if ok {
			srcs++
		}
	}
	if srcs > 1 {
		return "", 0, fmt.Errorf("-f / -staged / -uncommitted 至多选一个输入源")
	}
	if in.IncludeUntracked && srcs == 0 {
		return "", 0, fmt.Errorf("-include-untracked 须配合 -staged 或 -uncommitted（stdin/-f 场景未跟踪文件由调用方自行拼入）")
	}

	cap := syncDiffCap
	if in.Async {
		cap = asyncDiffCap
	}
	data, trunc, err := collectDiff(in, cap)
	if err != nil {
		return "", 0, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "（无 diff 输入，跳过预审）", exitEmpty, nil
	}

	if in.Async {
		out, err := call(ctx, cli, "review_diff_async", map[string]any{"diff": string(data)})
		if err != nil {
			return "", 0, err
		}
		if !in.JSONOut {
			// 异步回执本就是 {task_id,status,note} JSON，文本模式原样透传
			// （v3.123.0 起的行为，保持兼容）。
			return out, exitClean, nil
		}
		var env map[string]any
		_ = json.Unmarshal([]byte(out), &env)
		env["mode"] = "async"
		if trunc {
			env["truncated"] = true
		}
		b, _ := json.MarshalIndent(env, "", "  ")
		return string(b), exitClean, nil
	}

	report, err := call(ctx, cli, "review_diff", map[string]any{"diff": string(data)})
	if err != nil {
		return "", 0, err
	}
	findings := parseFindings(report)
	out := report
	if in.JSONOut {
		res := reviewResult{
			Mode:      "sync",
			Clean:     len(findings) == 0,
			Findings:  findings,
			Report:    report,
			Truncated: trunc,
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		out = string(b)
	}
	return out, thresholdExit(findings, in.FailOn), nil
}

// collectDiff 按 -f > -staged > -uncommitted > stdin 的优先级取 diff，
// -include-untracked 时追加未跟踪文件合成的新文件 diff，最后按上限截断。
func collectDiff(in reviewInput, capBytes int) ([]byte, bool, error) {
	var data []byte
	var err error
	switch {
	case in.DiffFile != "":
		data, err = os.ReadFile(in.DiffFile)
		if err != nil {
			return nil, false, err
		}
	case in.Staged, in.Uncommitted:
		args := []string{"diff", "--staged"}
		if in.Uncommitted {
			args = []string{"diff", "HEAD"}
		}
		data, err = runGit(args...)
		if err != nil {
			return nil, false, err
		}
	default:
		data, err = readAllStdin()
		if err != nil {
			return nil, false, err
		}
	}
	trunc := false
	if in.IncludeUntracked {
		var t bool
		data, t = appendUntracked(data, capBytes)
		trunc = trunc || t
	}
	if len(data) > capBytes {
		warnf("diff 超过 %d 字节上限，已截断（服务端同样只取前段；更大变更请分块提交）", capBytes)
		data = data[:capBytes]
		trunc = true
	}
	return data, trunc, nil
}

// runGit 在当前工作目录跑 git 只读命令（diff/ls-files），失败带 stderr。
func runGit(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s 失败: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// readAllStdin stdin 全量读取（review.go 独立于 main.go，便于单测注入）。
func readAllStdin() ([]byte, error) { return io.ReadAll(os.Stdin) }

// appendUntracked 未跟踪文件（git ls-files --others --exclude-standard，已尊重
// .gitignore）逐个合成 new-file diff 追加到 diff 尾部。二进制文件跳过；单文件
// 内容超 untrackedFileCap 截断（hunk 头行数如实）；总体超 capBytes 即停并报告截断。
func appendUntracked(diff []byte, capBytes int) ([]byte, bool) {
	out, err := runGit("ls-files", "--others", "--exclude-standard")
	if err != nil {
		warnf("跳过未跟踪文件：%v", err)
		return diff, false
	}
	buf := bytes.NewBuffer(diff)
	if buf.Len() > 0 && !bytes.HasSuffix(diff, []byte("\n")) {
		buf.WriteByte('\n')
	}
	trunc := false
	for _, f := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if strings.TrimSpace(f) == "" {
			continue
		}
		content, rerr := os.ReadFile(f)
		if rerr != nil {
			warnf("跳过不可读的未跟踪文件 %s：%v", f, rerr)
			continue
		}
		if isBinary(content) {
			continue
		}
		piece := synthNewFileDiff(f, content)
		if buf.Len()+len(piece) > capBytes {
			warnf("未跟踪文件拼入后超过 %d 字节上限，剩余文件未拼入", capBytes)
			return buf.Bytes(), true
		}
		buf.WriteString(piece)
	}
	return buf.Bytes(), trunc
}

// synthNewFileDiff 单个未跟踪文件 → unified diff 新文件形态（与 git diff 输出
// 同构，服务端 LLM 无差别消费）。
func synthNewFileDiff(path string, content []byte) string {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	kept := 0
	for kept < len(lines) && kept < untrackedFileCap {
		kept++
	}
	if len(lines) > kept {
		lines = append(lines[:kept], "…（文件截断，仅前 "+fmt.Sprint(kept)+" 行进预审）")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	fmt.Fprintf(&b, "+++ b/%s\n", path)
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

// isBinary 二进制嗅探：窗口内含 NUL 即视为二进制（与 codesearch 同口径）。
func isBinary(b []byte) bool {
	if len(b) > binarySniffLen {
		b = b[:binarySniffLen]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// warnf stderr 告警（机器可读输出走 stdout，stderr 恒是人读附注）。
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "argus-copilot: "+format+"\n", args...)
}
