package e2e

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gopkg.in/yaml.v3"

	"github.com/yi-nology/argus/internal/plugin"
)

type utHeader = struct{ Key, Value string }

func freeTCPPort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "18080"
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return strconv.Itoa(port)
}

func waitHTTPReady(addr string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			time.Sleep(100 * time.Millisecond) // 等 hertz 完成路由装配
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func performRequest(h *server.Hertz, method, url, body string, headers []utHeader) *ut.ResponseRecorder {
	utHeaders := make([]ut.Header, 0, len(headers))
	for _, hd := range headers {
		utHeaders = append(utHeaders, ut.Header{Key: hd.Key, Value: hd.Value})
	}
	return ut.PerformRequest(h.Engine, method, url, &ut.Body{Body: strings.NewReader(body), Len: len(body)}, utHeaders...)
}

func mustMkdir(t TestingT, dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t TestingT, path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { // 755：mock 可执行脚本需要
		t.Fatal(err)
	}
}

// marshalAgents 把声明序列化为 plugins.yaml。
func marshalAgents(agents []plugin.AgentDecl) string {
	pf := struct {
		Agents []plugin.AgentDecl `yaml:"agents"`
	}{Agents: agents}
	data, _ := yaml.Marshal(pf)
	return string(data)
}

// marshalPluginsFile agents + squads 全量声明（编队场景用）。
func marshalPluginsFile(agents []plugin.AgentDecl, squads []plugin.SquadDecl) string {
	pf := struct {
		Agents []plugin.AgentDecl `yaml:"agents"`
		Squads []plugin.SquadDecl `yaml:"squads"`
	}{Agents: agents, Squads: squads}
	data, _ := yaml.Marshal(pf)
	return string(data)
}

// writeOCRScript 生成假 OCR CLI：打印固定 JSON findings（容错解析器契约见 adapter_cli）。
func writeOCRScript(t TestingT, dir string) string {
	path := filepath.Join(dir, "bin", "mock-ocr.sh")
	mustMkdir(t, filepath.Dir(path))
	script := `#!/bin/sh
# mock OCR：忽略参数，输出固定 review findings
cat <<'EOF'
[
  {"path": "main.go", "line": 3, "level": "high", "comment": "e2e-ocr finding: hard-coded token"},
  {"path": "main.go", "line": 9, "level": "low", "comment": "e2e-ocr finding: naming style"}
]
EOF
`
	mustWrite(t, path, script)
	if out, err := exec.Command("chmod", "+x", path).CombinedOutput(); err != nil {
		t.Fatalf("chmod 失败: %v %s", err, out)
	}
	return path
}

// makeFixtureRepo 建 file:// 测试仓库：main 分支 + refs/pull/7/head（workcopy 浅克隆链路）。
func makeFixtureRepo(t TestingT, dir string) string {
	repo := filepath.Join(dir, "fixture-repo")
	mustMkdir(t, repo)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@test",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "--initial-branch", "main", "-q")
	mustWrite(t, repo+"/main.go", "package main\n\nfunc login() {}\n")
	run("add", ".")
	run("commit", "-qm", "init")
	run("update-ref", "refs/pull/7/head", "HEAD")
	return repo
}

// writeFakeKimi 生成假 kimi CLI（kimi-code 协议：stream-json 消息流），
// content 携带 findings JSON 数组（验证 adapter_acpx 的提取/归一化/By 标注全链路）。
func writeFakeKimi(t TestingT, dir string) {
	path := filepath.Join(dir, "bin", "kimi")
	mustMkdir(t, filepath.Dir(path))
	script := `#!/bin/sh
# mock kimi：忽略参数，输出一条 assistant 消息（content = findings JSON）
cat <<'EOF'
{"role":"assistant","content":"[{\"file\":\"main.go\",\"line\":7,\"severity\":\"high\",\"comment\":\"e2e-acpx finding: SQL string concat\"}]"}
EOF
`
	mustWrite(t, path, script)
	if out, err := exec.Command("chmod", "+x", path).CombinedOutput(); err != nil {
		t.Fatalf("chmod 失败: %v %s", err, out)
	}
}
