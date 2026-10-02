package requirement

import (
	"testing"

	"git.example.com/enjoydream/agentkit/textutil"
)

// 深挖 P0 配套：get_file 输出逐行带 4 位宽行号前缀，行号从 1 起、跨空行连续——
// 证据门要求 file:line 定位，无行号原文等于逼模型编造行号。
func TestNumberLines(t *testing.T) {
	got := textutil.NumberLines("package main\n\nfunc main() {}")
	want := "   1|package main\n   2|\n   3|func main() {}"
	if got != want {
		t.Fatalf("numberLines 输出不符:\n got: %q\nwant: %q", got, want)
	}
	if got := textutil.NumberLines(""); got != "" {
		t.Fatalf("空输入应返回空串, got %q", got)
	}
	// 尾随换行不产生空的第 N+1 行视觉噪声（保留一个空行语义即可）
	if got := textutil.NumberLines("a\n"); got != "   1|a" {
		t.Fatalf("尾换行应只折叠为末行, got %q", got)
	}
}
