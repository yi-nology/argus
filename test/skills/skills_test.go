// Package skills_test Skills/MCP 发布管理门禁（P3-01，#26）：
//   - SKILL.md front matter 契约（name/description/compatibility 兼容性声明）；
//   - skills/checksums.txt 与技能树零漂移（安装侧 sha256 校验单源必须与
//     发布物一致——改技能文件必须跑 scripts/release-skills.sh 再生成）；
//   - install-skills.sh 本地模式幂等安装回归（--version 固定 Release 为远端
//     ref 安装，需网络，CI 不跑——契约由本地模式与 checksums 漂移门禁共同
//     锚定）。
package skills_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installableSkills 可安装技能清单（与 install-skills.sh SKILL_FILES_* 对齐；
// ocr-grading 是 builtin 插件的提示词引用面，非 Agent Skills 安装物，不在列）。
var installableSkills = []struct {
	name  string
	files []string
}{
	{"argus-mcp", []string{"SKILL.md", "references/tools.md"}},
	{"argus-triage", []string{"SKILL.md"}},
	{"argus-precommit", []string{"SKILL.md"}},
}

// frontMatter 解析 SKILL.md 头部 --- 包围的 YAML 块为 k: v 平面 map
// （只取标量行；带引号值后的同行注释按 shell 侧同款剥除）。
func frontMatter(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatalf("%s 缺 front matter（首行非 ---）", path)
	}
	out := map[string]string{}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		// 带引号值后的 " # 注释" 剥除（compatibility: "..."  # 注释）：
		// 取最后一个引号收尾（本文件值内不含转义引号）
		if strings.HasPrefix(v, "\"") {
			if end := strings.LastIndex(v, "\""); end > 0 {
				v = v[:end+1]
			}
		}
		out[k] = v
	}
	return out
}

// TestSKILLFrontMatterCompat 兼容性声明契约：name=目录名、description 非空、
// compatibility 为可解析的版本区间（">=X[.Y.Z] <Y[.Z]" 形态）。
func TestSKILLFrontMatterCompat(t *testing.T) {
	for _, s := range installableSkills {
		t.Run(s.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "skills", s.name, "SKILL.md")
			fm := frontMatter(t, path)
			if got := fm["name"]; got != s.name {
				t.Errorf("name = %q, want %q", got, s.name)
			}
			if strings.TrimSpace(fm["description"]) == "" {
				t.Error("description 不得为空（Agent Skills 触发面）")
			}
			compat, ok := fm["compatibility"]
			if !ok || strings.TrimSpace(compat) == "" {
				t.Fatal("缺 compatibility 兼容性声明（适配的 argus 服务版本区间）")
			}
			if !compatRangeRe.MatchString(compat) {
				t.Errorf("compatibility=%q 不是可解析区间（形如 %q）", compat, `">=6.9.0 <7.0.0"`)
			}
		})
	}
}

var compatRangeRe = regexp.MustCompile(`^"?(>=|<=|==)?\d+(\.\d+){0,2}(\s+(<|<=|>|>=)\s*\d+(\.\d+){0,2})?"?$`)

// TestChecksumsNoDrift 发布物校验和零漂移：skills/checksums.txt 与技能树逐一
// 核对（清单缺文件/树缺清单项/sha 不符都拦）。
func TestChecksumsNoDrift(t *testing.T) {
	root := filepath.Join("..", "..")
	sums, err := os.ReadFile(filepath.Join(root, "skills", "checksums.txt"))
	if err != nil {
		t.Fatalf("skills/checksums.txt 不存在：发布流程必须先跑 scripts/release-skills.sh: %v", err)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("checksums.txt 行非法（want `<sha256>  <path>`）: %q", line)
		}
		want, path := parts[0], parts[1]
		listed[path] = true
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("清单文件不可读: %s: %v", path, err)
			continue
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s sha256 漂移（want %s got %s）——改技能文件后须跑 scripts/release-skills.sh", path, want, got)
		}
	}
	// 反向：可安装技能的每个文件必须在清单内（新增文件漏生成校验和 = 安装侧裸奔）
	for _, s := range installableSkills {
		for _, f := range s.files {
			path := "skills/" + s.name + "/" + f
			if !listed[path] {
				t.Errorf("技能文件不在 checksums.txt 清单内: %s", path)
			}
		}
	}
}

// TestInstallScriptIdempotent 本地模式安装回归：检出树安装 → 幂等重装 →
// sha256 与源树一致（无残留/无半装）。
func TestInstallScriptIdempotent(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "install-skills.sh")
	target := t.TempDir()

	run := func() {
		cmd := exec.Command("bash", script, "--target", target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("安装失败: %v\n%s", err, out)
		}
	}
	run()
	run() // 幂等：重复执行无残留无报错

	for _, s := range installableSkills {
		for _, f := range s.files {
			src := filepath.Join(root, "skills", s.name, f)
			dst := filepath.Join(target, s.name, f)
			want, err := os.ReadFile(src)
			if err != nil {
				t.Fatalf("源文件缺失 %s: %v", src, err)
			}
			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("安装产物缺失 %s: %v", dst, err)
			}
			if string(want) != string(got) {
				t.Errorf("%s/%s 内容与源树不一致（幂等安装被破坏）", s.name, f)
			}
		}
	}
	// 意外残留检测：目标目录里除清单技能外不应有其余 argus 条目
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		known := false
		for _, s := range installableSkills {
			if e.Name() == s.name {
				known = true
			}
		}
		if !known {
			t.Errorf("安装目录出现清单外残留: %s", e.Name())
		}
	}
}

// TestInstallScriptVersionFlag 安装脚本必须暴露 --version 与 --no-verify
// （发布管理契约面；远端固定版本安装路径需网络，此处只做脚本面锚定）。
func TestInstallScriptVersionFlag(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-skills.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"--version", "--no-verify", "checksums.txt", "shasum -a 256"} {
		if !strings.Contains(s, want) {
			t.Errorf("install-skills.sh 缺契约要素: %q", want)
		}
	}
}
