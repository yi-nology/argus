package testkit

import (
	"os/exec"
	"strings"
	"testing"
)

// 走真实 sh 还原：含空格、单引号、$ 与通配符的词必须逐字回来（正控），
// 裸拼同词必须跑偏（负控）——两个方向同形才说明引号真的在起作用。
func TestShqRoundTrip(t *testing.T) {
	word := `/tmp/a b/c'd/e$f/*`
	out, err := exec.Command("sh", "-c", "printf %s "+Shq(word)).Output()
	if err != nil {
		t.Fatalf("sh 执行失败: %v", err)
	}
	if string(out) != word {
		t.Fatalf("还原不逐字: got %q want %q", out, word)
	}
	// 负控：同一词不加引号经 sh 词分裂后必然不等于原词。
	raw, _ := exec.Command("sh", "-c", "printf %s "+word).Output()
	if string(raw) == word {
		t.Fatalf("裸拼竟与原词相同，负控无效: %q", raw)
	}
}

// 空串与纯单引号是边界：引号闭合转接不能把词拼接坏。
func TestShqEdge(t *testing.T) {
	for _, word := range []string{"", "'", "a'b'c"} {
		out, err := exec.Command("sh", "-c", "printf %s "+Shq(word)).Output()
		if err != nil {
			t.Fatalf("sh 执行失败(%q): %v", word, err)
		}
		if string(out) != word {
			t.Fatalf("边界还原失败: got %q want %q", out, word)
		}
	}
}

// 语法自检：拼出的脚本必须能过 sh -n（解析不炸）。
func TestShqScriptSyntax(t *testing.T) {
	script := "#!/bin/sh\necho \"$@\" >> " + Shq("/tmp/x y/z") + "\nexit 0\n"
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if err := cmd.Run(); err != nil {
		t.Fatalf("生成脚本语法不合法: %v\n%s", err, script)
	}
}
