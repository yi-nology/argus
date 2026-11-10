package ocrshim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

// writeHistory：OCR 原始 stdout（合法 JSON 原样嵌入）+ 归一化 findings 落盘，
// 文件名带 PR 且不含路径分隔符。
func TestWriteHistory(t *testing.T) {
	hdir := t.TempDir()
	p := shimPayload{Platform: "gitea", Owner: "o", Repo: "r", Number: "7",
		HeadSHA: "abc", BaseBranch: "main"}
	raw := `{"comments":[{"path":"a.go","content":"竞态","start_line":7,"severity":"critical"}]}`
	findings := []plugin.Finding{{File: "a.go", Line: 7, Severity: "high", Comment: "竞态"}}

	writeHistory(hdir, p, raw, findings)

	es, err := os.ReadDir(hdir)
	if err != nil || len(es) != 1 {
		t.Fatalf("应恰好 1 个留档文件: %v %v", es, err)
	}
	if !strings.HasSuffix(es[0].Name(), "-o-r-PR7.json") {
		t.Fatalf("文件名应带 PR 标识: %s", es[0].Name())
	}
	b, err := os.ReadFile(filepath.Join(hdir, es[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		PR       string           `json:"pr"`
		OcrRaw   json.RawMessage  `json:"ocr_raw"`
		Findings []plugin.Finding `json:"findings"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("留档应为合法 JSON: %v\n%s", err, b)
	}
	if got.PR != "gitea/o/r#7@abc" || len(got.Findings) != 1 || got.Findings[0].Severity != "high" {
		t.Fatalf("留档内容不符: pr=%q findings=%+v", got.PR, got.Findings)
	}
	var rawBack map[string]any
	if err := json.Unmarshal(got.OcrRaw, &rawBack); err != nil {
		t.Fatalf("ocr_raw 应为原样嵌入的 JSON 对象: %v", err)
	}
	if _, ok := rawBack["comments"]; !ok {
		t.Fatalf("ocr_raw 应保留原始结构: %s", got.OcrRaw)
	}
}

// 非 JSON stdout：降级为字符串字段，仍留档。
func TestWriteHistoryNonJSONRaw(t *testing.T) {
	hdir := t.TempDir()
	writeHistory(hdir, shimPayload{Platform: "gitea", Owner: "o", Repo: "r", Number: "7"},
		"不是 JSON 的输出", nil)
	es, _ := os.ReadDir(hdir)
	if len(es) != 1 {
		t.Fatalf("非 JSON 原文也应留档: %v", es)
	}
	b, _ := os.ReadFile(filepath.Join(hdir, es[0].Name()))
	if !strings.Contains(string(b), "不是 JSON 的输出") {
		t.Fatalf("原文应降级为字符串保留:\n%s", b)
	}
}
