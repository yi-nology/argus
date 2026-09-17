package ocrshim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.example.com/enjoydream/agentkit/workcopy"

	"argus/internal/plugin"
	"argus/internal/plugin/adapter_remote"
)

// 假 OCR：把收到的 argv 与 OPENAI_API_KEY 写探针文件，stdout 打新 schema JSON。
func fakeOCR(t *testing.T, bin string) (argvProbe, envProbe string) {
	t.Helper()
	argvProbe = filepath.Join(bin, "argv.probe")
	envProbe = filepath.Join(bin, "env.probe")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + argvProbe + "\n" +
		"echo \"$OPENAI_API_KEY\" >> " + envProbe + "\n" +
		`echo '{"comments":[{"path":"a.go","content":"竞态","start_line":7,"severity":"critical","suggestion_code":"加锁"}]}'` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ocr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return argvProbe, envProbe
}

// staticPool 固定返回工作副本目录（满足 adapter_remote.WorkcopyPool）。
type staticPool struct{ dir string }

func (p staticPool) Ensure(_ context.Context, _ workcopy.WorktreeKey) (string, error) {
	return p.dir, nil
}

func (p staticPool) Release(workcopy.WorktreeKey) {}

// shimClient 起一个真 shim，返回 argus remote/mcp 客户端（needs:[working-copy]，
// 目录经 payload.workcopy_dir 下发——与生产装配同路径）。
func shimClient(t *testing.T, cfg Config, workcopyDir string) *adapter_remote.Remote {
	t.Helper()
	ts := httptest.NewServer(Handler(cfg))
	t.Cleanup(ts.Close)
	m := plugin.AgentManifest{Name: "ocr", Skills: []string{plugin.SkillCodeReview},
		Timeout: 60 * time.Second}
	m.Needs.WorkingCopy = true
	client, err := adapter_remote.New(m, plugin.ProtocolMCP, ts.URL+"/mcp", "review", "", staticPool{dir: workcopyDir})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// 端到端：argus remote/mcp 适配器（真客户端）→ shim → fake git/OCR → findings 契约。
// 同时锁定：unshallow 触发、argv 形态、LLM 凭证白名单透传、healthz。
func TestHandlerEndToEnd(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	bin := t.TempDir()
	argvProbe, envProbe := fakeOCR(t, bin)
	fakeGitOnPath(t) // PATH 上的假 git（摘 shallow 标记）

	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, ".git", "shallow"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{OcrBin: filepath.Join(bin, "ocr"), OcrEnv: []string{"OPENAI_API_KEY"},
		Timeout: 30 * time.Second, UnshallowTimeout: 30 * time.Second, MaxStdout: 1 << 20}
	ts := httptest.NewServer(Handler(cfg))
	t.Cleanup(ts.Close)

	// healthz 就绪探针
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz 应 200，got %d", resp.StatusCode)
	}

	m := plugin.AgentManifest{Name: "ocr", Skills: []string{plugin.SkillCodeReview}, Timeout: 60 * time.Second}
	m.Needs.WorkingCopy = true
	client, err := adapter_remote.New(m, plugin.ProtocolMCP, ts.URL+"/mcp", "review", "", staticPool{dir: wc})
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Invoke(context.Background(), plugin.Task{PR: plugin.PRContext{
		Platform: "gitea", Owner: "demo-user", Repo: "demo", Number: "7",
		Title: "fix login", BaseBranch: "main", HeadSHA: "abc",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("应 1 条 finding，got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.File != "a.go" || f.Line != 7 || f.Comment != "竞态" || f.Severity != "high" || f.SuggestedFix != "加锁" {
		t.Fatalf("归一化不符: %+v", f)
	}

	// shallow 标记应已被假 git 摘除（unshallow 触发）
	if _, err := os.Stat(filepath.Join(wc, ".git", "shallow")); !os.IsNotExist(err) {
		t.Fatal("unshallow 应已执行")
	}
	// argv 形态锁定
	argvRaw, err := os.ReadFile(argvProbe)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(argvRaw)
	for _, want := range []string{"--from origin/main", "--to HEAD", "--format json", "--audience agent", "--background fix login"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv 缺 %q: %s", want, argv)
		}
	}
	// LLM 凭证经白名单透传
	envRaw, err := os.ReadFile(envProbe)
	if err != nil {
		t.Fatal(err)
	}
	if string(envRaw) != "sk-secret\n" {
		t.Fatalf("OCR 应收到白名单凭证: %q", string(envRaw))
	}
}

// payload 缺 workcopy_dir（argus 侧未启用 working-copy）→ 工具错误，不误跑。
func TestHandlerPayloadValidation(t *testing.T) {
	bin := t.TempDir()
	fakeOCR(t, bin)
	cfg := Config{OcrBin: filepath.Join(bin, "ocr"), Timeout: 30 * time.Second, UnshallowTimeout: 60 * time.Second}
	ts := httptest.NewServer(Handler(cfg))
	t.Cleanup(ts.Close)

	m := plugin.AgentManifest{Name: "ocr", Timeout: 60 * time.Second}
	client, err := adapter_remote.New(m, plugin.ProtocolMCP, ts.URL+"/mcp", "review", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Invoke(context.Background(), plugin.Task{PR: plugin.PRContext{BaseBranch: "main"}}); err == nil {
		t.Fatal("缺 workcopy_dir 应报错")
	}
}

// 零意见 PR：OCR 输出 {"comments":[]} → findings 空数组（合法零结果，非错误）。
func TestHandlerZeroFindings(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ocr"), []byte("#!/bin/sh\necho '{\"comments\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OcrBin: filepath.Join(bin, "ocr"), Timeout: 30 * time.Second, UnshallowTimeout: 60 * time.Second}
	client := shimClient(t, cfg, t.TempDir()) // 无 shallow → 不触发 git
	res, err := client.Invoke(context.Background(), plugin.Task{PR: plugin.PRContext{BaseBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("应 0 条，got %+v", res.Findings)
	}
}
