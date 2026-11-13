// Package shellpit 仓库 shell 脚本的 bash 3.2 兼容坑门禁。
//
// 坑：bash 3.2 + UTF-8 locale 下，`$var` 紧跟非 ASCII 字符（如全角括号）会被
// 解析成「变量名一直吃到非 ASCII 之后」——`got=$got）` 展开的是变量 `got）`，
// 在 set -u 下直接 unbound 中止；更阴的是脚本带 `trap ... EXIT` 时该错误的
// 退出码被 trap 洗成 0（bash 3.2 特有），install-skills.sh 曾因此在 macOS 上
// 「成功退出但一个文件都没装」。Linux（bash 5）只按 ASCII 断名，CI 抓不到——
// 所以要这道静态门禁把回归钉死。修法一律花括号：${got}）。
//
// 扫描面：scripts/ 与 deploy/ 下全部 *.sh（单引号内不展开、跳过注释行）。
package shellpit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unbracedVarRe 匹配未加花括号的 $name 紧跟非 ASCII 字节（$1/$?/$@ 等不含
// 名字的展开不受此坑影响，不入名单）。
var unbracedVarRe = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]`)

// singleQuotedRe 剥掉单引号段（单引号内不展开，不是本坑的触发面）。
var singleQuotedRe = regexp.MustCompile(`'[^']*'`)

func TestNoUnbracedVarBeforeMultibyte(t *testing.T) {
	root := filepath.Join("..", "..")
	var scanDirs = []string{"scripts", "deploy"}
	found := 0
	for _, dir := range scanDirs {
		base := filepath.Join(root, dir)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".sh") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for i, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				scrubbed := singleQuotedRe.ReplaceAllString(line, "''")
				loc := unbracedVarRe.FindString(scrubbed)
				if loc == "" {
					continue
				}
				found++
				t.Errorf("%s:%d: %s —— bash 3.2 会把 %s 解析成变量名（set -u 下 unbound，EXIT trap 还会把退出码洗成 0）；改用花括号 ${...}",
					rel, i+1, strings.TrimSpace(line), loc)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s: %v", dir, err)
		}
	}
	if found == 0 {
		t.Log("scripts/ 与 deploy/ 无 $var 紧跟非 ASCII 的写法")
	}
}
