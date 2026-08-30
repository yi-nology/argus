package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gopkg.in/yaml.v3"

	"argus/internal/plugin"
)

type utHeader = struct{ Key, Value string }

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
