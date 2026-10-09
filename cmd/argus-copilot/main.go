// argus-copilot —— Argus 副驾参考客户端（v3.22.0，批次 4 条件项的降档落地）。
//
// 以 MCP 客户端消费 argus /mcp 的副驾工具（get_task_stages / list_findings /
// get_suggestion / ask_argus），为 IDE 插件与脚本集成提供可运行的参考实现：
//
//	argus-copilot -url http://argus:8899/mcp -token $TOKEN stages  -task <id>
//	argus-copilot -url … -token … findings -task <id>
//	argus-copilot -url … -token … suggest -task <id> -fp ab12cd34
//	argus-copilot -url … -token … ask     -pr <platform/owner/repo#num> [-q 问题]
//
// 只读：客户端不改动任何代码——「一键回写」由使用者对 get_suggestion 返回的
// 内容自行执行。GUI 形态（VS Code 插件等）另立项，本 CLI 即集成参考与联调工具。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// 退出码契约（v3.124.0，review/poll 生效；review.go 与本注同源）：
// 0=通过（无达阈值意见）/异步提交成功；1=有达阈值意见（建议阻断）；2=错误
// （执行失败或用法错误）；4=空 diff 跳过；130=信号中断。hook 想放行一切照旧
// `|| true`，想阻断就保留退出码。
func main() {
	url := flag.String("url", envOr("ARGUS_MCP_URL", "http://127.0.0.1:8899/mcp"), "argus MCP 端点")
	token := flag.String("token", os.Getenv("ARGUS_APITOKEN"), "Bearer token（ARGUS_APITOKEN）")
	timeout := flag.Duration("timeout", 30*time.Second, "单次调用超时")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	// 信号中断 → 130（hook/CI 可与「审查发现意见」区分开）。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		os.Exit(130)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cli := dial(ctx, *url, *token)
	out, code, err := dispatch(ctx, cli, args[0], args[1:])
	fail(err)
	if out != "" {
		fmt.Println(out)
	}
	os.Exit(code)
}

func dial(ctx context.Context, url, token string) *mcpclient.Client {
	cli, err := mcpclient.NewStreamableHttpClient(url,
		mcptransport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	fail(err)
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "argus-copilot", Version: "1"}
	if _, err := cli.Initialize(ctx, req); err != nil {
		fail(err)
	}
	return cli
}

func dispatch(ctx context.Context, cli *mcpclient.Client, cmd string, rest []string) (string, int, error) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	task := fs.String("task", "", "任务 ID")
	fp := fs.String("fp", "", "意见指纹短码（suggest）")
	pr := fs.String("pr", "", "platform/owner/repo#number（ask）")
	q := fs.String("q", "", "追问内容（ask 可选）")
	diffFile := fs.String("f", "", "diff 文件路径（review；缺省读 stdin）")
	async := fs.Bool("async", false, "review 异步提交：立即返回 task_id 后退出，配合 poll 轮询结果")
	jsonOut := fs.Bool("json", false, "review/poll 结构化 JSON 输出（findings/file/line/severity/note）")
	failOn := fs.String("fail-on", "high", "退出码 1 的严重度阈值：high|medium|low|none（none 恒 0）")
	staged := fs.Bool("staged", false, "review 输入源：git diff --staged（与 -f 互斥）")
	uncommitted := fs.Bool("uncommitted", false, "review 输入源：git diff HEAD（staged+未暂存，与 -f/-staged 互斥）")
	includeUntracked := fs.Bool("include-untracked", false, "review 追加未跟踪文件为新文件 diff（配合 -staged/-uncommitted）")
	_ = fs.Parse(rest)

	switch cmd {
	case "stages":
		need(*task != "", "-task 必填")
		out, err := call(ctx, cli, "get_task_stages", map[string]any{"task_id": *task})
		return out, exitClean, err
	case "capabilities":
		out, err := call(ctx, cli, "get_capabilities", map[string]any{})
		return out, exitClean, err
	case "findings":
		need(*task != "", "-task 必填")
		// list_findings 返回的已是结构化 JSON（fp/severity/suggested_fix），
		// 机器消费无需再包一层信封。
		out, err := call(ctx, cli, "list_findings", map[string]any{"task_id": *task})
		return out, exitClean, err
	case "suggest":
		need(*task != "" && *fp != "", "-task 与 -fp 必填")
		out, err := call(ctx, cli, "get_suggestion", map[string]any{"task_id": *task, "fp": *fp})
		return out, exitClean, err
	case "ask":
		need(*pr != "", "-pr 必填（platform/owner/repo#number）")
		platform, owner, repo, number, err := splitPR(*pr)
		if err != nil {
			return "", exitClean, err
		}
		args := map[string]any{"platform": platform, "owner": owner, "repo": repo, "number": number}
		if *q != "" {
			args["question"] = *q
		}
		out, err := call(ctx, cli, "ask_argus", args)
		return out, exitClean, err
	case "review":
		// pre-commit 本地预审（v3.74.1；v3.124.0 起支持 git 侧组装与机器可读面）：
		//   git diff --staged | argus-copilot review            # stdin 同步快审
		//   argus-copilot review -staged -include-untracked      # CLI 自组 diff（含未跟踪文件）
		//   argus-copilot review -staged -async                  # 异步提交秒回 task_id
		// 退出码：0=无达阈值意见 1=有（-fail-on 定阈值）4=空 diff 2=错误。
		return runReview(ctx, cli, reviewInput{
			DiffFile: *diffFile, Staged: *staged, Uncommitted: *uncommitted,
			IncludeUntracked: *includeUntracked, Async: *async,
			JSONOut: *jsonOut, FailOn: *failOn,
		})
	case "poll":
		// 提交方轮询（v3.123.0）：按 task_id 等 diff_preview 任务到终态，打印报告。
		// 自带 10min 上限与 3s 间隔，不吃全局 -timeout（hook/CI 等待场景）。
		// v3.124.0：-json/-fail-on 与 review 同契约——报告里的意见过阈值即退出 1。
		need(*task != "", "-task 必填")
		return pollTask(context.Background(), cli, *task, pollOpts{JSONOut: *jsonOut, FailOn: *failOn})
	case "fix":
		// 沙箱修复（v3.89.0）：按 PR+fp 同步执行自动修复，输出 diff 与验证结论
		need(*pr != "" && *fp != "", "-pr 与 -fp 必填")
		platform, owner, repo, number, err := splitPR(*pr)
		if err != nil {
			return "", exitClean, err
		}
		out, err := call(ctx, cli, "fix_finding", map[string]any{
			"platform": platform, "owner": owner, "repo": repo, "number": number, "fp": *fp,
		})
		return out, exitClean, err
	default:
		usage()
		return "", exitClean, nil
	}
}

func call(ctx context.Context, cli *mcpclient.Client, name string, args map[string]any) (string, error) {
	res, err := cli.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		return "", err
	}
	if len(res.Content) == 0 {
		return "", fmt.Errorf("工具 %s 无内容", name)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		b, _ := json.Marshal(res.Content[0])
		return string(b), nil
	}
	return tc.Text, nil
}

// splitPR platform/owner/repo#number → 四元组。
func splitPR(s string) (platform, owner, repo, number string, err error) {
	i := strings.IndexByte(s, '#')
	if i <= 0 {
		return "", "", "", "", fmt.Errorf("格式须为 platform/owner/repo#number: %q", s)
	}
	number = s[i+1:]
	parts := strings.Split(s[:i], "/")
	if len(parts) != 3 {
		return "", "", "", "", fmt.Errorf("格式须为 platform/owner/repo#number: %q", s)
	}
	return parts[0], parts[1], parts[2], number, nil
}

// pollOpts poll 的机器可读面选项（与 review 同契约）。
type pollOpts struct {
	JSONOut bool
	FailOn  string
}

// pollTask 轮询 get_report 至终态（v3.123.0 diff_preview 闭环的提交方侧）：
// done/stale → 打印报告并按意见阈值给退出码；failed/超时 → 报错（退出码 2）。
// 10min 上限、3s 间隔。
func pollTask(ctx context.Context, cli *mcpclient.Client, taskID string, opts pollOpts) (string, int, error) {
	deadline := time.Now().Add(10 * time.Minute)
	lastStage := "" // 阶段进度只在变化时打 stderr，等待期不静默（v3.127.0）
	for {
		out, err := call(ctx, cli, "get_report", map[string]any{"task_id": taskID})
		if err != nil {
			return "", 0, err
		}
		var view map[string]any
		if jerr := json.Unmarshal([]byte(out), &view); jerr != nil {
			return out, exitClean, nil // 非 JSON 视为错误文本，原样透传
		}
		status, _ := view["status"].(string)
		if cur, _ := view["stage_current"].(string); cur != "" && cur != lastStage {
			lastStage = cur
			warnf("任务 %s 进行中，当前阶段 %s", taskID, cur)
		}
		switch status {
		case "done", "stale":
			report, _ := view["report"].(string)
			findings := parseFindings(report)
			if !opts.JSONOut {
				if report != "" {
					return report, thresholdExit(findings, opts.FailOn), nil
				}
				return out, exitClean, nil
			}
			res := reviewResult{
				Mode: "poll", Clean: len(findings) == 0, Findings: findings,
				Report: report, TaskID: taskID, Status: status,
			}
			if v, ok := view["verdict"].(string); ok {
				res.Verdict = v
			}
			if st, ok := view["stages"]; ok {
				res.Stages = st
			}
			if status == "stale" {
				res.Note = "审查完成但已有更新的提交，报告未回帖（结论仅对旧 head 成立）"
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			return string(b), thresholdExit(findings, opts.FailOn), nil
		case "failed":
			reason, _ := view["fail_reason"].(string)
			return "", 0, fmt.Errorf("预审失败: %s", reason)
		}
		if time.Now().After(deadline) {
			return "", 0, fmt.Errorf("轮询超时（10min），任务 %s 仍在执行，稍后重跑 poll", taskID)
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func need(cond bool, msg string) {
	if !cond {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "argus-copilot:", err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `argus-copilot — Argus 副驾参考客户端（数据面只读；review/fix 为服务端审查/修复面）

用法:
  argus-copilot -url <mcp> -token <t> capabilities
                                  （能力面快照：部署开了什么一次看清）
  argus-copilot -url <mcp> -token <t> stages   -task <id>
  argus-copilot -url <mcp> -token <t> findings -task <id>
  argus-copilot -url <mcp> -token <t> suggest  -task <id> -fp <短码>
  argus-copilot -url <mcp> -token <t> ask      -pr platform/owner/repo#num [-q 问题]
  git diff --staged | argus-copilot -url <mcp> -token <t> review [-f <diff文件>]
                                  （pre-commit 本地预审：服务端单轮 LLM 审查，
                                   不建任务不落库；模型未配置时返回错误）
  argus-copilot -url <mcp> -token <t> review -staged [-include-untracked]
                                  （CLI 自组 diff：-staged=git diff --staged，
                                   -uncommitted=git diff HEAD；-include-untracked
                                   把未跟踪文件合成 new-file diff 拼入）
  argus-copilot -url <mcp> -token <t> review [-staged] -json -fail-on medium
                                  （-json 结构化输出 findings；-fail-on 定退出码
                                   阈值 high|medium|low|none，缺省 high）
  git diff --staged | argus-copilot -url <mcp> -token <t> review -async
  argus-copilot -url <mcp> -token <t> poll -task <id> [-json -fail-on <档>]
                                  （异步预审闭环：提交秒回 task_id 不阻塞，
                                   提交方稍后 poll 轮询报告；≤48KB）
  argus-copilot -url <mcp> -token <t> fix      -pr platform/owner/repo#num -fp <短码>
                                  （沙箱修复：服务端编码 agent 出 patch + 确定性
                                   验证，回显 diff；需 ARGUS_FIX_AGENT 启用，ci+ 档 token）

review/poll 退出码: 0=通过（无达阈值意见）/异步提交成功  1=有达阈值意见（建议阻断）
  2=错误（执行失败或用法错误）  4=空 diff 跳过  130=信号中断
环境: ARGUS_MCP_URL / ARGUS_APITOKEN
`)
	os.Exit(2)
}
