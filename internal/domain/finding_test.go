package domain

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// NormalizeSeverity: 词表内任意写法归一到 {high,medium,low}；越界归 low 且报 not-ok。
func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"high", SeverityHigh, true},
		{"HIGH", SeverityHigh, true},
		{"High", SeverityHigh, true},
		{"medium", SeverityMedium, true},
		{"MEDIUM", SeverityMedium, true},
		{"low", SeverityLow, true},
		{"Low", SeverityLow, true},
		{"critical", SeverityCritical, true}, // v5.5.0 四档：critical 独立档（textx#20）
		{"banana", SeverityLow, false},       // 越界：归 low 并报 not-ok
		{"", SeverityLow, false},
	}
	for _, c := range cases {
		got, ok := NormalizeSeverity(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("NormalizeSeverity(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// SeverityRank: high > medium > low 严格排序。
func TestSeverityRank(t *testing.T) {
	if !(SeverityRank(SeverityHigh) > SeverityRank(SeverityMedium) &&
		SeverityRank(SeverityMedium) > SeverityRank(SeverityLow)) {
		t.Errorf("rank order wrong: high=%d medium=%d low=%d",
			SeverityRank(SeverityHigh), SeverityRank(SeverityMedium), SeverityRank(SeverityLow))
	}
}

// HigherSeverity: 两个方向都取更严重者。
func TestHigherSeverity(t *testing.T) {
	if got := HigherSeverity(SeverityLow, SeverityHigh); got != SeverityHigh {
		t.Errorf("HigherSeverity(low, high) = %q, want high", got)
	}
	if got := HigherSeverity(SeverityHigh, SeverityLow); got != SeverityHigh {
		t.Errorf("HigherSeverity(high, low) = %q, want high", got)
	}
	if got := HigherSeverity(SeverityMedium, SeverityLow); got != SeverityMedium {
		t.Errorf("HigherSeverity(medium, low) = %q, want medium", got)
	}
}

// Fingerprint: 确定性、32 位 hex、行号敏感、负行号钳到 0。
func TestFingerprint(t *testing.T) {
	const file, comment = "a.go", "这里有个问题"
	fp1 := Fingerprint(file, comment, 10)
	if len(fp1) != 32 {
		t.Fatalf("fingerprint length = %d, want 32", len(fp1))
	}
	if _, err := hex.DecodeString(fp1); err != nil {
		t.Fatalf("fingerprint not hex: %v", err)
	}
	if fp1 != Fingerprint(file, comment, 10) {
		t.Error("fingerprint not deterministic")
	}
	if fp1 == Fingerprint(file, comment, 11) {
		t.Error("different line should give different fingerprint")
	}
	if fp1 == Fingerprint("b.go", comment, 10) {
		t.Error("different file should give different fingerprint")
	}
	// 负行号钳到 0：与行 0 同指纹
	if Fingerprint(file, comment, -5) != Fingerprint(file, comment, 0) {
		t.Error("negative line should clamp to 0")
	}
}

// FingerprintNorm: 与 Fingerprint 同指纹，且给出规范化评语。
func TestFingerprintNorm(t *testing.T) {
	const file, comment = "a.go", "这里有个问题"
	fp, norm := FingerprintNorm(file, comment, 7)
	if fp != Fingerprint(file, comment, 7) {
		t.Errorf("FingerprintNorm fp %q != Fingerprint %q", fp, Fingerprint(file, comment, 7))
	}
	if norm == "" {
		t.Error("normalized comment should be non-empty")
	}
	fpClamp, _ := FingerprintNorm(file, comment, -1)
	if fpClamp != Fingerprint(file, comment, 0) {
		t.Error("negative line should clamp to 0 in FingerprintNorm")
	}
}

// Evidence 字段（v6.18.0 Finding 级证据，v6.17.0 Claim 级同族）：JSON 双向携带；
// 零值 omitempty 不出线（旧消费者/存储面零影响）。渲染属性不入指纹——指纹仍由
// Fingerprint(file, comment, line) 派生，与 Evidence 无关（下方对照钉住）。
func TestFindingEvidenceJSON(t *testing.T) {
	in := `{"file":"a.go","line":3,"severity":"high","comment":"缺判空","evidence":"a.go:3 改动侧缺失判空"}`
	var f Finding
	if err := json.Unmarshal([]byte(in), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Evidence != "a.go:3 改动侧缺失判空" {
		t.Fatalf("Evidence 未从 JSON 携带: %q", f.Evidence)
	}
	out, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"evidence":"a.go:3 改动侧缺失判空"`) {
		t.Fatalf("Evidence 未回写 JSON: %s", out)
	}
	// 零值不出线（omitempty 契约）
	zero, err := json.Marshal(Finding{File: "x.go", Comment: "c"})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if strings.Contains(string(zero), "evidence") {
		t.Fatalf("零值 Evidence 不应出线: %s", zero)
	}
	// 指纹身份不受 Evidence 影响（渲染属性不入指纹纪律）
	if Fingerprint(f.File, f.Comment, f.Line) != Fingerprint("a.go", "缺判空", 3) {
		t.Fatal("Evidence 不应参与指纹派生")
	}
}
