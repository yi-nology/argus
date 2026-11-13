package qconsole

import (
	"os"
	"testing"
)

// TestLoadRealBaselineProjection 真实基线投影对齐（批 B2）：仓内
// eval/results/baseline-latest.json 存在时验证 EvalTrend JSON tag 与
// evalq.Report 产出对齐；文件缺位（CI/gitignored）跳过。
func TestLoadRealBaselineProjection(t *testing.T) {
	path := "../../eval/results/baseline-latest.json"
	if _, err := os.Stat(path); err != nil {
		t.Skip("真实基线文件不在（gitignored），跳过")
	}
	tr := LoadEvalTrend(path)
	if tr == nil {
		t.Fatal("LoadEvalTrend 返回 nil：字段不对齐或文件读失败")
	}
	if tr.Model == "stub" {
		t.Skip("文件是打桩占位（stub），非真实基线")
	}
	if tr.Model == "" || len(tr.ByLanguage) != 4 {
		t.Fatalf("投影不完整: model=%q langs=%d", tr.Model, len(tr.ByLanguage))
	}
	if tr.Overall.P == 0 && tr.Overall.R == 0 {
		t.Fatal("overall 指标全零：JSON tag 不对齐")
	}
	t.Logf("model=%q stability=%.3f overall=P%.3f/R%.3f/FP%.3f/Miss%.3f langs=%d",
		tr.Model, tr.Stability, tr.Overall.P, tr.Overall.R, tr.Overall.FP, tr.Overall.Miss, len(tr.ByLanguage))
}
