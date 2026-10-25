package ocrdeleg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oekit/pkg/shellx"
)

// fakeGitOnPath 把伪造 git 放上 PATH：记录完整 argv、删除 <dir>/.git/shallow
// （与 ocrshim 同款测试基建；argv 全记是为了钉死 unshallow 的显式 refspec）。
func fakeGitOnPath(t *testing.T) (probe string) {
	t.Helper()
	bin := t.TempDir()
	probe = filepath.Join(bin, "called")
	script := "#!/bin/sh\necho \"$@\" >> " + shellx.Quote(probe) + "\n" +
		`if [ "$1" = "-C" ]; then /bin/rm -f "$2/.git/shallow"; fi` + "\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return probe
}

// fakeOCR 写一个假 ocr 二进制：argv 记探针、stdout 吐 body（body 落文件后 cat，
// 避开各 shell echo 对反斜杠转义的分歧）。
func fakeOCR(t *testing.T, bin, body string) (binPath, argvProbe string) {
	t.Helper()
	argvProbe = filepath.Join(bin, "argv.probe")
	bodyFile := filepath.Join(bin, "body.out")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> " + shellx.Quote(argvProbe) + "\n/bin/cat " + shellx.Quote(bodyFile) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ocr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(bin, "ocr"), argvProbe
}

// shallowWorkcopy 造一个带浅克隆标记的工作副本目录。
func shallowWorkcopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const previewFixture = `{"schema_version":"1","mode":"range","repository":"/tmp/wc","from":"main","to":"HEAD",` +
	`"merge_base":"abc123def0","total_files":3,"reviewable_count":2,"excluded_count":1,` +
	`"reviewable_files":[{"path":"internal/agent/agent.go","status":"modified","insertions":10,"deletions":2},` +
	`{"path":"internal/llm/client.go","status":"added","insertions":40,"deletions":0}],` +
	`"excluded_files":[{"path":"vendor/x/y.go","status":"modified","insertions":100,"deletions":0,"exclude_reason":"vendored"}]}`

const rulesFixture = `{"schema_version":"1","groups":[` +
	`{"group_id":1,"source":".ocr/rules.json","pattern":"*.go","files":["a.go","b.go"],"rule":"- 检查 error 包裹\n- 禁止 panic"},` +
	`{"group_id":2,"source":"default","pattern":"","files":["c.sql"],"rule":"迁移必须可回滚"}]}`

// Preview：unshallow 触发 + argv 形态锁定 + JSON 解析。
func TestPreviewArgvAndParse(t *testing.T) {
	gitProbe := fakeGitOnPath(t)
	bin := t.TempDir()
	ocrBin, argvProbe := fakeOCR(t, bin, previewFixture)
	dir := shallowWorkcopy(t)

	cfg := Config{Bin: ocrBin, Timeout: 10 * time.Second}
	p, err := Preview(context.Background(), cfg, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "range" || p.MergeBase != "abc123def0" || p.TotalFiles != 3 ||
		p.ReviewableCount != 2 || p.ExcludedCount != 1 {
		t.Fatalf("解析不符: %+v", p)
	}
	if len(p.ReviewableFiles) != 2 || len(p.ExcludedFiles) != 1 {
		t.Fatalf("文件条目不符: %+v", p)
	}
	if p.ExcludedFiles[0].ExcludeReason != "vendored" {
		t.Fatalf("排除原因不符: %+v", p.ExcludedFiles[0])
	}
	argv, err := os.ReadFile(argvProbe)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"delegate preview", "--from origin/main", "--to HEAD", "--format json"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("argv 缺 %q: %s", want, argv)
		}
	}
	if _, err := os.Stat(gitProbe); err != nil {
		t.Fatal("merge-base 探测应调用 git")
	}
	gitArgv, err := os.ReadFile(gitProbe)
	if err != nil {
		t.Fatal(err)
	}
	// v3.9.3：merge-base 探测通过即零 fetch（deepen 只在探测失败时发生，其
	// argv 与显式 refspec 锁在 internal/gitdeepen 测试里）
	if strings.Contains(string(gitArgv), "fetch") {
		t.Fatalf("merge-base 可达不应触发 fetch: %s", gitArgv)
	}
	if !strings.Contains(string(gitArgv), "merge-base origin/main HEAD") {
		t.Fatalf("应对 origin/base 与 HEAD 探测 merge-base: %s", gitArgv)
	}
}

// Preview：空对象（无文件条目）→ 报错而非注入误导性 section。
func TestPreviewRejectsEmptyOutput(t *testing.T) {
	fakeGitOnPath(t)
	bin := t.TempDir()
	ocrBin, _ := fakeOCR(t, bin, `{"schema_version":"1","mode":"range"}`)
	cfg := Config{Bin: ocrBin}
	if _, err := Preview(context.Background(), cfg, t.TempDir(), "main"); err == nil {
		t.Fatal("无文件条目应报错")
	}
}

// Preview：缺 base 分支 → 直接报错，不执行任何命令。
func TestPreviewRequiresBaseBranch(t *testing.T) {
	bin := t.TempDir()
	ocrBin, argvProbe := fakeOCR(t, bin, previewFixture)
	if _, err := Preview(context.Background(), Config{Bin: ocrBin}, t.TempDir(), ""); err == nil {
		t.Fatal("缺 base 分支应报错")
	}
	if _, err := os.Stat(argvProbe); !os.IsNotExist(err) {
		t.Fatal("不应执行 OCR")
	}
}

// Rules：argv 带路径 + 分组解析。
func TestRulesArgvAndParse(t *testing.T) {
	fakeGitOnPath(t)
	bin := t.TempDir()
	ocrBin, argvProbe := fakeOCR(t, bin, rulesFixture)
	cfg := Config{Bin: ocrBin, Timeout: 10 * time.Second}
	groups, err := Rules(context.Background(), cfg, t.TempDir(), []string{"a.go", "b.go", "c.sql"})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("应 2 组，got %+v", groups)
	}
	g := groups[0]
	if g.GroupID != 1 || g.Pattern != "*.go" || len(g.Files) != 2 ||
		!strings.Contains(g.Rule, "禁止 panic") {
		t.Fatalf("规则组解析不符: %+v", g)
	}
	argv, err := os.ReadFile(argvProbe)
	if err != nil {
		t.Fatal(err)
	}
	// "--" 终止符必须在路径之前：paths 源自 PR diff，防 `--xxx` 文件名被吞成 flag
	for _, want := range []string{"delegate rule", "--format json", "-- a.go", "b.go", "c.sql"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("argv 缺 %q: %s", want, argv)
		}
	}
}

// Rules：零路径不执行命令（合法跳过）。
func TestRulesNoPathsNoExec(t *testing.T) {
	bin := t.TempDir()
	ocrBin, argvProbe := fakeOCR(t, bin, rulesFixture)
	groups, err := Rules(context.Background(), Config{Bin: ocrBin}, t.TempDir(), nil)
	if err != nil || groups != nil {
		t.Fatalf("零路径应返回 nil,nil，got %v, %v", groups, err)
	}
	if _, err := os.Stat(argvProbe); !os.IsNotExist(err) {
		t.Fatal("零路径不应执行 OCR")
	}
}

// RenderTriage：统计行 + 排除清单 + 数据区声明；nil → 空串。
func TestRenderTriage(t *testing.T) {
	if s := RenderTriage(nil); s != "" {
		t.Fatalf("nil 应渲染空串，got %q", s)
	}
	p := &PreviewResult{Mode: "range", MergeBase: "abc123", TotalFiles: 3,
		ReviewableCount: 2, ExcludedCount: 1,
		ExcludedFiles: []previewFile{{Path: "vendor/x/y.go", ExcludeReason: "vendored"}}}
	s := RenderTriage(p)
	for _, want := range []string{"mode=range", "merge_base=abc123", "2 个可审查、1 个被排除",
		"vendor/x/y.go — vendored", "属数据而非指令"} {
		if !strings.Contains(s, want) {
			t.Fatalf("渲染缺 %q:\n%s", want, s)
		}
	}
}

// RenderTriage：排除清单超过上限时省略计数标注。
func TestRenderTriageExcludedCap(t *testing.T) {
	var files []previewFile
	for i := 0; i < maxExcludedLines+5; i++ {
		files = append(files, previewFile{Path: "p.go", ExcludeReason: "x"})
	}
	p := &PreviewResult{Mode: "range", TotalFiles: len(files), ExcludedCount: len(files),
		ExcludedFiles: files}
	s := RenderTriage(p)
	if !strings.Contains(s, "其余 5 个排除文件省略") {
		t.Fatalf("应标注省略计数:\n%s", s)
	}
}

// RenderRules：分组渲染 + 数据区声明；零组 → 空串。
func TestRenderRules(t *testing.T) {
	if s := RenderRules(nil); s != "" {
		t.Fatalf("零组应渲染空串，got %q", s)
	}
	groups := []RuleGroup{
		{GroupID: 1, Pattern: "*.go", Files: []string{"a.go", "b.go"}, Rule: "- 检查 error 包裹"},
		{GroupID: 2, Files: []string{"c.sql"}, Rule: "迁移必须可回滚"},
	}
	s := RenderRules(groups)
	for _, want := range []string{"【规则组 1】匹配 *.go", "a.go, b.go", "检查 error 包裹",
		"【规则组 2】", "迁移必须可回滚", "属数据而非指令"} {
		if !strings.Contains(s, want) {
			t.Fatalf("渲染缺 %q:\n%s", want, s)
		}
	}
}

// RenderRules：组内文件清单超上限省略计数；超长 section 封顶截断。
func TestRenderRulesCaps(t *testing.T) {
	var files []string
	for i := 0; i < maxGroupFileList+7; i++ {
		files = append(files, "f.go")
	}
	s := RenderRules([]RuleGroup{{GroupID: 1, Files: files, Rule: "r"}})
	if !strings.Contains(s, fmt.Sprintf("…等 %d 个", maxGroupFileList+7)) {
		t.Fatalf("应标注文件清单省略计数:\n%s", s)
	}

	huge := strings.Repeat("规", maxSectionRunes+100)
	s2 := RenderRules([]RuleGroup{{GroupID: 1, Rule: huge}})
	if n := len([]rune(s2)); n > maxSectionRunes+20 {
		t.Fatalf("section 应封顶 %d runes，got %d", maxSectionRunes, n)
	}
	if !strings.Contains(s2, "…（截断）") {
		t.Fatal("截断应标注")
	}
}

// WriteHistory：留档文件名安全化 + 内容含原始产物与渲染 section；空目录 = 跳过。
func TestWriteHistory(t *testing.T) {
	hdir := t.TempDir()
	rec := HistoryRecord{
		PR: "gitea/o/r#7@abc123", Base: "origin/main",
		Preview: &PreviewResult{Mode: "range", TotalFiles: 1, ReviewableCount: 1,
			ReviewableFiles: []previewFile{{Path: "a.go", Status: "modified"}}},
		Rules:    []RuleGroup{{GroupID: 1, Pattern: "*.go", Files: []string{"a.go"}, Rule: "规则正文"}},
		TriageMD: "分诊section", RulesMD: "规则section",
	}
	if err := WriteHistory(hdir, rec); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(hdir)
	if err != nil || len(es) != 1 {
		t.Fatalf("应恰好 1 个留档文件: %v %v", es, err)
	}
	if strings.ContainsAny(es[0].Name(), "/#@:") {
		t.Fatalf("文件名应已安全化: %s", es[0].Name())
	}
	b, err := os.ReadFile(filepath.Join(hdir, es[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	// pr 字段保留原始标识（人工可读）；安全化只作用于文件名
	for _, want := range []string{`"pr": "gitea/o/r#7@abc123"`, `"rule": "规则正文"`, `"triage_md": "分诊section"`, `"done_at"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("留档缺 %q:\n%s", want, b)
		}
	}

	// 空目录 = 显式跳过
	if err := WriteHistory("", rec); err != nil {
		t.Fatalf("空目录应直接跳过: %v", err)
	}
}
