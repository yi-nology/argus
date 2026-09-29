package domain

import (
	"encoding/hex"
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
		{"critical", SeverityHigh, true}, // 同义词归 high
		{"banana", SeverityLow, false},   // 越界：归 low 并报 not-ok
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
