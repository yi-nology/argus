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
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	url := flag.String("url", envOr("ARGUS_MCP_URL", "http://127.0.0.1:8899/mcp"), "argus MCP 端点")
	token := flag.String("token", os.Getenv("ARGUS_APITOKEN"), "Bearer token（ARGUS_APITOKEN）")
	timeout := flag.Duration("timeout", 30*time.Second, "单次调用超时")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cli := dial(ctx, *url, *token)
	out, err := dispatch(ctx, cli, args[0], args[1:])
	fail(err)
	fmt.Println(out)
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

func dispatch(ctx context.Context, cli *mcpclient.Client, cmd string, rest []string) (string, error) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	task := fs.String("task", "", "任务 ID")
	fp := fs.String("fp", "", "意见指纹短码（suggest）")
	pr := fs.String("pr", "", "platform/owner/repo#number（ask）")
	q := fs.String("q", "", "追问内容（ask 可选）")
	_ = fs.Parse(rest)

	switch cmd {
	case "stages":
		need(*task != "", "-task 必填")
		return call(ctx, cli, "get_task_stages", map[string]any{"task_id": *task})
	case "findings":
		need(*task != "", "-task 必填")
		return call(ctx, cli, "list_findings", map[string]any{"task_id": *task})
	case "suggest":
		need(*task != "" && *fp != "", "-task 与 -fp 必填")
		return call(ctx, cli, "get_suggestion", map[string]any{"task_id": *task, "fp": *fp})
	case "ask":
		need(*pr != "", "-pr 必填（platform/owner/repo#number）")
		platform, owner, repo, number, err := splitPR(*pr)
		if err != nil {
			return "", err
		}
		args := map[string]any{"platform": platform, "owner": owner, "repo": repo, "number": number}
		if *q != "" {
			args["question"] = *q
		}
		return call(ctx, cli, "ask_argus", args)
	default:
		usage()
		return "", nil
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
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `argus-copilot — Argus 副驾参考客户端（只读）

用法:
  argus-copilot -url <mcp> -token <t> stages   -task <id>
  argus-copilot -url <mcp> -token <t> findings -task <id>
  argus-copilot -url <mcp> -token <t> suggest  -task <id> -fp <短码>
  argus-copilot -url <mcp> -token <t> ask      -pr platform/owner/repo#num [-q 问题]

环境: ARGUS_MCP_URL / ARGUS_APITOKEN
`)
	os.Exit(2)
}
