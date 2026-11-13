package service

import (
	"encoding/json"
	"testing"

	"github.com/yi-nology/argus/internal/plugin"
)

// TestTaskFindings 往返钉 ResultJSON findings 载荷契约（读写两端必须同步演化；
// 此前 marker 契约漂移的教训——往返测试是防漂移的硬门）。
func TestTaskFindings(t *testing.T) {
	in := []plugin.Finding{{File: "a.go", Line: 3, Severity: "high", Comment: "x", By: "sast"}}
	raw, err := json.Marshal(map[string]any{"findings": in})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, ok := taskFindings(string(raw))
	if !ok || len(got) != 1 || got[0].File != "a.go" || got[0].Line != 3 {
		t.Fatalf("往返失真: ok=%v got=%+v", ok, got)
	}
	// 两态语义：解析失败 (nil,false) vs 空 findings 数组 (空切片,true) 可区分。
	if fs, ok := taskFindings("not-json"); ok || fs != nil {
		t.Errorf("垃圾输入应 (nil,false)，got ok=%v fs=%v", ok, fs)
	}
	if fs, ok := taskFindings(`{"findings":[]}`); !ok || fs == nil || len(fs) != 0 {
		t.Errorf("空数组应 (空切片,true)，got ok=%v fs=%v", ok, fs)
	}
	if fs, ok := taskFindings(""); ok || fs != nil {
		t.Errorf("空串应 (nil,false)，got ok=%v fs=%v", ok, fs)
	}
}

// TestFindingDTO v6.18.0 list_findings 出口带 evidence：Finding 级证据字段随
// 结构化清单直出（MCP 副驾消费面与 schema 同步演化——字段入 schema 而出口
// 不带，等于消费面看不到）。
func TestFindingDTO(t *testing.T) {
	f := plugin.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "x", By: "sast",
		SuggestedFix: "if v == nil { return }", Evidence: "a.go:3 判空缺失"}
	m := findingDTO(f)
	if m["evidence"] != "a.go:3 判空缺失" {
		t.Fatalf("dto 缺 evidence: %+v", m)
	}
	if m["suggested_fix"] != "if v == nil { return }" || m["fp"] == nil {
		t.Fatalf("既有字段应保持: %+v", m)
	}
}
