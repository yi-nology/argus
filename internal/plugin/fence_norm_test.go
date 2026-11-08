package plugin

import "testing"

func TestNormalizeFenceInline(t *testing.T) {
	inner := `[{"a":1}]`
	inline := "审查完成。" + "```json\n" + inner + "\n```"
	got := NormalizeFence(inline)
	want := "审查完成。\n```json\n" + inner + "\n```"
	if got != want {
		t.Fatalf("行内 fence 应前置换行:\n got=%q\nwant=%q", got, want)
	}
	// 幂等：规范化后再跑一遍不变
	if again := NormalizeFence(got); again != want {
		t.Fatalf("应幂等: %q", again)
	}
}

func TestNormalizeFenceLineStartUntouched(t *testing.T) {
	linestart := "```json\n[{\"a\":1}]\n```"
	if got := NormalizeFence(linestart); got != linestart {
		t.Fatalf("行首 fence 不应改动: %q", got)
	}
}

func TestNormalizeFenceCodeSpanUntouched(t *testing.T) {
	span := "用 ```json x``` 这样的行内代码标注"
	if got := NormalizeFence(span); got != span {
		t.Fatalf("行内 code span（无换行）不应改动: %q", got)
	}
}
