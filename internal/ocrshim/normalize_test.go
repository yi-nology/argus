package ocrshim

import (
	"testing"
)

// OCR v1.12+ 新 schema：{"comments":[{path,content,start_line,severity,suggestion_code}]}。
func TestNormalizeNewSchemaWrapper(t *testing.T) {
	in := `{"comments":[
		{"path":"internal/a.go","content":"存在竞态","start_line":12,"end_line":14,"severity":"critical","suggestion_code":"加锁","category":"bug"},
		{"path":"b.go","content":"命名不清","start_line":3,"severity":"low"}
	]}`
	fs, err := normalizeFindings(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("应 2 条，got %d", len(fs))
	}
	f := fs[0]
	if f.File != "internal/a.go" || f.Line != 12 || f.Comment != "存在竞态" || f.SuggestedFix != "加锁" {
		t.Fatalf("字段映射不符: %+v", f)
	}
	if f.Severity != "critical" { // v5.5.0 四档：critical 独立档（textx#20）
		t.Fatalf("critical 应保留独立档，got %q", f.Severity)
	}
	if fs[1].Line != 3 || fs[1].Severity != "low" {
		t.Fatalf("第二条不符: %+v", fs[1])
	}
}

// 裸数组形态同词表。
func TestNormalizeBareArray(t *testing.T) {
	in := `[{"path":"a.go","content":"x","start_line":1,"severity":"medium"}]`
	fs, err := normalizeFindings(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].File != "a.go" || fs[0].Severity != "medium" {
		t.Fatalf("不符: %+v", fs)
	}
}

// start_line 缺失时回退 end_line。
func TestNormalizeEndLineFallback(t *testing.T) {
	fs, err := normalizeFindings(`[{"path":"a.go","content":"x","end_line":9,"severity":"low"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Line != 9 {
		t.Fatalf("应回退 end_line: %+v", fs)
	}
}

// 旧版 schema（path/comment/line/level 变体）→ 复用 adapter_cli 容错解析兜底。
func TestNormalizeLegacySchemaFallback(t *testing.T) {
	in := `{"comments":[{"path":"a.go","line":5,"comment":"旧字段","level":"high"}]}`
	fs, err := normalizeFindings(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].File != "a.go" || fs[0].Line != 5 || fs[0].Comment != "旧字段" || fs[0].Severity != "high" {
		t.Fatalf("旧 schema 应兜底解析: %+v", fs)
	}
}

// 干净 PR 的合法零意见（两种 schema、两种包裹）。
func TestNormalizeZeroFindings(t *testing.T) {
	for _, in := range range2() {
		fs, err := normalizeFindings(in)
		if err != nil {
			t.Fatalf("%s 应为合法零结果，got err=%v", in, err)
		}
		if len(fs) != 0 {
			t.Fatalf("%s 应 0 条，got %d", in, len(fs))
		}
	}
}

func range2() []string {
	return []string{"[]", `{"comments":[]}`}
}

// 空输出 / 非法 JSON → 错误（OCR 崩溃或输出被吞，须上抛触发部分失败容忍）。
func TestNormalizeGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "not json", `{"foo":1}`} {
		if _, err := normalizeFindings(in); err == nil {
			t.Fatalf("%q 应报错", in)
		}
	}
}
