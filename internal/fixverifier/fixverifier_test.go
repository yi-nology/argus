package fixverifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"

	"github.com/yi-nology/argus/internal/domain"
)

// setupFenceFixtures 工作副本围栏夹具：合法文件 + 副本内 symlink + 逃逸 symlink
// + 悬空软链。返回副本根与沙箱外文件。
func setupFenceFixtures(t *testing.T) (dir, outsideFile string) {
	t.Helper()
	dir = t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideFile = filepath.Join(outside, "secret")
	if err := os.WriteFile(outsideFile, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 逃逸 symlink：仓库可携带指向沙箱外的链接（恶意 PR 提交 link -> 外部文件）
	if err := os.Symlink(outsideFile, filepath.Join(dir, "escape.go")); err != nil {
		t.Fatal(err)
	}
	// 悬空软链：目标不存在（文本拼接对它等于未判定，围栏必须拒判）
	if err := os.Symlink("/nonexistent/argus-fence-test", filepath.Join(dir, "dangling.go")); err != nil {
		t.Fatal(err)
	}
	// 副本内 symlink：合法（落地仍在副本内）
	if err := os.Symlink(filepath.Join(dir, "main.go"), filepath.Join(dir, "inner-link.go")); err != nil {
		t.Fatal(err)
	}
	return dir, outsideFile
}

func TestVerify(t *testing.T) {
	dir, _ := setupFenceFixtures(t)
	// 包装命令：验证目标是「文件能编译」——用 gofmt 代替真实编译（无工具链依赖
	// 差异；命令失败即验证失败）
	cfg := Config{Cmd: "gofmt -l . | grep -q . && exit 1 || exit 0", Timeout: 0}
	findings := []domain.Finding{
		{File: "main.go", Line: 4, Severity: "low", Comment: "x", SuggestedFix: "\tprintln(\"hello\")"},
		{File: "main.go", Line: 99, SuggestedFix: "\tx"},     // 行越界
		{File: "missing.go", Line: 1, SuggestedFix: "\tx"},   // 文件缺失（围栏放行、读文件失败跳过）
		{File: "main.go", Line: 3, Comment: "no fix"},        // 无建议
		{File: "../escape.go", Line: 1, SuggestedFix: "\tx"}, // 相对路径越界（拒判）
		{File: "escape.go", Line: 1, SuggestedFix: "\tx"},    // symlink 逃逸沙箱外（拒判）
		{File: "dangling.go", Line: 1, SuggestedFix: "\tx"},  // 悬空软链（拒判）
	}
	results := Verify(context.Background(), cfg, dir, findings)
	if len(results) != len(findings) {
		t.Fatalf("结果数不符: %d", len(results))
	}
	if !results[0].OK || !results[0].Applied {
		t.Fatalf("第 1 条应验证通过: %+v", results[0])
	}
	// 验证后文件必须还原
	raw, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(raw), `"hi"`) || strings.Contains(string(raw), `"hello"`) {
		t.Fatalf("验证后文件应还原: %s", raw)
	}
	// 越界/逃逸/断链拒绝（围栏拒判），缺失/无建议跳过
	for i, want := range []string{"锚定行越界", "文件不在工作副本", "无修复建议", "围栏", "围栏", "围栏"} {
		if results[i+1].OK || results[i+1].Skipped == "" || !strings.Contains(results[i+1].Skipped, want) {
			t.Fatalf("第 %d 条应跳过(%s): %+v", i+1, want, results[i+1])
		}
	}
}

// TestResolveSafePathFence FSFence 收编对表（guardx/sandbox 错误哨兵逐一对照）：
// 越界/逃逸/悬空软链拒判，合法路径（含副本内 symlink）放行——手写三段式
// 「越界/断链/逃逸」三类拒绝语义同向。
func TestResolveSafePathFence(t *testing.T) {
	dir, outsideFile := setupFenceFixtures(t)

	// 合法路径放行（含副本内 symlink：落地后仍在副本内）
	for _, rel := range []string{"main.go", "inner-link.go"} {
		p, err := resolveSafePath(dir, rel)
		if err != nil {
			t.Fatalf("%q 应放行: %v", rel, err)
		}
		if !strings.Contains(p, "main.go") {
			t.Fatalf("%q 落地路径异常: %s", rel, p)
		}
	}

	// 拒绝对表
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"相对路径越界", "../escape.go", sandbox.ErrFenceInvalid},
		{"绝对路径越界", outsideFile, sandbox.ErrFenceOutside},
		{"symlink 逃逸", "escape.go", sandbox.ErrFenceOutside},
		{"悬空软链", "dangling.go", sandbox.ErrFenceInvalid},
		{"空路径", "", sandbox.ErrFenceInvalid},
	} {
		if _, err := resolveSafePath(dir, tc.path); !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}

	// 副本根不可达（构造期 fail-fast）
	if _, err := resolveSafePath(filepath.Join(dir, "no-such-root"), "main.go"); err == nil {
		t.Fatal("副本根不存在应报错")
	}
}

func TestVerifyNoCmd(t *testing.T) {
	results := Verify(context.Background(), Config{}, "/tmp", []domain.Finding{{SuggestedFix: "x"}})
	if results[0].OK || results[0].Skipped == "" {
		t.Fatalf("未配置命令应整体跳过: %+v", results[0])
	}
}
