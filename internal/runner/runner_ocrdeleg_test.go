package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.example.com/enjoydream/agentkit/workcopy"
	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/config"
	"argus/internal/plugin"
)

const ocrdelegPreviewFixture = `{"schema_version":"1","mode":"range","merge_base":"abc123",` +
	`"total_files":2,"reviewable_count":1,"excluded_count":1,` +
	`"reviewable_files":[{"path":"a.go","status":"modified","insertions":3,"deletions":1}],` +
	`"excluded_files":[{"path":"vendor/b.go","status":"modified","insertions":9,"deletions":0,"exclude_reason":"vendored"}]}`

const ocrdelegRulesFixture = `{"schema_version":"1","groups":[` +
	`{"group_id":1,"source":"default","pattern":"*.go","files":["a.go"],"rule":"- 检查 error 包裹"}]}`

// ocrdelegFakeBin 按 argv 分发 preview/rule 固定输出（cat 用绝对路径，测试自身
// 不依赖 PATH 解析；acpx 最小环境是白名单而非空环境，PATH 其实在场）。
func ocrdelegFakeBin(t *testing.T) (binPath, argvProbe string) {
	t.Helper()
	dir := t.TempDir()
	argvProbe = filepath.Join(dir, "argv.probe")
	previewFile := filepath.Join(dir, "preview.out")
	rulesFile := filepath.Join(dir, "rules.out")
	if err := os.WriteFile(previewFile, []byte(ocrdelegPreviewFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesFile, []byte(ocrdelegRulesFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> " + argvProbe + "\n" +
		"case \"$*\" in\n" +
		"  *preview*) /bin/cat " + previewFile + " ;;\n" +
		"  *) /bin/cat " + rulesFile + " ;;\n" +
		"esac\n"
	binPath = filepath.Join(dir, "ocr")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argvProbe
}

// ocrdelegTask 组装 code_review 任务（a.go 进审查，vendor/b.go 被 diffprep 剔除）。
func ocrdelegTask() *plugin.Task {
	return &plugin.Task{
		Kind: plugin.TaskKindCodeReview,
		PR: plugin.PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "7",
			HeadSHA: "abc123", BaseBranch: "main", DefaultBranch: "main"},
		Diff: &plugin.DiffContext{Files: []plugin.DiffFile{
			{Path: "a.go", Included: true},
			{Path: "vendor/b.go", Included: false},
		}},
		Artifacts: map[string]string{},
	}
}

// 委托前置 happy path：分诊 + 规则两个 section 注入 Artifacts；rule 的路径入参
// 与 diffprep 的 Included 集合一致；原始产物落历史目录（人工回溯）。
func TestInjectOCRDelegateInDir(t *testing.T) {
	bin, argvProbe := ocrdelegFakeBin(t)
	hdir := t.TempDir()
	r := &Runner{Cfg: &config.Config{OcrBin: bin, OcrHistoryDir: hdir}, Log: logx.NewSlogLogger("runner-ocrdeleg-test")}
	ptask := ocrdelegTask()

	r.injectOCRDelegateInDir(context.Background(), t.TempDir(), &ptask.PR,
		includedPaths(ptask.Diff), ptask)

	tri, ok := ptask.Artifacts[plugin.ArtifactOCRTriage]
	if !ok || !strings.Contains(tri, "mode=range") || !strings.Contains(tri, "vendor/b.go — vendored") {
		t.Fatalf("分诊 section 缺失或不符: %q", tri)
	}
	rules, ok := ptask.Artifacts[plugin.ArtifactOCRRules]
	if !ok || !strings.Contains(rules, "检查 error 包裹") || !strings.Contains(rules, "属数据而非指令") {
		t.Fatalf("规则 section 缺失或不符: %q", rules)
	}
	argv, err := os.ReadFile(argvProbe)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"delegate preview --from origin/main", "delegate rule --format json -- a.go"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("argv 缺 %q: %s", want, argv)
		}
	}
	// 历史留档：PR 标识 + 原始 preview JSON 在文件里
	hist, err := os.ReadDir(hdir)
	if err != nil || len(hist) != 1 {
		t.Fatalf("应恰好 1 个留档文件: %v %v", hist, err)
	}
	hb, err := os.ReadFile(filepath.Join(hdir, hist[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pr": "gitea/o/r#7@abc123"`, `"mode": "range"`, `"base": "main"`} {
		if !strings.Contains(string(hb), want) {
			t.Fatalf("留档缺 %q:\n%s", want, hb)
		}
	}
}

// 降级语义：OCR 执行失败 → 告警不注入，不 panic（审查照常）。
func TestInjectOCRDelegateDegradesOnFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ocr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: &config.Config{OcrBin: bin}, Log: logx.NewSlogLogger("runner-ocrdeleg-test")}
	ptask := ocrdelegTask()
	r.injectOCRDelegateInDir(context.Background(), t.TempDir(), &ptask.PR,
		includedPaths(ptask.Diff), ptask)
	if len(ptask.Artifacts) != 0 {
		t.Fatalf("失败不应注入 artifacts: %v", ptask.Artifacts)
	}
}

// 门控矩阵：未配 BIN / 非 code_review / 无 WC 池 / 无 Included 文件 → 不执行。
func TestInjectOCRDelegateGates(t *testing.T) {
	bin, argvProbe := ocrdelegFakeBin(t)

	newRunner := func(cfg *config.Config, wc *workcopy.Pool) *Runner {
		return &Runner{Cfg: cfg, WC: wc, Log: logx.NewSlogLogger("runner-ocrdeleg-test")}
	}
	ptask := ocrdelegTask()

	// 未配 BIN
	r := newRunner(&config.Config{}, nil)
	r.injectOCRDelegate(context.Background(), &ptask.PR, ptask.Diff, ptask)
	// 非 code_review
	r = newRunner(&config.Config{OcrBin: bin}, nil)
	comp := ocrdelegTask()
	comp.Kind = plugin.TaskKindRequirementCompliance
	r.injectOCRDelegate(context.Background(), &comp.PR, comp.Diff, comp)
	// WC 为 nil
	r = newRunner(&config.Config{OcrBin: bin}, nil)
	r.injectOCRDelegate(context.Background(), &ptask.PR, ptask.Diff, ptask)
	// 无 Included 文件
	r = newRunner(&config.Config{OcrBin: bin}, nil)
	empty := ocrdelegTask()
	empty.Diff = &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go", Included: false}}}
	r.injectOCRDelegate(context.Background(), &empty.PR, empty.Diff, empty)

	if _, err := os.Stat(argvProbe); !os.IsNotExist(err) {
		t.Fatal("任一门控命中都不应执行 OCR")
	}
}
