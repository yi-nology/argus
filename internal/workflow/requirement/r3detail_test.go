package requirement

// get_detail 收编 goagent-kernel/spill 后的行为钉（v6.15.0）：分页语义、
// 续页指引、非法 ref 拒绝（含路径穿越——上游 sanitizeName 防线）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestGetDetailPagination(t *testing.T) {
	root := t.TempDir()
	w := &Workflow{SpillDir: root + "/t-test1", Log: nopLogger{}}
	content := strings.Repeat("x", 20_000)
	ref := w.stashDetail(content)
	if ref == "" {
		t.Fatal("stashDetail 应产出 ref")
	}

	tl, err := w.r3DetailTool()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(in getDetailIn) string {
		t.Helper()
		b, _ := json.Marshal(in)
		out, err := tl.(tool.InvokableTool).InvokableRun(ctx, string(b))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 首页：缺省 8KB 一页，带续页指引。
	first := run(getDetailIn{Ref: ref})
	if !strings.Contains(first, "分页取回") || !strings.Contains(first, "取下一页") {
		t.Fatalf("首页缺续页指引:\n%s", first)
	}

	// 续页：offset 翻页累计能取到全文（≥20000 x），且末页无续页指引。
	// 注意取「offset=」最后一次出现——页脚含本页 offset 与 next offset 两处。
	var seen int
	off := int64(0)
	for i := 0; i < 10; i++ {
		pg := run(getDetailIn{Ref: ref, Offset: off})
		seen += strings.Count(pg, "x")
		idx := strings.LastIndex(pg, "offset=")
		if idx < 0 {
			break // EOF
		}
		var next int64
		for _, ch := range pg[idx+7:] {
			if ch < '0' || ch > '9' {
				break
			}
			next = next*10 + int64(ch-'0')
		}
		if next <= off {
			t.Fatalf("next_offset 未前进: %d <= %d", next, off)
		}
		off = next
	}
	if seen < 20_000 {
		t.Fatalf("分页累计 %d < 全文 20000", seen)
	}

	// 非法 ref：路径穿越/空 ref/控制字符一律「无此详情」。
	for _, bad := range []string{"../x", "a/b", "", "d1\x00"} {
		if got := run(getDetailIn{Ref: bad}); !strings.Contains(got, "无此详情 ref") {
			t.Fatalf("ref %q 应拒: %s", bad, got)
		}
	}
}
