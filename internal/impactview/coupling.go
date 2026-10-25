// coupling.go —— P2 历史共变（change coupling，CodeScene/CodeCharta 同思想）：
// 扫工作副本 git log 的文件共现，找「总是和改动文件一起被改」的隐性耦合——
// import 图到不了的逻辑依赖（同改同挂）。procx 执行 git（与 workcopy deepen
// 同原语），stderr 经 logredact 脱敏；任何失败静默降级（耦合是增强维度）。
package impactview

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/runtime/procx"
)

// gitLogFiles `git log --name-only` 的 commit×文件清单（按 commit 分组）。
// 边界用 --format=%x1e：每 commit 先输出一行记录分隔符（空 format 不产分组
// 空行，实测 commit 间无分隔会被并组）。
func gitLogFiles(ctx context.Context, dir string, maxCommits int, timeout time.Duration) ([][]string, error) {
	req := procx.RunRequest{
		Argv: []string{
			"git", "-C", dir, "log", "--name-only", "--format=%x1e", "--no-renames",
			"--max-count=" + strconv.Itoa(maxCommits), "--",
		},
		Timeout: timeout,
	}
	out, stderr, _, err := procx.Run(ctx, req)
	if err != nil {
		return nil, errorsRedacted(stderr, err)
	}
	const sep = "\x1e"
	var groups [][]string
	var cur []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == sep:
			if len(cur) > 0 {
				groups = append(groups, cur)
				cur = nil
			}
		case line != "":
			cur = append(cur, filepathSlash(line))
		}
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups, nil
}

// collectCoupling 统计与 changed 文件共现的其他文件，达 MinCoupleCount 次的
// 按次数降序取 Top MaxCoupling。changed 自身不进榜。
func collectCoupling(ctx context.Context, dir string, changed []string, maxCommits int, timeout time.Duration) ([]Couple, int, string) {
	if dir == "" || maxCommits <= 0 || len(changed) == 0 {
		return nil, 0, ""
	}
	groups, err := gitLogFiles(ctx, dir, maxCommits, timeout)
	if err != nil {
		return nil, 0, err.Error() // 失败记 Note，不产耦合数据
	}
	changeSet := map[string]bool{}
	for _, c := range changed {
		changeSet[c] = true
	}
	counts := map[string]int{}
	for _, g := range groups {
		hit := false
		for _, f := range g {
			if changeSet[f] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		seen := map[string]bool{} // 同一 commit 内去重
		for _, f := range g {
			if changeSet[f] || seen[f] {
				continue
			}
			seen[f] = true
			counts[f]++
		}
	}
	var couples []Couple
	for f, n := range counts {
		if n >= MinCoupleCount {
			couples = append(couples, Couple{Other: f, Count: n})
		}
	}
	sort.Slice(couples, func(i, j int) bool {
		if couples[i].Count != couples[j].Count {
			return couples[i].Count > couples[j].Count
		}
		return couples[i].Other < couples[j].Other
	})
	if len(couples) > MaxCoupling {
		couples = couples[:MaxCoupling]
	}
	// 补 File 侧（恒为某改动文件：取与该 Other 共现计数最高的改动文件，渲染需要）
	for i := range couples {
		couples[i].File = bestCoupleSource(groups, changeSet, couples[i].Other)
	}
	return couples, len(groups), ""
}

// bestCoupleSource 共变对另一侧的归属改动文件（共现次数最多者；并列取字典序）。
func bestCoupleSource(groups [][]string, changeSet map[string]bool, other string) string {
	type srcCount struct {
		file  string
		count int
	}
	var srcs []srcCount
	for _, c := range sortedKeys(changeSet) {
		n := 0
		for _, g := range groups {
			hasOther, hasSrc := false, false
			for _, f := range g {
				if f == other {
					hasOther = true
				}
				if f == c {
					hasSrc = true
				}
			}
			if hasOther && hasSrc {
				n++
			}
		}
		if n > 0 {
			srcs = append(srcs, srcCount{file: c, count: n})
		}
	}
	if len(srcs) == 0 {
		return ""
	}
	sort.Slice(srcs, func(i, j int) bool {
		if srcs[i].count != srcs[j].count {
			return srcs[i].count > srcs[j].count
		}
		return srcs[i].file < srcs[j].file
	})
	return srcs[0].file
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errorsRedacted git 失败回执（stderr 可能回显带凭证 URL，脱敏后拼接）。
func errorsRedacted(stderr string, err error) error {
	return &gitErr{msg: "git log 共变扫描失败: " + err.Error() + "; stderr: " + logredact.Redact(tailStr(stderr, 200))}
}

type gitErr struct{ msg string }

func (e *gitErr) Error() string { return e.msg }

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func filepathSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
